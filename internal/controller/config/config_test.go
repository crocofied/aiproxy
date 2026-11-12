package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/data" || cfg.Listen != ":8080" || cfg.ProviderListen != "127.0.0.1:8081" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.ProviderURL() != "http://127.0.0.1:8081/traefik/config" || cfg.UIBackendURL() != "http://127.0.0.1:8080" {
		t.Errorf("urls: %s %s", cfg.ProviderURL(), cfg.UIBackendURL())
	}
	if len(cfg.RestartCommand) != 0 {
		t.Errorf("RestartCommand = %v, want none", cfg.RestartCommand)
	}
}

func TestLoadPrefill(t *testing.T) {
	cfg, err := Load([]string{
		"LANPROXY_LISTEN=0.0.0.0:9000",
		"LANPROXY_TRAEFIK_RESTART_COMMAND=/command/s6-svc -r /run/service/traefik",
		"LANPROXY_BASE_DOMAIN=home.example.com",
		"LANPROXY_ACME_STAGING=true",
		"LANPROXY_DNS_RESOLVERS=1.1.1.1, 8.8.8.8:53",
		"LANPROXY_DNS_ENV_CF_DNS_API_TOKEN=secret=with=equals",
		"LANPROXY_ADMIN_PASSWORD=hunter22",
		"UNRELATED=x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UIBackendURL() != "http://127.0.0.1:9000" {
		t.Errorf("UIBackendURL() = %s", cfg.UIBackendURL())
	}
	if !reflect.DeepEqual(cfg.RestartCommand, []string{"/command/s6-svc", "-r", "/run/service/traefik"}) {
		t.Errorf("RestartCommand = %v", cfg.RestartCommand)
	}
	p := cfg.Prefill
	if p.BaseDomain != "home.example.com" || !p.Staging || p.AdminPassword != "hunter22" {
		t.Errorf("prefill: %+v", p)
	}
	if !reflect.DeepEqual(p.DNSResolvers, []string{"1.1.1.1", "8.8.8.8:53"}) {
		t.Errorf("DNSResolvers = %v", p.DNSResolvers)
	}
	if !reflect.DeepEqual(p.DNSEnv, map[string]string{"CF_DNS_API_TOKEN": "secret=with=equals"}) {
		t.Errorf("DNSEnv = %v", p.DNSEnv)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]string{
		"LANPROXY_LISTEN=8080":                   EnvListen,
		"LANPROXY_PROVIDER_LISTEN=0.0.0.0:8081":  "loopback",
		"LANPROXY_PROVIDER_LISTEN=192.0.2.1:808": "loopback",
		"LANPROXY_LOG_LEVEL=loud":                EnvLogLevel,
		"LANPROXY_ACME_STAGING=maybe":            EnvACMEStaging,
	}
	for env, want := range tests {
		_, err := Load([]string{env})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(%s) error = %v, want one containing %q", env, err, want)
		}
	}
}
