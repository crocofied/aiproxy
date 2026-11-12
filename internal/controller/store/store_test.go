package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lanproxy/api"
)

var (
	ctx = context.Background()
	t0  = time.Unix(1_700_000_000, 0)
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustAgent(t *testing.T, s *Store, name string) Agent {
	t.Helper()
	a, err := s.CreateAgent(ctx, name, "hash-"+name, t0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// routeStates returns hostname+workload -> state for all routes.
func routeStates(t *testing.T, s *Store) map[string]string {
	t.Helper()
	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]string)
	for _, r := range routes {
		states[r.Hostname+" "+r.Workload] = r.State
	}
	return states
}

func TestOpenIsPrivateAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, Settings{BaseDomain: "example.com"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("database mode = %o, want 600", perm)
	}

	// Opening again must not re-run the migration.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	got, err := s.LoadSettings(ctx)
	if err != nil || got.BaseDomain != "example.com" {
		t.Errorf("LoadSettings() = %+v, %v", got, err)
	}
}

func TestSettingsDefaults(t *testing.T) {
	s := openTest(t)
	got, err := s.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentTimeoutSeconds != 300 || len(got.DNSResolvers) != 2 || got.HostnamePattern == "" {
		t.Errorf("unexpected defaults: %+v", got)
	}
	if _, ok, _ := s.AdminPasswordHash(ctx); ok {
		t.Error("fresh database already has an admin")
	}
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	if err := s.CreateSession(ctx, "h1", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, "h1", t0); !ok {
		t.Error("fresh session is not valid")
	}
	if ok, _ := s.SessionValid(ctx, "h1", t0.Add(2*time.Hour)); ok {
		t.Error("expired session is valid")
	}
	if ok, _ := s.SessionValid(ctx, "other", t0); ok {
		t.Error("unknown session is valid")
	}
	_ = s.DeleteSession(ctx, "h1")
	if ok, _ := s.SessionValid(ctx, "h1", t0); ok {
		t.Error("deleted session is valid")
	}
}

func TestAgents(t *testing.T) {
	s := openTest(t)
	a := mustAgent(t, s, "nas")
	if _, err := s.CreateAgent(ctx, "nas", "x", t0); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name: error = %v, want ErrNameTaken", err)
	}
	if _, hash, err := s.AgentTokenHash(ctx, a.ID); err != nil || hash != "hash-nas" {
		t.Errorf("AgentTokenHash() = %q, %v", hash, err)
	}
	if _, _, err := s.AgentTokenHash(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent: error = %v, want ErrNotFound", err)
	}

	containers := []Container{{
		AgentID: a.ID, Workload: "shop/web",
		Container: api.Container{
			ID: "c1", Name: "shop-web-1", ComposeProject: "shop", ComposeService: "web",
			Labels: map[string]string{"proxy.enable": "true"},
			Ports:  []api.Port{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}},
		},
	}}
	report := AgentReport{Hostname: "nas", HostIP: "192.0.2.5", Version: "1"}
	if err := s.ApplyReport(ctx, a.ID, report, containers, nil, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	agents, _ := s.Agents(ctx)
	if len(agents) != 1 || agents[0].HostIP != "192.0.2.5" || !agents[0].LastSeenAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("agent after report: %+v", agents)
	}
	if !agents[0].Online(t0.Add(2*time.Minute), 5*time.Minute) || agents[0].Online(t0.Add(time.Hour), 5*time.Minute) {
		t.Error("Online() is wrong")
	}
	got, _ := s.Containers(ctx)
	if len(got) != 1 || got[0].Labels["proxy.enable"] != "true" || got[0].Ports[0].HostPort != 8080 {
		t.Errorf("containers after report: %+v", got)
	}

	// The next report replaces the snapshot.
	if err := s.ApplyReport(ctx, a.ID, report, nil, nil, t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Containers(ctx); len(got) != 0 {
		t.Errorf("containers after empty report: %+v", got)
	}
}

func TestManualRouteConflict(t *testing.T) {
	s := openTest(t)
	route := Route{Source: SourceManual, Hostname: "nas.example.com", TargetHost: "192.0.2.9", TargetPort: 5000, CreatedAt: t0}
	id, err := s.CreateRoute(ctx, route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRoute(ctx, route); !errors.Is(err, ErrHostnameTaken) {
		t.Errorf("second route: error = %v, want ErrHostnameTaken", err)
	}

	other := route
	other.Hostname = "other.example.com"
	otherID, _ := s.CreateRoute(ctx, other)
	other.ID, other.Hostname = otherID, "nas.example.com"
	if err := s.UpdateRoute(ctx, other); !errors.Is(err, ErrHostnameTaken) {
		t.Errorf("rename onto taken hostname: error = %v, want ErrHostnameTaken", err)
	}

	// Updating a route without changing its hostname is not a conflict.
	route.ID, route.TargetPort = id, 5001
	if err := s.UpdateRoute(ctx, route); err != nil {
		t.Errorf("update in place: %v", err)
	}
	if err := s.DeleteRoute(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoute(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: error = %v, want ErrNotFound", err)
	}
}

func TestLabelRoutesFirstComeFirstServed(t *testing.T) {
	s := openTest(t)
	a, b := mustAgent(t, s, "a"), mustAgent(t, s, "b")
	want := []LabelRoute{{Workload: "web", Hostname: "web.example.com"}}

	// Agent a claims the hostname, agent b comes second and is parked.
	if err := s.SyncLabelRoutes(ctx, a.ID, want, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncLabelRoutes(ctx, b.ID, want, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	routes, _ := s.Routes(ctx)
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(routes))
	}
	for _, r := range routes {
		wantState := StateOK
		if r.AgentID == b.ID {
			wantState = StateConflict
		}
		if r.State != wantState {
			t.Errorf("agent %d: state = %s, want %s", r.AgentID, r.State, wantState)
		}
	}

	// Repeated reports change nothing: a keeps the hostname.
	_ = s.SyncLabelRoutes(ctx, b.ID, want, t0.Add(2*time.Second))
	_ = s.SyncLabelRoutes(ctx, a.ID, want, t0.Add(3*time.Second))
	routes, _ = s.Routes(ctx)
	for _, r := range routes {
		if (r.AgentID == a.ID) != (r.State == StateOK) {
			t.Errorf("after repeated sync: agent %d has state %s", r.AgentID, r.State)
		}
	}

	// A manual route cannot take the hostname either.
	_, err := s.CreateRoute(ctx, Route{Source: SourceManual, Hostname: "web.example.com", TargetHost: "x", TargetPort: 1, CreatedAt: t0})
	if !errors.Is(err, ErrHostnameTaken) {
		t.Errorf("manual route on taken hostname: error = %v", err)
	}

	// When a's container disappears, b's parked route takes over.
	if err := s.SyncLabelRoutes(ctx, a.ID, nil, t0.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	routes, _ = s.Routes(ctx)
	if len(routes) != 1 || routes[0].AgentID != b.ID || routes[0].State != StateOK {
		t.Errorf("after a left: %+v", routes)
	}
}

func TestLabelRouteChanges(t *testing.T) {
	s := openTest(t)
	a := mustAgent(t, s, "a")

	sync := func(routes ...LabelRoute) {
		t.Helper()
		if err := s.SyncLabelRoutes(ctx, a.ID, routes, t0); err != nil {
			t.Fatal(err)
		}
	}

	sync(LabelRoute{Workload: "web", Hostname: "web.example.com"},
		LabelRoute{Workload: "bad", Hostname: "a.b.example.com", Invalid: "zu tief"})
	states := routeStates(t, s)
	if states["web.example.com web"] != StateOK || states["a.b.example.com bad"] != StateInvalid {
		t.Errorf("initial states: %v", states)
	}

	// Port change keeps the row; hostname change replaces it; fixing
	// the labels turns an invalid route into a working one.
	before, _ := s.Routes(ctx)
	sync(LabelRoute{Workload: "web", Hostname: "web.example.com", ContainerPort: 8080, BackendHTTPS: true},
		LabelRoute{Workload: "bad", Hostname: "good.example.com"})
	after, _ := s.Routes(ctx)
	states = routeStates(t, s)
	if len(after) != 2 || states["good.example.com bad"] != StateOK || states["web.example.com web"] != StateOK {
		t.Errorf("states after change: %v", states)
	}
	var webBefore, webAfter Route
	for _, r := range before {
		if r.Workload == "web" {
			webBefore = r
		}
	}
	for _, r := range after {
		if r.Workload == "web" {
			webAfter = r
		}
	}
	if webAfter.ID != webBefore.ID || webAfter.ContainerPort != 8080 || !webAfter.BackendHTTPS {
		t.Errorf("web route was not updated in place: before %+v, after %+v", webBefore, webAfter)
	}

	// Deleting the agent removes its routes and containers.
	if err := s.DeleteAgent(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if routes, _ := s.Routes(ctx); len(routes) != 0 {
		t.Errorf("routes after agent delete: %+v", routes)
	}
}

func TestTwoWorkloadsOfOneAgentWantTheSameHostname(t *testing.T) {
	s := openTest(t)
	a := mustAgent(t, s, "a")
	err := s.SyncLabelRoutes(ctx, a.ID, []LabelRoute{
		{Workload: "one", Hostname: "same.example.com"},
		{Workload: "two", Hostname: "same.example.com"},
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	states := routeStates(t, s)
	if states["same.example.com one"] != StateOK || states["same.example.com two"] != StateConflict {
		t.Errorf("states: %v", states)
	}
}
