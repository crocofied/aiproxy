package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lanproxy/api"
)

// fakeDaemon serves handler on a Unix socket, like dockerd does, and
// returns a client connected to it.
func fakeDaemon(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	// Unix socket paths are limited to about 100 bytes, which t.TempDir
	// can exceed, so create a short directory ourselves.
	dir, err := os.MkdirTemp("", "dock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)
	return New(socket)
}

const containerListJSON = `[
  {
    "Id": "bbb", "Names": ["/web"], "Image": "nginx:1",
    "Labels": {
      "com.docker.compose.project": "shop",
      "com.docker.compose.service": "frontend",
      "proxy.enable": "true"
    },
    "Ports": [
      {"IP": "::", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
      {"IP": "0.0.0.0", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
      {"PrivatePort": 443, "Type": "tcp"},
      {"IP": "127.0.0.1", "PrivatePort": 9000, "PublicPort": 9000, "Type": "tcp"}
    ]
  },
  {
    "Id": "ccc", "Names": ["/internal-only"], "Image": "redis:7", "Labels": {},
    "Ports": [{"PrivatePort": 6379, "Type": "tcp"}]
  },
  {
    "Id": "aaa", "Names": ["/dns"], "Image": "adguard", "Labels": null,
    "Ports": [{"IP": "0.0.0.0", "PrivatePort": 53, "PublicPort": 53, "Type": "udp"}]
  }
]`

func TestListContainers(t *testing.T) {
	client := fakeDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, containerListJSON)
	}))

	got, err := client.ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := []api.Container{
		{
			ID: "aaa", Name: "dns", Image: "adguard",
			Ports: []api.Port{{HostIP: "0.0.0.0", HostPort: 53, ContainerPort: 53, Protocol: "udp"}},
		},
		{
			ID: "bbb", Name: "web", Image: "nginx:1",
			ComposeProject: "shop", ComposeService: "frontend",
			Labels: map[string]string{
				"com.docker.compose.project": "shop",
				"com.docker.compose.service": "frontend",
				"proxy.enable":               "true",
			},
			Ports: []api.Port{
				// The IPv6 duplicate and the unpublished 443 are gone.
				{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
				{HostIP: "127.0.0.1", HostPort: 9000, ContainerPort: 9000, Protocol: "tcp"},
			},
		},
		// "internal-only" publishes nothing and is not reported.
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListContainers() =\n%+v\nwant\n%+v", got, want)
	}
}

func TestListContainersEmptyIsNotNil(t *testing.T) {
	client := fakeDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	got, err := client.ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A nil slice would be encoded as JSON null instead of [].
	if got == nil {
		t.Error("ListContainers() returned nil, want empty slice")
	}
}

func TestListContainersDaemonError(t *testing.T) {
	client := fakeDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"permission denied"}`, http.StatusForbidden)
	}))
	_, err := client.ListContainers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %v, want one mentioning the daemon's message", err)
	}
}

func TestWatchEvents(t *testing.T) {
	client := fakeDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("filters"); got != eventFilter {
			t.Errorf("filters = %q, want %q", got, eventFilter)
		}
		// Two events, then keep the stream open until the client leaves.
		fmt.Fprintln(w, `{"Type":"container","Action":"start"}`)
		fmt.Fprintln(w, `{"Type":"container","Action":"die"}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan struct{}, 10)
	done := make(chan error, 1)
	go func() {
		done <- client.WatchEvents(ctx, func() { events <- struct{}{} })
	}()

	for i := range 2 {
		select {
		case <-events:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for event %d", i+1)
		}
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("WatchEvents() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchEvents did not return after cancel")
	}
}

func TestWatchEventsStreamEnds(t *testing.T) {
	client := fakeDaemon(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// Returning closes the stream, as a daemon restart would.
	}))
	err := client.WatchEvents(context.Background(), func() {})
	if err == nil {
		t.Error("WatchEvents() = nil, want an error when the stream ends")
	}
}
