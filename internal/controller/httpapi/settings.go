package httpapi

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"lanproxy/internal/controller/auth"
	"lanproxy/internal/controller/certs"
	"lanproxy/internal/controller/routing"
	"lanproxy/internal/controller/store"
	"lanproxy/internal/controller/traefik"
)

// settingsRequest is what the setup wizard and the settings page send.
type settingsRequest struct {
	store.Settings
	// DNSEnv are the DNS provider credentials. nil (field absent) keeps
	// the stored ones; otherwise they are replaced as a whole.
	DNSEnv map[string]string `json:"dns_env"`
	// Password is only used by the setup wizard.
	Password string `json:"password"`
}

// settingsResponse never contains credentials, only their names.
type settingsResponse struct {
	store.Settings
	UIHost     string   `json:"ui_host"`
	DNSEnvKeys []string `json:"dns_env_keys"`
}

var providerPattern = regexp.MustCompile(`^[a-z0-9]+$`)

// validateSettings normalises the settings and checks them. The
// returned error is a message for the admin.
func validateSettings(in store.Settings) (store.Settings, error) {
	s := in
	s.BaseDomain = routing.NormalizeHostname(s.BaseDomain)
	s.UISubdomain = strings.ToLower(strings.TrimSpace(s.UISubdomain))
	s.ACMEEmail = strings.TrimSpace(s.ACMEEmail)
	s.DNSProvider = strings.ToLower(strings.TrimSpace(s.DNSProvider))
	s.HostnamePattern = strings.ToLower(strings.TrimSpace(s.HostnamePattern))

	if !routing.ValidDNSName(s.BaseDomain) {
		return s, userError{"Die Basis-Domain ist ungültig (Beispiel: home.example.com)"}
	}
	if !routing.ValidLabel(s.UISubdomain) {
		return s, userError{"Die Subdomain für die Oberfläche ist ungültig (Beispiel: proxy)"}
	}
	if addr, err := mail.ParseAddress(s.ACMEEmail); err != nil || addr.Address != s.ACMEEmail {
		return s, userError{"Die E-Mail-Adresse für Let's Encrypt ist ungültig"}
	}
	if !providerPattern.MatchString(s.DNSProvider) {
		return s, userError{"Der DNS-Anbieter muss der Provider-Code aus der Traefik-Doku sein (z. B. cloudflare)"}
	}

	resolvers := make([]string, 0, len(s.DNSResolvers))
	for _, r := range s.DNSResolvers {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		// "1.1.1.1" is completed to "1.1.1.1:53".
		if _, err := netip.ParseAddr(r); err == nil {
			r = net.JoinHostPort(r, "53")
		}
		if _, err := netip.ParseAddrPort(r); err != nil {
			return s, userError{fmt.Sprintf("Der DNS-Resolver %q ist keine IP-Adresse (Beispiel: 1.1.1.1)", r)}
		}
		resolvers = append(resolvers, r)
	}
	s.DNSResolvers = resolvers
	if len(resolvers) == 0 && !s.SkipPropagationCheck {
		return s, userError{"Mindestens ein öffentlicher DNS-Resolver ist nötig"}
	}
	if s.PropagationDelaySeconds < 0 || s.PropagationDelaySeconds > 3600 {
		return s, userError{"Die Wartezeit muss zwischen 0 und 3600 Sekunden liegen"}
	}
	if s.AgentTimeoutSeconds < 30 || s.AgentTimeoutSeconds > 86400 {
		return s, userError{"Der Agent-Timeout muss zwischen 30 und 86400 Sekunden liegen"}
	}
	if err := routing.CheckPattern(s.HostnamePattern); err != nil {
		return s, userError{"Hostname-Muster: " + err.Error()}
	}
	return s, nil
}

// applySettings stores new settings (and credentials, if given),
// rewrites Traefik's static configuration and restarts Traefik if that
// is necessary.
func (s *Server) applySettings(ctx context.Context, settings store.Settings, dnsEnv map[string]string) error {
	credentialsChanged := false
	if dnsEnv != nil {
		old, err := traefik.ReadDNSEnv(s.Paths.DNSEnvDir)
		if err != nil {
			return err
		}
		if !maps.Equal(old, dnsEnv) {
			if err := traefik.WriteDNSEnv(s.Paths.DNSEnvDir, dnsEnv); err != nil {
				return err
			}
			credentialsChanged = true
		}
	}
	if err := s.Store.SaveSettings(ctx, settings); err != nil {
		return err
	}
	if err := s.Traefik.Apply(ctx, settings, credentialsChanged); err != nil {
		return err
	}
	// Domain or pattern may have changed: recompute all label routes
	// from the containers we already know.
	return s.resyncLabelRoutes(ctx, settings)
}

func (s *Server) resyncLabelRoutes(ctx context.Context, settings store.Settings) error {
	agents, err := s.Store.Agents(ctx)
	if err != nil {
		return err
	}
	containers, err := s.Store.Containers(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		desired := routing.DesiredLabelRoutes(agent.Name, apiContainers(containers, agent.ID), settings)
		if err := s.Store.SyncLabelRoutes(ctx, agent.ID, desired, s.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) settingsResponse(settings store.Settings) (settingsResponse, error) {
	env, err := traefik.ReadDNSEnv(s.Paths.DNSEnvDir)
	if err != nil {
		return settingsResponse{}, err
	}
	// Only the names leave the server; maps.Keys drops the values.
	keys := slices.Sorted(maps.Keys(env))
	return settingsResponse{Settings: settings, UIHost: settings.UIHost(), DNSEnvKeys: keys}, nil
}

// handleSetupDefaults returns the values the wizard starts with. It
// works only while no admin exists.
func (s *Server) handleSetupDefaults(w http.ResponseWriter, r *http.Request) {
	if s.setupDone(w, r) {
		return
	}
	p := s.Config.Prefill
	settings := store.DefaultSettings()
	settings.BaseDomain, settings.UISubdomain = p.BaseDomain, p.UISubdomain
	settings.ACMEEmail, settings.DNSProvider, settings.Staging = p.ACMEEmail, p.DNSProvider, p.Staging
	if len(p.DNSResolvers) > 0 {
		settings.DNSResolvers = p.DNSResolvers
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings,
		// Secrets from the environment are announced, not revealed.
		"password_preset": p.AdminPassword != "",
		"dns_env_preset":  slices.Sorted(maps.Keys(p.DNSEnv)),
	})
}

// setupDone answers 409 and returns true if the setup has already been
// completed. This is what keeps the unauthenticated setup endpoints
// from being used to take over an installed system.
func (s *Server) setupDone(w http.ResponseWriter, r *http.Request) bool {
	_, done, err := s.Store.AdminPasswordHash(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return true
	}
	if done {
		writeError(w, http.StatusConflict, "Die Einrichtung ist bereits abgeschlossen")
	}
	return done
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.setupDone(w, r) {
		return
	}
	var req settingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	p := s.Config.Prefill

	password := req.Password
	if password == "" {
		password = p.AdminPassword
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusBadRequest, passwordMessage(err))
		return
	}
	settings, err := validateSettings(req.Settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Credentials from the environment, overridden by what was typed.
	dnsEnv := maps.Clone(p.DNSEnv)
	maps.Copy(dnsEnv, req.DNSEnv)
	if err := traefik.CheckDNSEnv(dnsEnv); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(dnsEnv) == 0 {
		writeError(w, http.StatusBadRequest, "Für den DNS-Anbieter fehlen die Zugangsdaten")
		return
	}

	if err := s.applySettings(r.Context(), settings, dnsEnv); err != nil {
		s.fail(w, r, err)
		return
	}
	// The password is stored last: if anything above fails, the wizard
	// can simply be submitted again.
	if err := s.Store.SetAdminPasswordHash(r.Context(), hash); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(w, r); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("setup completed", "base_domain", settings.BaseDomain, "dns_provider", settings.DNSProvider, "staging", settings.Staging)
	writeJSON(w, http.StatusOK, map[string]string{"ui_host": settings.UIHost()})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp, err := s.settingsResponse(settings)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	settings, err := validateSettings(req.Settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DNSEnv != nil {
		if err := traefik.CheckDNSEnv(req.DNSEnv); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(req.DNSEnv) == 0 {
			writeError(w, http.StatusBadRequest, "Für den DNS-Anbieter fehlen die Zugangsdaten")
			return
		}
	}
	if err := s.applySettings(r.Context(), settings, req.DNSEnv); err != nil {
		s.fail(w, r, err)
		return
	}
	resp, err := s.settingsResponse(settings)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// certificateResponse is the certificate status shown in the UI.
type certificateResponse struct {
	certs.Status
	BaseDomain string `json:"base_domain"`
	Staging    bool   `json:"staging"`
	// LastError is the most recent failed issuance or renewal that has
	// not been followed by a success.
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
}

func (s *Server) handleCertificate(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status, err := certs.Read(s.Paths.ACMEFile(settings.Staging), settings.BaseDomain, s.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := certificateResponse{Status: status, BaseDomain: settings.BaseDomain, Staging: settings.Staging}
	if s.Logs != nil {
		message, at := s.Logs.LastACMEError()
		// An error older than the current certificate has been
		// overcome by a successful issuance.
		if message != "" && (status.IssuedAt == nil || at.After(*status.IssuedAt)) {
			resp.LastError, resp.LastErrorAt = message, at.UTC().Format("2006-01-02T15:04:05Z")
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
