package routing

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"lanproxy/api"
	"lanproxy/internal/controller/store"
)

func testSettings() store.Settings {
	s := store.DefaultSettings()
	s.BaseDomain = "home.example.com"
	s.UISubdomain = "proxy"
	return s
}

func TestCheckHostname(t *testing.T) {
	s := testSettings()
	tests := []struct {
		host    string
		wantErr string // "" = valid
	}{
		{"wiki.home.example.com", ""},
		{"home.example.com", ""}, // the base domain itself is on the certificate
		{"a-1.home.example.com", ""},
		{"proxy.home.example.com", "reserviert"},
		{"wiki.example.com", "nicht unter"},
		{"wiki.other.org", "nicht unter"},
		{"a.b.home.example.com", "Ebene zu viel"},
		{"evilhome.example.com", "nicht unter"}, // suffix without the dot
		{"Wiki.home.example.com", "kein gültiger"},
		{"wi_ki.home.example.com", "kein gültiger"},
		{"-wiki.home.example.com", "kein gültiger"},
		{"wiki`.home.example.com", "kein gültiger"},
		{"", "kein gültiger"},
	}
	for _, tt := range tests {
		err := CheckHostname(tt.host, s)
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("CheckHostname(%q) = %v, want valid", tt.host, err)
		case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
			t.Errorf("CheckHostname(%q) = %v, want error containing %q", tt.host, err, tt.wantErr)
		}
	}
}

func TestExpandPattern(t *testing.T) {
	v := PatternVars{Service: "My_App.v2", Project: "shop", Container: "/web-1", Agent: "NAS", Domain: "example.com"}
	tests := map[string]string{
		"{service}.{domain}":         "my-app-v2.example.com",
		"{service}-{agent}.{domain}": "my-app-v2-nas.example.com",
		"{container}.{domain}":       "web-1.example.com",
	}
	for pattern, want := range tests {
		if got := ExpandPattern(pattern, v); got != want {
			t.Errorf("ExpandPattern(%q) = %q, want %q", pattern, got, want)
		}
	}
}

func TestCheckPattern(t *testing.T) {
	for _, ok := range []string{"{service}.{domain}", "{service}-{agent}.{domain}", "{project}-{container}.{domain}"} {
		if err := CheckPattern(ok); err != nil {
			t.Errorf("CheckPattern(%q) = %v, want valid", ok, err)
		}
	}
	for _, bad := range []string{"", "{agent}.{domain}", "{service}", "{service}.{agent}.{domain}", "{service}.example.com"} {
		if err := CheckPattern(bad); err == nil {
			t.Errorf("CheckPattern(%q) = nil, want an error", bad)
		}
	}
}

func container(name, project, service string, labels map[string]string, ports ...api.Port) api.Container {
	return api.Container{ID: "id-" + name, Name: name, ComposeProject: project, ComposeService: service, Labels: labels, Ports: ports}
}

func TestDesiredLabelRoutes(t *testing.T) {
	enable := func(extra ...string) map[string]string {
		m := map[string]string{LabelEnable: "true"}
		for i := 0; i < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	containers := []api.Container{
		container("shop-web-1", "shop", "web", enable()),
		container("shop-web-2", "shop", "web", enable()), // replica: no second route
		container("grafana", "", "", enable(LabelHost, "stats", LabelPort, "3000")),
		container("portainer", "", "", enable(LabelHost, "Docker.Home.Example.com.", LabelScheme, "https")),
		container("deep", "", "", enable(LabelHost, "a.b.home.example.com")),
		container("badport", "", "", enable(LabelPort, "http")),
		container("badscheme", "", "", enable(LabelScheme, "ftp")),
		container("ui-clash", "", "", enable(LabelHost, "proxy")),
		container("unlabelled", "", "", nil),
		container("disabled", "", "", map[string]string{LabelEnable: "false"}),
	}

	got := DesiredLabelRoutes("nas", containers, testSettings())

	type summary struct {
		hostname string
		port     int
		https    bool
		invalid  bool
	}
	want := map[string]summary{
		"shop/web":  {"web.home.example.com", 0, false, false},
		"grafana":   {"stats.home.example.com", 3000, false, false},
		"portainer": {"docker.home.example.com", 0, true, false},
		"deep":      {"a.b.home.example.com", 0, false, true},
		"badport":   {"badport.home.example.com", 0, false, true},
		"badscheme": {"badscheme.home.example.com", 0, false, true},
		"ui-clash":  {"proxy.home.example.com", 0, false, true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d routes, want %d: %+v", len(got), len(want), got)
	}
	for _, r := range got {
		w, ok := want[r.Workload]
		if !ok {
			t.Errorf("unexpected route for workload %q", r.Workload)
			continue
		}
		if r.Hostname != w.hostname || r.ContainerPort != w.port || r.BackendHTTPS != w.https || (r.Invalid != "") != w.invalid {
			t.Errorf("workload %q: got %+v, want %+v", r.Workload, r, w)
		}
	}

	if routes := DesiredLabelRoutes("nas", containers, store.DefaultSettings()); routes != nil {
		t.Errorf("without a base domain got %d routes, want none", len(routes))
	}
}

func TestPickTarget(t *testing.T) {
	tcp := func(bind string, host, ctr uint16) api.Port {
		return api.Port{HostIP: bind, HostPort: host, ContainerPort: ctr, Protocol: "tcp"}
	}
	udp := api.Port{HostIP: "0.0.0.0", HostPort: 53, ContainerPort: 53, Protocol: "udp"}

	tests := []struct {
		name          string
		ports         []api.Port
		containerPort int
		wantHost      string
		wantPort      int
		wantStatus    Status
	}{
		{"single port", []api.Port{tcp("0.0.0.0", 8080, 80)}, 0, "192.0.2.5", 8080, StatusActive},
		{"udp is ignored", []api.Port{udp, tcp("0.0.0.0", 8080, 80)}, 0, "192.0.2.5", 8080, StatusActive},
		{"only udp", []api.Port{udp}, 0, "", 0, StatusPortUnpublished},
		{"several ports, none chosen", []api.Port{tcp("0.0.0.0", 8080, 80), tcp("0.0.0.0", 8443, 443)}, 0, "", 0, StatusPortAmbiguous},
		{"several ports, one chosen", []api.Port{tcp("0.0.0.0", 8080, 80), tcp("0.0.0.0", 8443, 443)}, 443, "192.0.2.5", 8443, StatusActive},
		{"chosen port not published", []api.Port{tcp("0.0.0.0", 8080, 80)}, 3000, "", 0, StatusPortUnpublished},
		{"loopback only", []api.Port{tcp("127.0.0.1", 8080, 80)}, 0, "", 0, StatusPortLocalOnly},
		{"loopback and public", []api.Port{tcp("127.0.0.1", 8080, 80), tcp("0.0.0.0", 9090, 80)}, 80, "192.0.2.5", 9090, StatusActive},
		{"specific interface", []api.Port{tcp("192.0.2.77", 8080, 80)}, 0, "192.0.2.77", 8080, StatusActive},
		{"ipv6 wildcard", []api.Port{tcp("::", 8080, 80)}, 0, "192.0.2.5", 8080, StatusActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, status := PickTarget(tt.ports, tt.containerPort, "192.0.2.5")
			if host != tt.wantHost || port != tt.wantPort || status != tt.wantStatus {
				t.Errorf("got %q, %d, %s; want %q, %d, %s", host, port, status, tt.wantHost, tt.wantPort, tt.wantStatus)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	timeout := 5 * time.Minute
	agents := []store.Agent{
		{ID: 1, Name: "online", HostIP: "192.0.2.5", LastSeenAt: now.Add(-time.Minute)},
		{ID: 2, Name: "silent", HostIP: "192.0.2.6", LastSeenAt: now.Add(-10 * time.Minute)},
	}
	port := api.Port{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}
	port2 := api.Port{HostIP: "0.0.0.0", HostPort: 8081, ContainerPort: 80, Protocol: "tcp"}
	containers := []store.Container{
		{AgentID: 1, Workload: "shop/web", Container: api.Container{ID: "a", Ports: []api.Port{port}}},
		{AgentID: 1, Workload: "shop/web", Container: api.Container{ID: "b", Ports: []api.Port{port2}}},
		{AgentID: 2, Workload: "blog", Container: api.Container{ID: "c", Ports: []api.Port{port}}},
	}
	routes := []store.Route{
		{ID: 1, Source: store.SourceLabel, AgentID: 1, Workload: "shop/web", State: store.StateOK},
		{ID: 2, Source: store.SourceUI, AgentID: 2, Workload: "blog", State: store.StateOK},
		{ID: 3, Source: store.SourceUI, AgentID: 1, Workload: "gone", State: store.StateOK},
		{ID: 4, Source: store.SourceManual, TargetHost: "192.0.2.9", TargetPort: 8006, BackendHTTPS: true, State: store.StateOK},
		{ID: 5, Source: store.SourceManual, TargetHost: "fd00::1", TargetPort: 80, State: store.StateOK},
		{ID: 6, Source: store.SourceLabel, AgentID: 1, Workload: "shop/web", State: store.StateConflict},
		{ID: 7, Source: store.SourceLabel, AgentID: 1, Workload: "shop/web", State: store.StateInvalid},
	}

	views := Resolve(routes, agents, containers, now, timeout)

	want := []struct {
		status  Status
		servers []string
	}{
		{StatusActive, []string{"http://192.0.2.5:8080", "http://192.0.2.5:8081"}}, // both replicas
		{StatusAgentOffline, nil},
		{StatusContainerMissing, nil},
		{StatusActive, []string{"https://192.0.2.9:8006"}},
		{StatusActive, []string{"http://[fd00::1]:80"}},
		{StatusConflict, nil},
		{StatusInvalid, nil},
	}
	for i, w := range want {
		if views[i].Status != w.status || !reflect.DeepEqual(views[i].Servers, w.servers) {
			t.Errorf("route %d: got %s %v, want %s %v", routes[i].ID, views[i].Status, views[i].Servers, w.status, w.servers)
		}
	}
	if views[0].AgentName != "online" {
		t.Errorf("AgentName = %q, want %q", views[0].AgentName, "online")
	}
}
