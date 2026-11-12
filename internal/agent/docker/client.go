// Package docker is a deliberately tiny client for the Docker Engine API.
//
// The agent needs exactly two calls: list running containers and wait for
// container events. Both are plain HTTP GET requests over the Docker Unix
// socket, so we do not need the official SDK and its many dependencies.
//
// Security note: mounting the socket with ":ro" does NOT make the Docker
// API read-only; anyone who can connect to the socket can send any request.
// What keeps the agent harmless is that this package only ever issues the
// two GET requests below. Do not add writing calls here.
package docker

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"lanproxy/api"
)

const (
	// The host part is ignored because we dial the socket ourselves, but
	// net/http needs a syntactically valid URL.
	baseURL = "http://docker"

	labelComposeProject = "com.docker.compose.project"
	labelComposeService = "com.docker.compose.service"
)

// Client talks to a Docker daemon through its Unix socket.
type Client struct {
	http *http.Client
}

// New returns a client for the daemon listening on socketPath.
func New(socketPath string) *Client {
	transport := &http.Transport{
		// Every "TCP connection" net/http wants is really a connection to
		// the Unix socket, whatever address it asks for.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	// No client timeout: the event stream stays open indefinitely.
	// Callers bound each request with a context instead.
	return &Client{http: &http.Client{Transport: transport}}
}

// get sends a GET request. The path carries no API version prefix, so the
// daemon answers with its own current version; the few fields we read have
// been stable for many years.
func (c *Client) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	u := baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Include a short piece of the body: Docker explains errors there.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("docker: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// containerJSON and portJSON mirror the parts of the response of
// GET /containers/json that we care about.
type containerJSON struct {
	ID     string `json:"Id"`
	Names  []string
	Image  string
	Labels map[string]string
	Ports  []portJSON
}

type portJSON struct {
	IP          string
	PrivatePort uint16
	PublicPort  uint16 // 0 if the port is exposed but not published
	Type        string
}

// ListContainers returns all running containers that publish at least one
// port, sorted by name. The result is never nil.
func (c *Client) ListContainers(ctx context.Context) ([]api.Container, error) {
	// Without the "all" parameter Docker lists running containers only.
	resp, err := c.get(ctx, "/containers/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw []containerJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("docker: decode container list: %w", err)
	}

	containers := make([]api.Container, 0, len(raw))
	for _, rc := range raw {
		ports := publishedPorts(rc.Ports)
		if len(ports) == 0 {
			continue // nothing the proxy could connect to
		}
		containers = append(containers, api.Container{
			ID:             rc.ID,
			Name:           containerName(rc.Names),
			Image:          rc.Image,
			ComposeProject: rc.Labels[labelComposeProject],
			ComposeService: rc.Labels[labelComposeService],
			Labels:         rc.Labels,
			Ports:          ports,
		})
	}
	slices.SortFunc(containers, func(a, b api.Container) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return containers, nil
}

// containerName returns the primary name without Docker's leading slash.
func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// publishedPorts keeps only ports that are published on the host and
// removes duplicates. Docker reports "-p 8080:80" twice, once bound to
// 0.0.0.0 and once to "::"; for the proxy that is a single target.
func publishedPorts(raw []portJSON) []api.Port {
	type key struct {
		containerPort, hostPort uint16
		protocol                string
	}
	index := make(map[key]int) // key -> position in ports
	var ports []api.Port

	for _, rp := range raw {
		if rp.PublicPort == 0 {
			continue
		}
		p := api.Port{
			HostIP:        rp.IP,
			HostPort:      rp.PublicPort,
			ContainerPort: rp.PrivatePort,
			Protocol:      rp.Type,
		}
		k := key{p.ContainerPort, p.HostPort, p.Protocol}
		i, seen := index[k]
		if !seen {
			index[k] = len(ports)
			ports = append(ports, p)
			continue
		}
		// Of two bindings for the same port prefer the IPv4 one, because
		// the controller builds targets from the host's IPv4 address.
		if isIPv6(ports[i].HostIP) && !isIPv6(p.HostIP) {
			ports[i] = p
		}
	}
	slices.SortFunc(ports, func(a, b api.Port) int {
		return cmp.Or(
			cmp.Compare(a.ContainerPort, b.ContainerPort),
			cmp.Compare(a.HostPort, b.HostPort),
			cmp.Compare(a.Protocol, b.Protocol),
		)
	})
	return ports
}

func isIPv6(ip string) bool { return strings.Contains(ip, ":") }

// eventFilter limits the event stream to the changes that can alter the
// result of ListContainers. In particular it leaves out the frequent
// health_status and exec events.
const eventFilter = `{"type":["container"],"event":["start","die","destroy","rename"]}`

// WatchEvents blocks and calls onEvent for every relevant container event.
// It returns when ctx is cancelled or the stream breaks (for example when
// the Docker daemon restarts); the caller decides whether to reconnect.
func (c *Client) WatchEvents(ctx context.Context, onEvent func()) error {
	resp, err := c.get(ctx, "/events", url.Values{"filters": {eventFilter}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// The body is an endless sequence of JSON objects. We do not need
	// their content: any event simply means "look again".
	dec := json.NewDecoder(resp.Body)
	for {
		var event struct{}
		if err := dec.Decode(&event); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("docker: read event stream: %w", err)
		}
		onEvent()
	}
}
