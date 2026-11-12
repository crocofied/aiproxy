// Package config reads the controller's start-up configuration from
// environment variables. Everything the admin can change at runtime
// lives in the database instead (store.Settings).
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Names of the environment variables the controller understands.
const (
	EnvDataDir        = "LANPROXY_DATA_DIR"
	EnvListen         = "LANPROXY_LISTEN"
	EnvProviderListen = "LANPROXY_PROVIDER_LISTEN"
	EnvRestartCommand = "LANPROXY_TRAEFIK_RESTART_COMMAND"
	EnvAgentImage     = "LANPROXY_AGENT_IMAGE"
	EnvAgentBuild     = "LANPROXY_AGENT_BUILD_CONTEXT"
	EnvLogLevel       = "LANPROXY_LOG_LEVEL"

	// Values that pre-fill the setup wizard.
	EnvAdminPassword = "LANPROXY_ADMIN_PASSWORD"
	EnvBaseDomain    = "LANPROXY_BASE_DOMAIN"
	EnvUISubdomain   = "LANPROXY_UI_SUBDOMAIN"
	EnvACMEEmail     = "LANPROXY_ACME_EMAIL"
	EnvDNSProvider   = "LANPROXY_DNS_PROVIDER"
	EnvACMEStaging   = "LANPROXY_ACME_STAGING"
	EnvDNSResolvers  = "LANPROXY_DNS_RESOLVERS"
	// LANPROXY_DNS_ENV_<NAME>=value passes the variable NAME to the DNS
	// provider, e.g. LANPROXY_DNS_ENV_CF_DNS_API_TOKEN.
	EnvDNSEnvPrefix = "LANPROXY_DNS_ENV_"
)

// Config is the start-up configuration.
type Config struct {
	DataDir string
	// Listen is the address of the web interface and API.
	Listen string
	// ProviderListen is where Traefik fetches its dynamic configuration.
	// It must be a loopback address: the endpoint has no authentication.
	ProviderListen string
	// RestartCommand restarts Traefik; empty if Traefik is not managed.
	RestartCommand []string
	// AgentImage and AgentBuildContext appear in the compose file that
	// the UI generates for new agents.
	AgentImage        string
	AgentBuildContext string
	LogLevel          slog.Level
	Prefill           Prefill
}

// Prefill holds values for the setup wizard. AdminPassword and DNSEnv
// are secrets: they are used if the admin leaves the fields empty, but
// never sent to the browser.
type Prefill struct {
	AdminPassword string
	BaseDomain    string
	UISubdomain   string
	ACMEEmail     string
	DNSProvider   string
	Staging       bool
	DNSResolvers  []string
	DNSEnv        map[string]string
}

// Load builds a Config. environ is the full environment as returned by
// os.Environ ("KEY=value" strings); it is needed to find the variables
// with the LANPROXY_DNS_ENV_ prefix.
func Load(environ []string) (Config, error) {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		if key, value, ok := strings.Cut(kv, "="); ok {
			env[key] = value
		}
	}
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(env[key]); v != "" {
			return v
		}
		return fallback
	}

	cfg := Config{
		DataDir:           get(EnvDataDir, "/data"),
		Listen:            get(EnvListen, ":8080"),
		ProviderListen:    get(EnvProviderListen, "127.0.0.1:8081"),
		RestartCommand:    strings.Fields(env[EnvRestartCommand]),
		AgentImage:        get(EnvAgentImage, "lanproxy-agent:latest"),
		AgentBuildContext: get(EnvAgentBuild, ""),
		LogLevel:          slog.LevelInfo,
		Prefill: Prefill{
			AdminPassword: env[EnvAdminPassword],
			BaseDomain:    get(EnvBaseDomain, ""),
			UISubdomain:   get(EnvUISubdomain, ""),
			ACMEEmail:     get(EnvACMEEmail, ""),
			DNSProvider:   get(EnvDNSProvider, ""),
			DNSEnv:        map[string]string{},
		},
	}

	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvListen, err)
	}
	host, _, err := net.SplitHostPort(cfg.ProviderListen)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvProviderListen, err)
	}
	if addr, err := netip.ParseAddr(host); err != nil || !addr.IsLoopback() {
		return Config{}, fmt.Errorf("%s must be a loopback address such as 127.0.0.1:8081", EnvProviderListen)
	}

	if raw := get(EnvLogLevel, ""); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvLogLevel, err)
		}
	}
	if raw := get(EnvACMEStaging, ""); raw != "" {
		if cfg.Prefill.Staging, err = strconv.ParseBool(raw); err != nil {
			return Config{}, fmt.Errorf("%s: must be true or false", EnvACMEStaging)
		}
	}
	for _, r := range strings.Split(get(EnvDNSResolvers, ""), ",") {
		if r = strings.TrimSpace(r); r != "" {
			cfg.Prefill.DNSResolvers = append(cfg.Prefill.DNSResolvers, r)
		}
	}
	for key, value := range env {
		if name, ok := strings.CutPrefix(key, EnvDNSEnvPrefix); ok && name != "" && value != "" {
			cfg.Prefill.DNSEnv[name] = value
		}
	}
	return cfg, nil
}

// ProviderURL is the URL Traefik polls for its dynamic configuration.
func (c Config) ProviderURL() string {
	return "http://" + c.ProviderListen + ProviderPath
}

// ProviderPath is the path of the dynamic configuration endpoint.
const ProviderPath = "/traefik/config"

// UIBackendURL is the address under which Traefik, running in the same
// container, reaches the web interface.
func (c Config) UIBackendURL() string {
	_, port, _ := net.SplitHostPort(c.Listen) // validated in Load
	return "http://127.0.0.1:" + port
}
