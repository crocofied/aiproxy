// Package api defines the wire types shared by the agent and the controller.
//
// Both binaries import this package, so a change here is a change to the
// protocol between them. Keep it free of logic and of dependencies.
package api

// ReportPath is the only endpoint the agent talks to. The agent sends a
// POST with a JSON-encoded Report and an "Authorization: Bearer <token>"
// header.
const ReportPath = "/api/agent/v1/report"

// Reasons why an agent sent a report. They are informational only: every
// report carries the full state, so the controller treats them all the same.
const (
	ReasonStartup   = "startup"
	ReasonEvent     = "event"
	ReasonHeartbeat = "heartbeat"
)

// Report is a full snapshot of one Docker host. The agent never sends
// deltas: a snapshot is idempotent, so a lost or repeated report does no
// harm and the controller simply replaces what it knew about this agent.
type Report struct {
	AgentVersion string `json:"agent_version"`
	// Hostname is the name of the Docker host, for display in the UI.
	Hostname string `json:"hostname"`
	// HostIP is the address under which the published ports of this host
	// are reachable from the proxy.
	HostIP     string      `json:"host_ip"`
	Reason     string      `json:"reason"`
	Containers []Container `json:"containers"`
}

// Container is a running container that publishes at least one port.
type Container struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	// ComposeProject and ComposeService are empty for containers that
	// were not started by Docker Compose.
	ComposeProject string            `json:"compose_project,omitempty"`
	ComposeService string            `json:"compose_service,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Ports          []Port            `json:"ports"`
}

// Port is one published port mapping of a container.
type Port struct {
	// HostIP is the address the port is bound to on the host, e.g.
	// "0.0.0.0" (all interfaces) or "127.0.0.1" (not reachable from
	// other machines).
	HostIP        string `json:"host_ip"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	// Protocol is "tcp" or "udp" (or "sctp"); only tcp can be proxied.
	Protocol string `json:"protocol"`
}
