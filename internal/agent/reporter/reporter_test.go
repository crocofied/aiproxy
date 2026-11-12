package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanproxy/api"
)

const testToken = "agent1.s3cr3t-token-value"

// fakeDocker returns a fixed container list, or an error while failing.
type fakeDocker struct {
	containers []api.Container
	failing    atomic.Bool
}

func (f *fakeDocker) ListContainers(context.Context) ([]api.Container, error) {
	if f.failing.Load() {
		return nil, errors.New("docker is down")
	}
	return f.containers, nil
}

// fakeController records every report it receives.
type fakeController struct {
	t       *testing.T
	srv     *httptest.Server
	reports chan api.Report
	status  atomic.Int32 // HTTP status to answer with
}

func newFakeController(t *testing.T) *fakeController {
	t.Helper()
	fc := &fakeController{t: t, reports: make(chan api.Report, 100)}
	fc.status.Store(http.StatusNoContent)
	fc.srv = httptest.NewServer(http.HandlerFunc(fc.handle))
	t.Cleanup(fc.srv.Close)
	return fc
}

func (fc *fakeController) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != api.ReportPath {
		fc.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		fc.t.Errorf("Authorization header = %q", got)
	}
	var report api.Report
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		fc.t.Errorf("decode report: %v", err)
	}
	status := int(fc.status.Load())
	if status == http.StatusNoContent {
		fc.reports <- report
	}
	w.WriteHeader(status)
}

// next waits for the next accepted report.
func (fc *fakeController) next(t *testing.T) api.Report {
	t.Helper()
	select {
	case r := <-fc.reports:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a report")
		return api.Report{}
	}
}

// expectNone fails if a report arrives within d.
func (fc *fakeController) expectNone(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case r := <-fc.reports:
		t.Fatalf("unexpected report with reason %q", r.Reason)
	case <-time.After(d):
	}
}

// syncBuffer is a bytes.Buffer that is safe to use from two goroutines:
// the reporter logs into it while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start runs a Reporter against fc until the test ends. It returns the
// channel for simulated Docker events and the captured log output.
func start(t *testing.T, fc *fakeController, docker ContainerLister, change func(*Config)) (chan<- struct{}, *syncBuffer) {
	t.Helper()
	u, err := url.Parse(fc.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ControllerURL: u,
		Token:         testToken,
		HostIP:        "192.0.2.20",
		Interval:      time.Hour, // no heartbeats unless a test wants them
		Debounce:      20 * time.Millisecond,
		MinBackoff:    10 * time.Millisecond,
		MaxBackoff:    40 * time.Millisecond,
		Version:       "test",
	}
	if change != nil {
		change(&cfg)
	}

	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(cfg, docker, log).Run(ctx, events)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return events, logs
}

func TestStartupReport(t *testing.T) {
	fc := newFakeController(t)
	docker := &fakeDocker{containers: []api.Container{{
		ID: "abc", Name: "web",
		Ports: []api.Port{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}},
	}}}
	start(t, fc, docker, nil)

	got := fc.next(t)
	if got.Reason != api.ReasonStartup {
		t.Errorf("Reason = %q, want %q", got.Reason, api.ReasonStartup)
	}
	if got.HostIP != "192.0.2.20" || got.AgentVersion != "test" {
		t.Errorf("unexpected report header: %+v", got)
	}
	if len(got.Containers) != 1 || got.Containers[0].Name != "web" || got.Containers[0].Ports[0].HostPort != 8080 {
		t.Errorf("unexpected containers: %+v", got.Containers)
	}
}

func TestEventsAreDebounced(t *testing.T) {
	fc := newFakeController(t)
	events, _ := start(t, fc, &fakeDocker{}, func(c *Config) { c.Debounce = 100 * time.Millisecond })
	fc.next(t) // startup

	// A burst of events must lead to exactly one report.
	for range 5 {
		events <- struct{}{}
	}
	if got := fc.next(t); got.Reason != api.ReasonEvent {
		t.Errorf("Reason = %q, want %q", got.Reason, api.ReasonEvent)
	}
	fc.expectNone(t, 250*time.Millisecond)
}

func TestHeartbeat(t *testing.T) {
	fc := newFakeController(t)
	start(t, fc, &fakeDocker{}, func(c *Config) { c.Interval = 30 * time.Millisecond })
	fc.next(t) // startup

	for range 2 {
		if got := fc.next(t); got.Reason != api.ReasonHeartbeat {
			t.Errorf("Reason = %q, want %q", got.Reason, api.ReasonHeartbeat)
		}
	}
}

func TestRetriesAfterControllerError(t *testing.T) {
	fc := newFakeController(t)
	fc.status.Store(http.StatusServiceUnavailable)
	_, logs := start(t, fc, &fakeDocker{}, nil)

	fc.expectNone(t, 100*time.Millisecond) // several failed attempts
	fc.status.Store(http.StatusNoContent)

	// The report that finally gets through is still the startup report.
	if got := fc.next(t); got.Reason != api.ReasonStartup {
		t.Errorf("Reason = %q, want %q", got.Reason, api.ReasonStartup)
	}
	if !strings.Contains(logs.String(), "503") {
		t.Errorf("log does not mention the failure:\n%s", logs.String())
	}
}

func TestRetriesAfterDockerError(t *testing.T) {
	fc := newFakeController(t)
	docker := &fakeDocker{}
	docker.failing.Store(true)
	start(t, fc, docker, nil)

	fc.expectNone(t, 100*time.Millisecond)
	docker.failing.Store(false)
	fc.next(t)
}

func TestRejectedTokenIsNeverLogged(t *testing.T) {
	fc := newFakeController(t)
	fc.status.Store(http.StatusUnauthorized)
	_, logs := start(t, fc, &fakeDocker{}, nil)

	fc.expectNone(t, 100*time.Millisecond)

	out := logs.String()
	if !strings.Contains(out, ErrUnauthorized.Error()) {
		t.Errorf("log does not explain the rejection:\n%s", out)
	}
	if strings.Contains(out, testToken) || strings.Contains(out, "s3cr3t") {
		t.Errorf("log leaks the token:\n%s", out)
	}
}

// fakeWatcher emits one event per call and then reports a broken stream.
type fakeWatcher struct{ calls atomic.Int32 }

func (f *fakeWatcher) WatchEvents(ctx context.Context, onEvent func()) error {
	if f.calls.Add(1) > 3 {
		<-ctx.Done() // stay connected from the fourth call on
		return ctx.Err()
	}
	onEvent()
	return errors.New("stream closed")
}

func TestWatchReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := &fakeWatcher{}
	log := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	events := Watch(ctx, watcher, time.Millisecond, log)

	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("no event received")
	}

	// Watch must keep reconnecting after the stream broke.
	deadline := time.Now().Add(5 * time.Second)
	for watcher.calls.Load() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("WatchEvents called %d times, want 4", watcher.calls.Load())
		}
		time.Sleep(time.Millisecond)
	}
}
