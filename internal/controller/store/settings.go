package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	keySettings      = "settings"
	keyAdminPassword = "admin_password_hash"
)

// Settings is everything the admin can configure. DNS credentials are
// deliberately not part of it: they live in files, see package traefik.
type Settings struct {
	BaseDomain  string `json:"base_domain"`
	UISubdomain string `json:"ui_subdomain"`
	ACMEEmail   string `json:"acme_email"`
	// DNSProvider is the provider code from the Traefik documentation,
	// e.g. "cloudflare".
	DNSProvider string `json:"dns_provider"`
	// Staging selects the Let's Encrypt test environment.
	Staging bool `json:"staging"`
	// DNSResolvers are asked whether the challenge record is visible,
	// as "ip:port". They must be public resolvers.
	DNSResolvers []string `json:"dns_resolvers"`
	// SkipPropagationCheck waits PropagationDelaySeconds instead of
	// asking the resolvers, for networks that intercept outgoing DNS.
	SkipPropagationCheck    bool `json:"skip_propagation_check"`
	PropagationDelaySeconds int  `json:"propagation_delay_seconds"`
	// HostnamePattern builds the hostname of label routes.
	HostnamePattern string `json:"hostname_pattern"`
	// AgentTimeoutSeconds: routes of an agent that has been silent for
	// longer than this are switched off.
	AgentTimeoutSeconds int `json:"agent_timeout_seconds"`
}

// DefaultSettings returns the values used until the admin changes them.
func DefaultSettings() Settings {
	return Settings{
		DNSResolvers:            []string{"1.1.1.1:53", "9.9.9.9:53"},
		PropagationDelaySeconds: 60,
		HostnamePattern:         "{service}.{domain}",
		AgentTimeoutSeconds:     300,
	}
}

// UIHost returns the full hostname of the web interface, or "" before
// the setup is complete.
func (s Settings) UIHost() string {
	if s.BaseDomain == "" || s.UISubdomain == "" {
		return ""
	}
	return s.UISubdomain + "." + s.BaseDomain
}

// AgentTimeout returns AgentTimeoutSeconds as a duration.
func (s Settings) AgentTimeout() time.Duration {
	return time.Duration(s.AgentTimeoutSeconds) * time.Second
}

func (s *Store) getValue(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s *Store) setValue(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// LoadSettings returns the stored settings, or the defaults if nothing
// has been saved yet.
func (s *Store) LoadSettings(ctx context.Context) (Settings, error) {
	settings := DefaultSettings()
	raw, ok, err := s.getValue(ctx, keySettings)
	if err != nil || !ok {
		return settings, err
	}
	// Fields missing in the stored JSON keep their default value.
	err = json.Unmarshal([]byte(raw), &settings)
	return settings, err
}

// SaveSettings stores the settings.
func (s *Store) SaveSettings(ctx context.Context, settings Settings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.setValue(ctx, keySettings, string(raw))
}

// AdminPasswordHash returns the stored password hash; ok is false as
// long as no admin exists, which means the setup has not been done.
func (s *Store) AdminPasswordHash(ctx context.Context) (hash string, ok bool, err error) {
	return s.getValue(ctx, keyAdminPassword)
}

// SetAdminPasswordHash stores the hash of the admin password.
func (s *Store) SetAdminPasswordHash(ctx context.Context, hash string) error {
	return s.setValue(ctx, keyAdminPassword, hash)
}

// CreateSession stores the hash of a session token.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, expires_at) VALUES (?, ?)", tokenHash, expires.Unix())
	return err
}

// SessionValid reports whether a session with this token hash exists
// and has not expired.
func (s *Store) SessionValid(ctx context.Context, tokenHash string, now time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sessions WHERE token_hash = ? AND expires_at > ?", tokenHash, now.Unix()).Scan(&n)
	return n > 0, err
}

// DeleteSession removes one session (logout).
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// DeleteAllSessions logs everybody out, used after a password change.
func (s *Store) DeleteAllSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions")
	return err
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", now.Unix())
	return err
}
