package routing

import (
	"net"
	"net/netip"
	"strconv"
	"time"

	"lanproxy/api"
	"lanproxy/internal/controller/store"
)

// Status says whether a route is being served and, if not, why.
type Status string

// Possible values of Status.
const (
	StatusActive           Status = "active"
	StatusConflict         Status = "conflict"          // hostname owned by another route
	StatusInvalid          Status = "invalid"           // unusable labels
	StatusAgentOffline     Status = "agent_offline"     // agent missed its heartbeats
	StatusContainerMissing Status = "container_missing" // workload is not running
	StatusPortAmbiguous    Status = "port_ambiguous"    // several ports, none chosen
	StatusPortUnpublished  Status = "port_unpublished"  // chosen port is not published
	StatusPortLocalOnly    Status = "port_local_only"   // published on 127.0.0.1 only
)

// View is a route together with its current, computed state.
type View struct {
	store.Route
	AgentName string
	Status    Status
	// Servers are the backend URLs Traefik should use. Empty unless the
	// status is active.
	Servers []string
}

type workloadKey struct {
	agentID  int64
	workload string
}

// Resolve computes the current state of all routes from the latest
// agent and container data.
func Resolve(routes []store.Route, agents []store.Agent, containers []store.Container, now time.Time, timeout time.Duration) []View {
	agentByID := make(map[int64]store.Agent, len(agents))
	for _, a := range agents {
		agentByID[a.ID] = a
	}
	byWorkload := make(map[workloadKey][]store.Container)
	for _, c := range containers {
		k := workloadKey{c.AgentID, c.Workload}
		byWorkload[k] = append(byWorkload[k], c)
	}

	views := make([]View, 0, len(routes))
	for _, r := range routes {
		v := View{Route: r}
		agent, hasAgent := agentByID[r.AgentID]
		v.AgentName = agent.Name

		switch {
		case r.State == store.StateConflict:
			v.Status = StatusConflict
		case r.State == store.StateInvalid:
			v.Status = StatusInvalid
		case r.Source == store.SourceManual:
			v.Status = StatusActive
			v.Servers = []string{serverURL(r.BackendHTTPS, r.TargetHost, r.TargetPort)}
		case !hasAgent || !agent.Online(now, timeout) || agent.HostIP == "":
			v.Status = StatusAgentOffline
		default:
			v.Status, v.Servers = resolveWorkload(r, agent, byWorkload[workloadKey{r.AgentID, r.Workload}])
		}
		views = append(views, v)
	}
	return views
}

// resolveWorkload finds the host address and port for every running
// container of a workload.
func resolveWorkload(r store.Route, agent store.Agent, containers []store.Container) (Status, []string) {
	if len(containers) == 0 {
		return StatusContainerMissing, nil
	}
	var servers []string
	firstProblem := StatusActive
	for _, c := range containers {
		host, port, status := PickTarget(c.Ports, r.ContainerPort, agent.HostIP)
		if status != StatusActive {
			if firstProblem == StatusActive {
				firstProblem = status
			}
			continue
		}
		servers = append(servers, serverURL(r.BackendHTTPS, host, port))
	}
	if len(servers) == 0 {
		return firstProblem, nil
	}
	// One working replica is enough for the route to be up.
	return StatusActive, servers
}

// PickTarget chooses the published port of a container that the proxy
// should connect to. containerPort 0 means "the only published TCP
// port". It returns the address on the host, or the reason why there is
// none.
func PickTarget(ports []api.Port, containerPort int, agentIP string) (host string, port int, status Status) {
	if containerPort == 0 {
		distinct := make(map[uint16]bool)
		for _, p := range ports {
			if p.Protocol == "tcp" {
				distinct[p.ContainerPort] = true
				containerPort = int(p.ContainerPort)
			}
		}
		switch {
		case len(distinct) == 0:
			return "", 0, StatusPortUnpublished
		case len(distinct) > 1:
			return "", 0, StatusPortAmbiguous
		}
	}

	status = StatusPortUnpublished
	for _, p := range ports {
		if p.Protocol != "tcp" || int(p.ContainerPort) != containerPort {
			continue
		}
		bind, err := netip.ParseAddr(p.HostIP)
		switch {
		case err != nil || bind.IsUnspecified():
			// Bound to all interfaces: reachable under the host's address.
			return agentIP, int(p.HostPort), StatusActive
		case bind.IsLoopback():
			// "127.0.0.1:8080:80" is only reachable on that host itself.
			status = StatusPortLocalOnly
		default:
			// Bound to one specific interface address.
			return bind.String(), int(p.HostPort), StatusActive
		}
	}
	return "", 0, status
}

func serverURL(https bool, host string, port int) string {
	scheme := "http"
	if https {
		scheme = "https"
	}
	// JoinHostPort adds the brackets an IPv6 address needs.
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}
