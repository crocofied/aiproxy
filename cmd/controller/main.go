// Command controller is the brain of lanproxy: it stores the routes,
// receives the reports of the agents, serves the web interface and
// tells Traefik what to proxy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lanproxy/internal/controller/certs"
	"lanproxy/internal/controller/config"
	"lanproxy/internal/controller/httpapi"
	"lanproxy/internal/controller/store"
	"lanproxy/internal/controller/traefik"
	"lanproxy/internal/controller/ui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	// "controller healthcheck" is used by the container's HEALTHCHECK,
	// so the image needs no curl or wget.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(cfg))
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err := run(cfg, log); err != nil {
		log.Error("controller failed", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The data directory holds credentials and private keys.
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	// A Docker volume already exists with wider permissions; tighten it.
	if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
		log.Warn("cannot restrict permissions of the data directory", "error", err)
	}
	paths := traefik.PathsIn(cfg.DataDir)
	if err := paths.EnsureDirs(); err != nil {
		return err
	}

	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "lanproxy.db"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Write Traefik's static configuration before anything else:
	// Traefik waits for this file to appear.
	manager := traefik.NewManager(paths, cfg.ProviderURL(), cfg.RestartCommand, log)
	settings, err := db.LoadSettings(ctx)
	if err != nil {
		return err
	}
	if err := manager.Apply(ctx, settings, false); err != nil {
		// Not fatal: the web interface must stay reachable to fix it.
		log.Error("apply traefik configuration", "error", err)
	}

	// The log watcher needs the DNS credentials only to blank them out.
	secrets := func() []string {
		env, err := traefik.ReadDNSEnv(paths.DNSEnvDir)
		if err != nil {
			return nil
		}
		values := make([]string, 0, len(env))
		for _, v := range env {
			values = append(values, v)
		}
		return values
	}
	logs := certs.NewLogWatcher(paths.LogFile, secrets, log)
	go logs.Run(ctx)

	server := &httpapi.Server{
		Config: cfg, Store: db, Traefik: manager, Paths: paths, Logs: logs,
		Log: log, Version: version, UI: ui.Files(), Now: time.Now,
	}

	public := newHTTPServer(cfg.Listen, server.Handler())
	provider := newHTTPServer(cfg.ProviderListen, server.ProviderHandler())

	// Both servers run in their own goroutine and report through errs
	// if they stop unexpectedly.
	errs := make(chan error, 2)
	for _, srv := range []*http.Server{public, provider} {
		listener, err := net.Listen("tcp", srv.Addr) //nolint:noctx // lives as long as the process
		if err != nil {
			return fmt.Errorf("listen on %s: %w", srv.Addr, err)
		}
		go func() { errs <- srv.Serve(listener) }()
	}
	log.Info("controller started", "version", version, "listen", cfg.Listen,
		"traefik_provider", cfg.ProviderListen, "data_dir", cfg.DataDir)

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	// Give running requests a moment to finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = public.Shutdown(shutdownCtx)
	_ = provider.Shutdown(shutdownCtx)
	return nil
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: handler,
		// Timeouts keep slow or stuck clients from holding connections
		// (and memory) forever.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// healthcheck asks the running controller for its status and returns
// the process exit code.
func healthcheck(cfg config.Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.UIBackendURL()+"/api/status", nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
