// Command agent reports the containers of one Docker host to the
// lanproxy controller. It only sends; it opens no listening port.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lanproxy/internal/agent/config"
	"lanproxy/internal/agent/docker"
	"lanproxy/internal/agent/reporter"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// dockerRetryDelay is the pause before reconnecting to the event stream.
const dockerRetryDelay = 5 * time.Second

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	// The context is cancelled on Ctrl-C or "docker stop" (SIGTERM);
	// everything below stops when it is.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hostIP := cfg.HostIP
	if hostIP == "" {
		hostIP = "(auto-detect)"
	}
	// Note what is absent here: the token.
	log.Info("agent starting",
		"version", version,
		"controller", cfg.ControllerURL.Redacted(),
		"host_ip", hostIP,
		"heartbeat", cfg.Interval.String(),
		"docker_socket", cfg.DockerSocket,
	)

	client := docker.New(cfg.DockerSocket)
	events := reporter.Watch(ctx, client, dockerRetryDelay, log)

	rep := reporter.New(reporter.Config{
		ControllerURL: cfg.ControllerURL,
		Token:         cfg.Token,
		HostIP:        cfg.HostIP,
		Interval:      cfg.Interval,
		Version:       version,
	}, client, log)
	rep.Run(ctx, events)

	log.Info("agent stopped")
}
