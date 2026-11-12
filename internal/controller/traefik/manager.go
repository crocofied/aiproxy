package traefik

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"lanproxy/internal/controller/store"
)

// Manager writes the static configuration and restarts Traefik when
// that configuration changes.
type Manager struct {
	paths       Paths
	providerURL string
	// restartCommand restarts the Traefik process, e.g.
	// ["s6-svc", "-r", "/run/service/traefik"]. Empty means that Traefik
	// is not managed by us (development).
	restartCommand []string
	log            *slog.Logger

	mu sync.Mutex // serialises Apply
}

// NewManager returns a Manager. providerURL is where Traefik fetches the
// dynamic configuration from.
func NewManager(paths Paths, providerURL string, restartCommand []string, log *slog.Logger) *Manager {
	return &Manager{paths: paths, providerURL: providerURL, restartCommand: restartCommand, log: log}
}

// Apply writes the static configuration for s. Traefik is restarted if
// the file changed or if forceRestart is set (the DNS credentials, which
// Traefik also only reads at start, were changed).
func (m *Manager) Apply(ctx context.Context, s store.Settings, forceRestart bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	want, err := StaticConfig(s, m.paths, m.providerURL)
	if err != nil {
		return err
	}
	have, err := os.ReadFile(m.paths.StaticFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed := !bytes.Equal(have, want)
	if changed {
		if err := writeFileAtomic(m.paths.StaticFile, want, 0o600); err != nil {
			return fmt.Errorf("write traefik config: %w", err)
		}
	}
	if !changed && !forceRestart {
		return nil
	}
	return m.restart(ctx)
}

func (m *Manager) restart(ctx context.Context) error {
	if len(m.restartCommand) == 0 {
		m.log.Info("traefik configuration changed; no restart command configured, restart Traefik yourself")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// The command comes from our own configuration, not from user input,
	// and is executed directly, without a shell.
	cmd := exec.CommandContext(ctx, m.restartCommand[0], m.restartCommand[1:]...) //nolint:gosec
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restart traefik: %w: %s", err, bytes.TrimSpace(out))
	}
	m.log.Info("traefik restarted")
	return nil
}
