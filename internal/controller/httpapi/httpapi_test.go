package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"lanproxy/api"
	"lanproxy/internal/controller/auth"
	"lanproxy/internal/controller/config"
	"lanproxy/internal/controller/store"
	"lanproxy/internal/controller/traefik"
)

const (
	testPassword = "correct horse battery"
	testDNSToken = "cf-token-very-secret"
)

func TestMain(m *testing.M) {
	auth.Cost = 4 // the minimum: keeps the many logins in these tests fast
	os.Exit(m.Run())
}

// env is a complete controller with a browser-like client.
type env struct {
	t        *testing.T
	server   *Server
	public   *httptest.Server
	provider *httptest.Server
	client   *http.Client // keeps cookies, like a browser
	now      time.Time
	logs     *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	paths := traefik.PathsIn(dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg, err := config.Load([]string{"LANPROXY_DATA_DIR=" + dir, "LANPROXY_AGENT_IMAGE=example/agent:1"})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, now: time.Unix(1_800_000_000, 0), logs: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.server = &Server{
		Config: cfg, Store: db, Paths: paths, Log: log, Version: "test",
		Traefik: traefik.NewManager(paths, cfg.ProviderURL(), nil, log),
		UI:      fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "_app/immutable/x.js": {Data: []byte("js")}},
		Now:     func() time.Time { return e.now },
	}
	e.public = httptest.NewServer(e.server.Handler())
	e.provider = httptest.NewServer(e.server.ProviderHandler())
	t.Cleanup(e.public.Close)
	t.Cleanup(e.provider.Close)

	jar, _ := cookiejar.New(nil)
	e.client = &http.Client{Jar: jar}
	return e
}

// do sends a request and decodes the JSON response into out (if not nil).
func (e *env) do(client *http.Client, method, path string, body, out any, headers ...string) int {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.public.URL+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// No response may ever contain a secret.
	for _, secret := range []string{testPassword, testDNSToken} {
		if bytes.Contains(raw, []byte(secret)) {
			e.t.Errorf("%s %s: response contains the secret %q", method, path, secret)
		}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

// call is do with the logged-in client.
func (e *env) call(method, path string, body, out any) int {
	e.t.Helper()
	return e.do(e.client, method, path, body, out)
}

func setupBody() map[string]any {
	return map[string]any{
		"password":     testPassword,
		"base_domain":  "Home.Example.com",
		"ui_subdomain": "proxy",
		"acme_email":   "admin@example.com",
		"dns_provider": "cloudflare",
		"staging":      true,
		"dns_env":      map[string]string{"CF_DNS_API_TOKEN": testDNSToken},
		// Everything else keeps the defaults the wizard was given.
		"dns_resolvers":         []string{"1.1.1.1", "9.9.9.9:53"},
		"hostname_pattern":      "{service}.{domain}",
		"agent_timeout_seconds": 300,
	}
}

func (e *env) setup() {
	e.t.Helper()
	if status := e.call("POST", "/api/setup", setupBody(), nil); status != http.StatusOK {
		e.t.Fatalf("setup: status %d", status)
	}
}

// addAgent registers an agent and returns its token.
func (e *env) addAgent(name string) (id int64, token string) {
	e.t.Helper()
	var created struct {
		Agent struct {
			ID int64 `json:"id"`
		} `json:"agent"`
		Token string `json:"token"`
		Image string `json:"image"`
	}
	if status := e.call("POST", "/api/agents", map[string]string{"name": name}, &created); status != http.StatusCreated {
		e.t.Fatalf("create agent: status %d", status)
	}
	if created.Image != "example/agent:1" {
		e.t.Errorf("image = %q", created.Image)
	}
	return created.Agent.ID, created.Token
}

func (e *env) report(token, hostIP string, containers ...api.Container) int {
	e.t.Helper()
	body := api.Report{AgentVersion: "t", Hostname: "h", HostIP: hostIP, Reason: api.ReasonEvent, Containers: containers}
	// Agents are not browsers: a client without cookies.
	return e.do(http.DefaultClient, "POST", api.ReportPath, body, nil, "Authorization", "Bearer "+token)
}

func (e *env) routes() map[string]routeJSON {
	e.t.Helper()
	var list []routeJSON
	if status := e.call("GET", "/api/routes", nil, &list); status != http.StatusOK {
		e.t.Fatalf("list routes: status %d", status)
	}
	byHost := make(map[string]routeJSON)
	for _, r := range list {
		byHost[r.Hostname+"|"+r.Status] = r
	}
	return byHost
}

func (e *env) traefikConfig() traefik.Dynamic {
	e.t.Helper()
	resp, err := http.Get(e.provider.URL + config.ProviderPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var cfg traefik.Dynamic
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		e.t.Fatal(err)
	}
	return cfg
}

func web(name string, labels map[string]string, hostPort uint16) api.Container {
	return api.Container{
		ID: "id-" + name, Name: name, Image: "img", Labels: labels,
		Ports: []api.Port{{HostIP: "0.0.0.0", HostPort: hostPort, ContainerPort: 80, Protocol: "tcp"}},
	}
}

var enabled = map[string]string{"proxy.enable": "true"}

func TestSetupAndLogin(t *testing.T) {
	e := newEnv(t)

	var status struct {
		SetupDone     bool `json:"setup_done"`
		Authenticated bool `json:"authenticated"`
	}
	e.call("GET", "/api/status", nil, &status)
	if status.SetupDone || status.Authenticated {
		t.Errorf("fresh status: %+v", status)
	}
	if code := e.call("GET", "/api/routes", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated request: status %d, want 401", code)
	}

	// Invalid input is rejected and leaves the setup open.
	bad := setupBody()
	bad["base_domain"] = "not a domain"
	if code := e.call("POST", "/api/setup", bad, nil); code != http.StatusBadRequest {
		t.Errorf("invalid setup: status %d, want 400", code)
	}
	short := setupBody()
	short["password"] = "short"
	if code := e.call("POST", "/api/setup", short, nil); code != http.StatusBadRequest {
		t.Errorf("short password: status %d, want 400", code)
	}

	e.setup()
	e.call("GET", "/api/status", nil, &status)
	if !status.SetupDone || !status.Authenticated {
		t.Errorf("status after setup: %+v", status)
	}

	// The setup cannot be repeated to take over the installation.
	if code := e.do(http.DefaultClient, "POST", "/api/setup", setupBody(), nil); code != http.StatusConflict {
		t.Errorf("second setup: status %d, want 409", code)
	}
	if code := e.do(http.DefaultClient, "GET", "/api/setup", nil, nil); code != http.StatusConflict {
		t.Errorf("setup defaults after setup: status %d, want 409", code)
	}

	// Settings come back normalised and without the credentials.
	var settings settingsResponse
	e.call("GET", "/api/settings", nil, &settings)
	if settings.BaseDomain != "home.example.com" || settings.UIHost != "proxy.home.example.com" || !settings.Staging {
		t.Errorf("settings: %+v", settings)
	}
	if len(settings.DNSEnvKeys) != 1 || settings.DNSEnvKeys[0] != "CF_DNS_API_TOKEN" {
		t.Errorf("dns_env_keys = %v", settings.DNSEnvKeys)
	}
	if len(settings.DNSResolvers) != 2 || settings.DNSResolvers[0] != "1.1.1.1:53" {
		t.Errorf("resolvers = %v", settings.DNSResolvers)
	}

	// Traefik's static configuration was written, readable by us only.
	static, err := os.ReadFile(e.server.Paths.StaticFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(static), "acme-staging-v02") || !strings.Contains(string(static), `"provider": "cloudflare"`) {
		t.Errorf("static config:\n%s", static)
	}
	if strings.Contains(string(static), testDNSToken) {
		t.Error("static config contains the DNS token")
	}
	token, err := os.ReadFile(filepath.Join(e.server.Paths.DNSEnvDir, "CF_DNS_API_TOKEN"))
	if err != nil || strings.TrimSpace(string(token)) != testDNSToken {
		t.Errorf("credential file: %q, %v", token, err)
	}

	// Logout, wrong password, right password.
	e.call("POST", "/api/logout", nil, nil)
	if code := e.call("GET", "/api/settings", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("after logout: status %d, want 401", code)
	}
	if code := e.call("POST", "/api/login", map[string]string{"password": "wrong"}, nil); code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", code)
	}
	if code := e.call("POST", "/api/login", map[string]string{"password": testPassword}, nil); code != http.StatusOK {
		t.Errorf("login: status %d, want 200", code)
	}
	if code := e.call("GET", "/api/settings", nil, nil); code != http.StatusOK {
		t.Errorf("after login: status %d, want 200", code)
	}

	if strings.Contains(e.logs.String(), testPassword) || strings.Contains(e.logs.String(), testDNSToken) {
		t.Errorf("log contains a secret:\n%s", e.logs.String())
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	anon := &http.Client{}
	for range maxFailures {
		e.do(anon, "POST", "/api/login", map[string]string{"password": "wrong"}, nil)
	}
	// Now even the right password has to wait.
	if code := e.do(anon, "POST", "/api/login", map[string]string{"password": testPassword}, nil); code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: status %d, want 429", maxFailures, code)
	}
	e.now = e.now.Add(failureWindow + time.Second)
	if code := e.do(anon, "POST", "/api/login", map[string]string{"password": testPassword}, nil); code != http.StatusOK {
		t.Errorf("after the window: status %d, want 200", code)
	}
}

func TestCrossOriginRequestsAreRejected(t *testing.T) {
	e := newEnv(t)
	e.setup()
	body := map[string]string{"name": "x"}
	if code := e.do(e.client, "POST", "/api/agents", body, nil, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("foreign origin: status %d, want 403", code)
	}
	if code := e.do(e.client, "POST", "/api/agents", body, nil, "Origin", e.public.URL); code != http.StatusCreated {
		t.Errorf("own origin: status %d, want 201", code)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	e.setup()
	change := map[string]string{"current": "wrong", "new": "another good one"}
	if code := e.call("POST", "/api/password", change, nil); code != http.StatusForbidden {
		t.Errorf("wrong current password: status %d, want 403", code)
	}
	change["current"] = testPassword
	if code := e.call("POST", "/api/password", change, nil); code != http.StatusOK {
		t.Errorf("change password: status %d", code)
	}
	if code := e.call("GET", "/api/settings", nil, nil); code != http.StatusOK {
		t.Errorf("session after password change: status %d", code)
	}
	anon := &http.Client{}
	if code := e.do(anon, "POST", "/api/login", map[string]string{"password": "another good one"}, nil); code != http.StatusOK {
		t.Errorf("login with new password: status %d", code)
	}
}

func TestAgentReportsAndRoutes(t *testing.T) {
	e := newEnv(t)
	e.setup()
	agentID, token := e.addAgent("nas")

	// --- token handling ---
	for name, bad := range map[string]string{
		"garbage":      "garbage",
		"wrong secret": strings.Split(token, ".")[0] + ".wrong",
		"wrong agent":  "999." + strings.SplitN(token, ".", 2)[1],
	} {
		if code := e.report(bad, "192.0.2.5"); code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, code)
		}
	}
	if code := e.report(token, "not-an-ip"); code != http.StatusBadRequest {
		t.Errorf("bad host ip: status %d, want 400", code)
	}
	if strings.Contains(e.logs.String(), strings.SplitN(token, ".", 2)[1]) {
		t.Error("log contains the agent token")
	}

	// --- discovery ---
	code := e.report(token, "192.0.2.5",
		web("wiki", enabled, 8080),
		web("plain", nil, 8081),
		web("custom", map[string]string{"proxy.enable": "true", "proxy.host": "docs"}, 8082),
	)
	if code != http.StatusNoContent {
		t.Fatalf("report: status %d", code)
	}

	routes := e.routes()
	if r, ok := routes["wiki.home.example.com|active"]; !ok || r.Source != "label" || r.Servers[0] != "http://192.0.2.5:8080" {
		t.Errorf("label route: %+v (all: %v)", r, routes)
	}
	if _, ok := routes["docs.home.example.com|active"]; !ok {
		t.Errorf("proxy.host route missing: %v", routes)
	}

	var workloads []workloadJSON
	e.call("GET", "/api/containers", nil, &workloads)
	var plain workloadJSON
	for _, w := range workloads {
		if w.Workload == "plain" {
			plain = w
		}
	}
	if len(workloads) != 3 || plain.Routed || plain.LabelEnabled || plain.SuggestedHostname != "plain.home.example.com" ||
		len(plain.Ports) != 1 || !plain.Ports[0].Reachable {
		t.Errorf("unlabelled workload: %+v", plain)
	}

	// --- approve the unlabelled container in the UI ---
	approve := routeRequest{Source: "ui", Hostname: "plain", AgentID: agentID, Workload: "plain", ContainerPort: 80}
	if code := e.call("POST", "/api/routes", approve, nil); code != http.StatusCreated {
		t.Fatalf("approve: status %d", code)
	}
	// --- manual route to a device without Docker ---
	manual := routeRequest{Source: "manual", Hostname: "nas.home.example.com", TargetHost: "192.0.2.9", TargetPort: 5001, BackendHTTPS: true}
	var created struct {
		ID int64 `json:"id"`
	}
	if code := e.call("POST", "/api/routes", manual, &created); code != http.StatusCreated {
		t.Fatalf("manual route: status %d", code)
	}

	// --- hostname conflicts are rejected, never overwritten ---
	for _, host := range []string{"nas.home.example.com", "wiki", "proxy", "a.b.home.example.com", "x.other.org"} {
		clash := manual
		clash.Hostname = host
		code := e.call("POST", "/api/routes", clash, nil)
		if code != http.StatusConflict && code != http.StatusBadRequest {
			t.Errorf("route for %q: status %d, want 409 or 400", host, code)
		}
	}
	if r := e.routes()["wiki.home.example.com|active"]; r.Source != "label" {
		t.Errorf("label route was replaced: %+v", r)
	}

	// --- what Traefik gets ---
	cfg := e.traefikConfig()
	wantRouters := []string{"lanproxy-ui", "route-1", "route-2", "route-3", "route-4"}
	if len(cfg.HTTP.Routers) != len(wantRouters) {
		t.Errorf("traefik routers: %v", cfg.HTTP.Routers)
	}
	nas := cfg.HTTP.Services["route-4"].LoadBalancer
	if nas.Servers[0].URL != "https://192.0.2.9:5001" || nas.ServersTransport == "" {
		t.Errorf("manual route service: %+v", nas)
	}

	// --- the agent goes silent: its routes are switched off ---
	e.now = e.now.Add(6 * time.Minute)
	cfg = e.traefikConfig()
	if len(cfg.HTTP.Routers) != 2 { // ui + manual
		t.Errorf("routers after agent timeout: %v", cfg.HTTP.Routers)
	}
	if _, ok := e.routes()["wiki.home.example.com|agent_offline"]; !ok {
		t.Errorf("route status after timeout: %v", e.routes())
	}
	// ... and back on with the next heartbeat.
	e.report(token, "192.0.2.5", web("wiki", enabled, 8080))
	if _, ok := e.routes()["wiki.home.example.com|active"]; !ok {
		t.Errorf("route status after heartbeat: %v", e.routes())
	}
	// The approved container is no longer running.
	if _, ok := e.routes()["plain.home.example.com|container_missing"]; !ok {
		t.Errorf("approved route without container: %v", e.routes())
	}

	// --- editing and deleting ---
	manual.TargetPort = 5000
	if code := e.call("PUT", "/api/routes/4", manual, nil); code != http.StatusOK {
		t.Errorf("update manual route: status %d", code)
	}
	if code := e.call("DELETE", "/api/routes/1", nil, nil); code != http.StatusNotFound {
		t.Errorf("delete label route: status %d, want 404", code)
	}
	if code := e.call("DELETE", "/api/routes/4", nil, nil); code != http.StatusNoContent {
		t.Errorf("delete manual route: status %d", code)
	}
	if code := e.call("DELETE", "/api/agents/1", nil, nil); code != http.StatusNoContent {
		t.Errorf("delete agent: status %d", code)
	}
	if routes := e.routes(); len(routes) != 0 {
		t.Errorf("routes after deleting the agent: %v", routes)
	}
	if code := e.report(token, "192.0.2.5"); code != http.StatusUnauthorized {
		t.Errorf("report of deleted agent: status %d, want 401", code)
	}
}

func TestConflictBetweenAgents(t *testing.T) {
	e := newEnv(t)
	e.setup()
	_, tokenA := e.addAgent("a")
	_, tokenB := e.addAgent("b")

	e.report(tokenA, "192.0.2.5", web("wiki", enabled, 8080))
	e.report(tokenB, "192.0.2.6", web("wiki", enabled, 8080))

	routes := e.routes()
	if r := routes["wiki.home.example.com|active"]; r.AgentName != "a" {
		t.Errorf("owner of the hostname: %+v", r)
	}
	if r := routes["wiki.home.example.com|conflict"]; r.AgentName != "b" {
		t.Errorf("conflicting route: %+v", r)
	}
	cfg := e.traefikConfig()
	if len(cfg.HTTP.Routers) != 2 || cfg.HTTP.Services["route-1"].LoadBalancer.Servers[0].URL != "http://192.0.2.5:8080" {
		t.Errorf("traefik config: %+v", cfg.HTTP)
	}
}

func TestRenewAgentToken(t *testing.T) {
	e := newEnv(t)
	e.setup()
	_, oldToken := e.addAgent("nas")
	var renewed struct {
		Token string `json:"token"`
	}
	if code := e.call("POST", "/api/agents/1/token", nil, &renewed); code != http.StatusOK || renewed.Token == oldToken {
		t.Fatalf("renew: status %d, token changed: %v", code, renewed.Token != oldToken)
	}
	if code := e.report(oldToken, "192.0.2.5"); code != http.StatusUnauthorized {
		t.Errorf("old token: status %d, want 401", code)
	}
	if code := e.report(renewed.Token, "192.0.2.5"); code != http.StatusNoContent {
		t.Errorf("new token: status %d, want 204", code)
	}
}

func TestSettingsChangeRecomputesLabelRoutes(t *testing.T) {
	e := newEnv(t)
	e.setup()
	_, token := e.addAgent("nas")
	e.report(token, "192.0.2.5", web("wiki", enabled, 8080))

	change := setupBody()
	delete(change, "password")
	delete(change, "dns_env") // keep the stored credentials
	change["hostname_pattern"] = "{service}-{agent}.{domain}"
	if code := e.call("PUT", "/api/settings", change, nil); code != http.StatusOK {
		t.Fatalf("update settings: status %d", code)
	}
	if _, ok := e.routes()["wiki-nas.home.example.com|active"]; !ok {
		t.Errorf("routes after pattern change: %v", e.routes())
	}
	var settings settingsResponse
	e.call("GET", "/api/settings", nil, &settings)
	if len(settings.DNSEnvKeys) != 1 {
		t.Errorf("credentials were lost: %v", settings.DNSEnvKeys)
	}
}

func TestUIFallback(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/", "/agents", "/some/page"} {
		resp, err := http.Get(e.public.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "app") {
			t.Errorf("GET %s: status %d, body %q", path, resp.StatusCode, body)
		}
	}
	resp, err := http.Get(e.public.URL + "/_app/immutable/x.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	if code := e.call("GET", "/api/nope", nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown api path: status %d, want 404", code)
	}
}
