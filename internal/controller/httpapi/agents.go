package httpapi

import (
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"lanproxy/api"
	"lanproxy/internal/controller/auth"
	"lanproxy/internal/controller/routing"
	"lanproxy/internal/controller/store"
)

// handleReport receives the snapshot of an agent.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	// --- authentication ---
	// The header looks like "Authorization: Bearer <agent id>.<secret>".
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	agentID, secret, err := auth.ParseAgentToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	agentName, wantHash, err := s.Store.AgentTokenHash(r.Context(), agentID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Same answer as for a wrong secret: do not reveal which agent
		// ids exist.
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	if !auth.SecretMatches(secret, wantHash) {
		// Log who tried, never what was sent.
		s.Log.Warn("agent sent a wrong token", "agent", agentName, "client", clientIP(r))
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	// --- the report itself ---
	var report api.Report
	if !readJSON(w, r, &report) {
		return
	}
	hostIP, err := netip.ParseAddr(report.HostIP)
	if err != nil || hostIP.IsUnspecified() {
		writeError(w, http.StatusBadRequest, "host_ip is not a usable IP address")
		return
	}

	settings, err := s.Store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	containers := make([]store.Container, 0, len(report.Containers))
	for _, c := range report.Containers {
		containers = append(containers, store.Container{AgentID: agentID, Workload: routing.Workload(c), Container: c})
	}
	desired := routing.DesiredLabelRoutes(agentName, report.Containers, settings)
	meta := store.AgentReport{
		Hostname:   report.Hostname,
		HostIP:     hostIP.Unmap().String(),
		ObservedIP: clientIP(r),
		Version:    report.AgentVersion,
	}
	if err := s.Store.ApplyReport(r.Context(), agentID, meta, containers, desired, s.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Debug("report received", "agent", agentName, "reason", report.Reason, "containers", len(containers))
	w.WriteHeader(http.StatusNoContent)
}

// apiContainers returns the containers of one agent in wire format.
func apiContainers(all []store.Container, agentID int64) []api.Container {
	var out []api.Container
	for _, c := range all {
		if c.AgentID == agentID {
			out = append(out, c.Container)
		}
	}
	return out
}

type agentJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Hostname   string     `json:"hostname"`
	HostIP     string     `json:"host_ip"`
	ObservedIP string     `json:"observed_ip"`
	Version    string     `json:"version"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Containers int        `json:"containers"`
}

func toAgentJSON(a store.Agent, online bool, containers int) agentJSON {
	out := agentJSON{
		ID: a.ID, Name: a.Name, Hostname: a.Hostname, HostIP: a.HostIP, ObservedIP: a.ObservedIP,
		Version: a.Version, Online: online, CreatedAt: a.CreatedAt, Containers: containers,
	}
	if !a.LastSeenAt.IsZero() {
		out.LastSeenAt = &a.LastSeenAt
	}
	return out
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.Store.LoadSettings(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	agents, err := s.Store.Agents(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	containers, err := s.Store.Containers(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	count := make(map[int64]int)
	for _, c := range containers {
		count[c.AgentID]++
	}
	out := make([]agentJSON, 0, len(agents))
	for _, a := range agents {
		out = append(out, toAgentJSON(a, a.Online(s.Now(), settings.AgentTimeout()), count[a.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

// agentCreated is the only response that ever contains an agent token.
type agentCreated struct {
	Agent agentJSON `json:"agent"`
	// Token is shown once. It cannot be retrieved later because only
	// its hash is stored.
	Token        string `json:"token"`
	Image        string `json:"image"`
	BuildContext string `json:"build_context"`
}

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,39}$`)

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if !agentNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "Der Name darf bis zu 40 Buchstaben, Ziffern, Leerzeichen und . _ - enthalten")
		return
	}
	secret, err := auth.NewSecret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	agent, err := s.Store.CreateAgent(r.Context(), name, auth.HashSecret(secret), s.Now())
	if errors.Is(err, store.ErrNameTaken) {
		writeError(w, http.StatusConflict, "Es gibt bereits einen Agent mit diesem Namen")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("agent created", "agent", agent.Name)
	writeJSON(w, http.StatusCreated, s.agentCreated(agent, secret))
}

// handleRenewAgentToken replaces the token of an agent, for example
// after the old one was lost. The old token stops working immediately.
func (s *Server) handleRenewAgentToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	secret, err := auth.NewSecret()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Store.SetAgentTokenHash(r.Context(), id, auth.HashSecret(secret)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Agent nicht gefunden")
			return
		}
		s.fail(w, r, err)
		return
	}
	agents, err := s.Store.Agents(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, a := range agents {
		if a.ID == id {
			s.Log.Info("agent token renewed", "agent", a.Name)
			writeJSON(w, http.StatusOK, s.agentCreated(a, secret))
			return
		}
	}
	writeError(w, http.StatusNotFound, "Agent nicht gefunden")
}

func (s *Server) agentCreated(agent store.Agent, secret string) agentCreated {
	return agentCreated{
		Agent:        toAgentJSON(agent, false, 0),
		Token:        auth.FormatAgentToken(agent.ID, secret),
		Image:        s.Config.AgentImage,
		BuildContext: s.Config.AgentBuildContext,
	}
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteAgent(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Agent nicht gefunden")
			return
		}
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// workloadJSON is a discovered workload: one or more containers of the
// same compose service on one agent.
type workloadJSON struct {
	AgentID      int64      `json:"agent_id"`
	AgentName    string     `json:"agent_name"`
	AgentOnline  bool       `json:"agent_online"`
	Workload     string     `json:"workload"`
	Containers   []string   `json:"containers"`
	Image        string     `json:"image"`
	LabelEnabled bool       `json:"label_enabled"`
	Ports        []portJSON `json:"ports"`
	// Routed is true if a route of any kind points at this workload.
	Routed bool `json:"routed"`
	// SuggestedHostname is what the hostname pattern produces.
	SuggestedHostname string `json:"suggested_hostname"`
}

type portJSON struct {
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	// Reachable is false for ports bound to 127.0.0.1 only.
	Reachable bool `json:"reachable"`
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.Store.LoadSettings(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	agents, err := s.Store.Agents(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	containers, err := s.Store.Containers(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	routes, err := s.Store.Routes(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	type key struct {
		agentID  int64
		workload string
	}
	routed := make(map[key]bool)
	for _, route := range routes {
		routed[key{route.AgentID, route.Workload}] = true
	}
	agentByID := make(map[int64]store.Agent)
	for _, a := range agents {
		agentByID[a.ID] = a
	}

	out := []workloadJSON{}
	index := make(map[key]int) // position in out
	for _, c := range containers {
		k := key{c.AgentID, c.Workload}
		if i, ok := index[k]; ok {
			out[i].Containers = append(out[i].Containers, c.Name) // another replica
			continue
		}
		agent := agentByID[c.AgentID]
		service := c.ComposeService
		if service == "" {
			service = c.Name
		}
		wl := workloadJSON{
			AgentID: agent.ID, AgentName: agent.Name,
			AgentOnline:  agent.Online(s.Now(), settings.AgentTimeout()),
			Workload:     c.Workload,
			Containers:   []string{c.Name},
			Image:        c.Image,
			LabelEnabled: strings.EqualFold(c.Labels[routing.LabelEnable], "true"),
			Ports:        []portJSON{},
			Routed:       routed[k],
		}
		if settings.BaseDomain != "" {
			wl.SuggestedHostname = routing.ExpandPattern(settings.HostnamePattern, routing.PatternVars{
				Service: service, Project: c.ComposeProject, Container: c.Name,
				Agent: agent.Name, Domain: settings.BaseDomain,
			})
		}
		for _, p := range c.Ports {
			if p.Protocol != "tcp" {
				continue // only TCP can be proxied
			}
			_, _, status := routing.PickTarget([]api.Port{p}, int(p.ContainerPort), agent.HostIP)
			wl.Ports = append(wl.Ports, portJSON{
				ContainerPort: p.ContainerPort, HostPort: p.HostPort,
				Reachable: status == routing.StatusActive,
			})
		}
		index[k] = len(out)
		out = append(out, wl)
	}
	writeJSON(w, http.StatusOK, out)
}
