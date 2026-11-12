// Package reporter sends snapshots of the local Docker host to the
// controller: once at startup, shortly after every Docker event and
// otherwise periodically as a heartbeat.
package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"lanproxy/api"
	"lanproxy/internal/agent/hostip"
)

// ErrUnauthorized means the controller rejected the agent's token.
var ErrUnauthorized = errors.New("controller rejected the agent token")

// ContainerLister is the part of the Docker client the reporter needs.
// Accepting an interface instead of *docker.Client lets tests pass a fake.
type ContainerLister interface {
	ListContainers(ctx context.Context) ([]api.Container, error)
}

// EventWatcher is implemented by the Docker client, see WatchEvents.
type EventWatcher interface {
	WatchEvents(ctx context.Context, onEvent func()) error
}

// Config controls a Reporter. Zero durations get sensible defaults.
type Config struct {
	ControllerURL *url.URL
	// Token is sent as a bearer token. It must never be logged.
	Token string
	// HostIP is reported as is; if empty it is detected for every report,
	// so an address change (DHCP) is picked up without a restart.
	HostIP string
	// Interval is the time between heartbeats.
	Interval time.Duration
	// Debounce is how long to wait after a Docker event before reporting.
	// "docker compose up" causes a burst of events; waiting a moment
	// turns the burst into a single report.
	Debounce time.Duration
	// MinBackoff and MaxBackoff bound the wait between failed attempts.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	Version    string
}

// Reporter sends reports. Create one with New.
type Reporter struct {
	cfg       Config
	docker    ContainerLister
	client    *http.Client
	log       *slog.Logger
	reportURL string
	hostname  string
}

// New returns a Reporter that reads from docker and reports to the
// controller named in cfg.
func New(cfg Config, docker ContainerLister, log *slog.Logger) *Reporter {
	if cfg.Debounce == 0 {
		cfg.Debounce = time.Second
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = 2 * time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = time.Minute
	}
	// In host network mode this is the name of the Docker host itself.
	hostname, _ := os.Hostname()

	return &Reporter{
		cfg:       cfg,
		docker:    docker,
		client:    &http.Client{Timeout: 15 * time.Second},
		log:       log,
		reportURL: cfg.ControllerURL.JoinPath(api.ReportPath).String(),
		hostname:  hostname,
	}
}

// Run sends reports until ctx is cancelled. A value on events means
// "something changed in Docker".
//
// The loop is a small state machine around a single timer, which always
// holds the time of the next report:
//
//   - after a successful report: one heartbeat interval,
//   - after a Docker event: the debounce delay,
//   - after a failed report: the backoff delay, doubling up to MaxBackoff.
func (r *Reporter) Run(ctx context.Context, events <-chan struct{}) {
	timer := time.NewTimer(0) // fires at once: the startup report
	defer timer.Stop()

	var (
		reason  = api.ReasonStartup
		backoff = r.cfg.MinBackoff
		// eventPending: an event already moved the timer forward. Further
		// events must not move it again, or a steady stream of events
		// would postpone the report forever.
		eventPending = false
		// failing: the last attempt failed and the timer holds the retry
		// time. Events must not shorten it; the retry sends a fresh
		// snapshot anyway.
		failing = false
	)

	for {
		select {
		case <-ctx.Done():
			return

		case <-events:
			if failing || eventPending {
				continue
			}
			eventPending = true
			if reason == api.ReasonHeartbeat {
				reason = api.ReasonEvent
			}
			timer.Reset(r.cfg.Debounce)

		case <-timer.C:
			eventPending = false
			err := r.send(ctx, reason)
			if err != nil {
				if ctx.Err() != nil {
					return // shutting down, not a real failure
				}
				failing = true
				r.log.Warn("report failed, will retry", "error", err, "retry_in", backoff.String())
				timer.Reset(backoff)
				backoff = min(backoff*2, r.cfg.MaxBackoff)
				continue
			}
			failing = false
			backoff = r.cfg.MinBackoff
			reason = api.ReasonHeartbeat
			timer.Reset(r.cfg.Interval)
		}
	}
}

// send takes a snapshot of the Docker host and posts it to the controller.
func (r *Reporter) send(ctx context.Context, reason string) error {
	containers, err := r.docker.ListContainers(ctx)
	if err != nil {
		return err
	}

	hostIP := r.cfg.HostIP
	if hostIP == "" {
		if hostIP, err = hostip.Detect(ctx, r.cfg.ControllerURL); err != nil {
			return err
		}
	}

	body, err := json.Marshal(api.Report{
		AgentVersion: r.cfg.Version,
		Hostname:     r.hostname,
		HostIP:       hostIP,
		Reason:       reason,
		Containers:   containers,
	})
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.reportURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// The token travels only in this header. Errors from net/http contain
	// the URL but never the headers, so they are safe to log.
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Read (a bounded amount of) the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("controller answered %s", resp.Status)
	}

	r.log.Debug("report sent", "reason", reason, "containers", len(containers), "host_ip", hostIP)
	return nil
}

// Watch forwards Docker events to the returned channel and reconnects to
// Docker whenever the event stream breaks. It stops when ctx is cancelled.
func Watch(ctx context.Context, docker EventWatcher, retryDelay time.Duration, log *slog.Logger) <-chan struct{} {
	// Capacity 1 is enough: the channel only says "something changed".
	// If a signal is already waiting, another one adds no information.
	events := make(chan struct{}, 1)
	notify := func() {
		select {
		case events <- struct{}{}:
		default:
		}
	}

	go func() {
		for {
			err := docker.WatchEvents(ctx, notify)
			if ctx.Err() != nil {
				return
			}
			log.Warn("docker event stream ended, reconnecting", "error", err, "retry_in", retryDelay.String())

			select {
			case <-ctx.Done():
				return
			case <-time.After(retryDelay):
			}
			// Events may have been missed while disconnected.
			notify()
		}
	}()
	return events
}
