package traefik

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lanproxy/internal/controller/routing"
	"lanproxy/internal/controller/store"
)

func setup() store.Settings {
	s := store.DefaultSettings()
	s.BaseDomain = "home.example.com"
	s.UISubdomain = "proxy"
	s.ACMEEmail = "admin@example.com"
	s.DNSProvider = "cloudflare"
	return s
}

// lookup walks a decoded JSON document along keys.
func lookup(t *testing.T, doc any, keys ...string) any {
	t.Helper()
	for _, key := range keys {
		m, ok := doc.(map[string]any)
		if !ok {
			t.Fatalf("%v: not an object at %q", keys, key)
		}
		doc = m[key]
	}
	return doc
}

func staticDoc(t *testing.T, s store.Settings) any {
	t.Helper()
	raw, err := StaticConfig(s, PathsIn("/data"), "http://127.0.0.1:8081/traefik/config")
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestStaticConfig(t *testing.T) {
	doc := staticDoc(t, setup())
	acme := []string{"certificatesResolvers", ResolverName, "acme"}

	checks := []struct {
		path []string
		want any
	}{
		{[]string{"entryPoints", "web", "address"}, ":80"},
		{[]string{"entryPoints", "websecure", "address"}, ":443"},
		{[]string{"providers", "http", "endpoint"}, "http://127.0.0.1:8081/traefik/config"},
		{append(acme, "email"), "admin@example.com"},
		{append(acme, "storage"), "/data/acme/acme-production.json"},
		{append(acme, "caServer"), nil}, // production: Traefik's default
		{append(acme, "dnsChallenge", "provider"), "cloudflare"},
		{append(acme, "dnsChallenge", "resolvers"), []any{"1.1.1.1:53", "9.9.9.9:53"}},
		// DNS-01 is the only challenge type we ever configure.
		{append(acme, "httpChallenge"), nil},
		{append(acme, "tlsChallenge"), nil},
	}
	for _, c := range checks {
		if got := lookup(t, doc, c.path...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v", strings.Join(c.path, "."), got, c.want)
		}
	}
}

func TestStaticConfigStaging(t *testing.T) {
	s := setup()
	s.Staging = true
	doc := staticDoc(t, s)
	acme := []string{"certificatesResolvers", ResolverName, "acme"}
	if got := lookup(t, doc, append(acme, "caServer")...); got != letsEncryptStaging {
		t.Errorf("caServer = %v", got)
	}
	if got := lookup(t, doc, append(acme, "storage")...); got != "/data/acme/acme-staging.json" {
		t.Errorf("storage = %v, want a separate file for staging", got)
	}
}

func TestStaticConfigSkipPropagationCheck(t *testing.T) {
	s := setup()
	s.SkipPropagationCheck = true
	s.PropagationDelaySeconds = 90
	doc := staticDoc(t, s)
	challenge := []string{"certificatesResolvers", ResolverName, "acme", "dnsChallenge"}
	if got := lookup(t, doc, append(challenge, "propagation", "disableChecks")...); got != true {
		t.Errorf("disableChecks = %v", got)
	}
	if got := lookup(t, doc, append(challenge, "propagation", "delayBeforeChecks")...); got != "90s" {
		t.Errorf("delayBeforeChecks = %v", got)
	}
}

func TestStaticConfigBeforeSetup(t *testing.T) {
	doc := staticDoc(t, store.DefaultSettings())
	if got := lookup(t, doc, "certificatesResolvers"); got != nil {
		t.Errorf("certificatesResolvers = %v, want none before the setup", got)
	}
}

func TestDynamicConfig(t *testing.T) {
	views := []routing.View{
		{Route: store.Route{ID: 1, Hostname: "wiki.home.example.com"}, Status: routing.StatusActive,
			Servers: []string{"http://192.0.2.5:8080", "http://192.0.2.5:8081"}},
		{Route: store.Route{ID: 2, Hostname: "nas.home.example.com", BackendHTTPS: true}, Status: routing.StatusActive,
			Servers: []string{"https://192.0.2.9:5001"}},
		{Route: store.Route{ID: 3, Hostname: "off.home.example.com"}, Status: routing.StatusAgentOffline},
		{Route: store.Route{ID: 4, Hostname: "wiki.home.example.com"}, Status: routing.StatusConflict},
	}
	cfg := DynamicConfig(setup(), views, "http://127.0.0.1:8080")

	if len(cfg.HTTP.Routers) != 3 || len(cfg.HTTP.Services) != 3 {
		t.Fatalf("got %d routers and %d services, want 3 each (ui + 2 active)", len(cfg.HTTP.Routers), len(cfg.HTTP.Services))
	}
	ui := cfg.HTTP.Routers["lanproxy-ui"]
	if ui.Rule != "Host(`proxy.home.example.com`)" || cfg.HTTP.Services["lanproxy-ui"].LoadBalancer.Servers[0].URL != "http://127.0.0.1:8080" {
		t.Errorf("ui router: %+v", ui)
	}
	wantTLS := TLS{CertResolver: ResolverName, Domains: []TLSDomain{{Main: "home.example.com", SANs: []string{"*.home.example.com"}}}}
	for name, r := range cfg.HTTP.Routers {
		if !reflect.DeepEqual(r.TLS, wantTLS) {
			t.Errorf("router %s: tls = %+v, want the wildcard certificate", name, r.TLS)
		}
		if !reflect.DeepEqual(r.EntryPoints, []string{"websecure"}) {
			t.Errorf("router %s: entryPoints = %v", name, r.EntryPoints)
		}
	}
	if got := cfg.HTTP.Services["route-1"].LoadBalancer; len(got.Servers) != 2 || got.ServersTransport != "" {
		t.Errorf("route-1: %+v", got)
	}
	if got := cfg.HTTP.Services["route-2"].LoadBalancer; got.ServersTransport != skipVerifyTransport {
		t.Errorf("route-2 transport = %q", got.ServersTransport)
	}
	if !cfg.HTTP.ServersTransports[skipVerifyTransport].InsecureSkipVerify {
		t.Error("skip-verify transport is missing")
	}

	// Before the setup the document is empty but well-formed.
	raw, _ := json.Marshal(DynamicConfig(store.DefaultSettings(), views, "x"))
	if string(raw) != `{"http":{"routers":{},"services":{}}}` {
		t.Errorf("empty config = %s", raw)
	}
}

func TestDNSEnvFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dns-env")
	if err := WriteDNSEnv(dir, map[string]string{"CF_DNS_API_TOKEN": "tok en=with'chars", "OLD": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteDNSEnv(dir, map[string]string{"CF_DNS_API_TOKEN": "tok en=with'chars"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDNSEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CF_DNS_API_TOKEN": "tok en=with'chars"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadDNSEnv() = %v, want %v (old variable removed)", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, "CF_DNS_API_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credential file mode = %o, want 600", perm)
	}

	for _, bad := range []map[string]string{
		{"lower": "x"}, {"../ESCAPE": "x"}, {"A B": "x"}, {"OK": "line1\nline2"}, {"EMPTY": ""},
	} {
		err := WriteDNSEnv(dir, bad)
		if err == nil {
			t.Errorf("WriteDNSEnv(%v) = nil, want an error", bad)
		} else if strings.Contains(err.Error(), "line1") {
			t.Errorf("error leaks the value: %v", err)
		}
	}
}

func TestManagerRestartsOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	paths := PathsIn(dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	// The "restart command" appends a line to a file we can count.
	marker := filepath.Join(dir, "restarts")
	cmd := []string{"sh", "-c", "echo x >> " + marker}
	m := NewManager(paths, "http://127.0.0.1:1/x", cmd, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	restarts := func() int {
		data, _ := os.ReadFile(marker)
		return strings.Count(string(data), "x")
	}

	s := setup()
	steps := []struct {
		name   string
		change func()
		force  bool
		want   int
	}{
		{"first write", func() {}, false, 1},
		{"unchanged", func() {}, false, 1},
		{"changed", func() { s.Staging = true }, false, 2},
		{"forced", func() {}, true, 3},
	}
	for _, step := range steps {
		step.change()
		if err := m.Apply(ctx, s, step.force); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := restarts(); got != step.want {
			t.Errorf("%s: %d restarts, want %d", step.name, got, step.want)
		}
	}
	info, _ := os.Stat(paths.StaticFile)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("static config mode = %o, want 600", perm)
	}
}
