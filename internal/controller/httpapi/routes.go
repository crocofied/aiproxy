package httpapi

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"lanproxy/internal/controller/routing"
	"lanproxy/internal/controller/store"
)

// routeJSON is a route as shown in the overview.
type routeJSON struct {
	ID            int64    `json:"id"`
	Source        string   `json:"source"`
	Hostname      string   `json:"hostname"`
	AgentID       int64    `json:"agent_id,omitempty"`
	AgentName     string   `json:"agent_name,omitempty"`
	Workload      string   `json:"workload,omitempty"`
	ContainerPort int      `json:"container_port,omitempty"`
	TargetHost    string   `json:"target_host,omitempty"`
	TargetPort    int      `json:"target_port,omitempty"`
	BackendHTTPS  bool     `json:"backend_https"`
	Status        string   `json:"status"`
	Detail        string   `json:"detail,omitempty"`
	Servers       []string `json:"servers"`
}

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	_, views, err := s.resolveRoutes(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]routeJSON, 0, len(views))
	for _, v := range views {
		servers := v.Servers
		if servers == nil {
			servers = []string{}
		}
		out = append(out, routeJSON{
			ID: v.ID, Source: v.Source, Hostname: v.Hostname,
			AgentID: v.AgentID, AgentName: v.AgentName, Workload: v.Workload, ContainerPort: v.ContainerPort,
			TargetHost: v.TargetHost, TargetPort: v.TargetPort, BackendHTTPS: v.BackendHTTPS,
			Status: string(v.Status), Detail: v.Detail, Servers: servers,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// routeRequest creates or changes a route. Source decides which fields
// matter: "manual" uses the target, "ui" the agent and workload.
type routeRequest struct {
	Source        string `json:"source"`
	Hostname      string `json:"hostname"`
	AgentID       int64  `json:"agent_id"`
	Workload      string `json:"workload"`
	ContainerPort int    `json:"container_port"`
	TargetHost    string `json:"target_host"`
	TargetPort    int    `json:"target_port"`
	BackendHTTPS  bool   `json:"backend_https"`
}

// toRoute validates a request. The returned error is a message for the
// admin.
func (s *Server) toRoute(r *http.Request, req routeRequest) (store.Route, error) {
	settings, err := s.Store.LoadSettings(r.Context())
	if err != nil {
		return store.Route{}, err
	}
	host := routing.NormalizeHostname(req.Hostname)
	// "wiki" is shorthand for "wiki.<base domain>".
	if host != "" && !strings.Contains(host, ".") {
		host += "." + settings.BaseDomain
	}
	if err := routing.CheckHostname(host, settings); err != nil {
		return store.Route{}, userError{err.Error()}
	}
	route := store.Route{
		Source: req.Source, Hostname: host, BackendHTTPS: req.BackendHTTPS, CreatedAt: s.Now(),
	}

	switch req.Source {
	case store.SourceManual:
		target := strings.TrimSpace(req.TargetHost)
		if _, err := netip.ParseAddr(target); err != nil && !validTargetName(target) {
			return route, userError{"Das Ziel muss eine IP-Adresse oder ein Hostname sein"}
		}
		if req.TargetPort < 1 || req.TargetPort > 65535 {
			return route, userError{"Der Ziel-Port muss zwischen 1 und 65535 liegen"}
		}
		route.TargetHost, route.TargetPort = target, req.TargetPort

	case store.SourceUI:
		if req.Workload == "" || req.AgentID <= 0 {
			return route, userError{"Agent und Container fehlen"}
		}
		if req.ContainerPort < 0 || req.ContainerPort > 65535 {
			return route, userError{"Der Container-Port ist ungültig"}
		}
		route.AgentID, route.Workload, route.ContainerPort = req.AgentID, req.Workload, req.ContainerPort

	default:
		return route, userError{"Unbekannte Art von Route"}
	}
	return route, nil
}

// validTargetName accepts host names like "nas" or "nas.lan".
func validTargetName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.ToLower(name), ".") {
		if !routing.ValidLabel(label) {
			return false
		}
	}
	return true
}

// userError is an error whose message may be shown to the admin, in
// contrast to internal errors, which only go to the log.
type userError struct{ message string }

func (e userError) Error() string { return e.message }

// routeError translates errors from validation and storage into HTTP
// responses.
func (s *Server) routeError(w http.ResponseWriter, r *http.Request, err error) {
	var ue userError
	switch {
	case errors.As(err, &ue):
		writeError(w, http.StatusBadRequest, ue.message)
	case errors.Is(err, store.ErrHostnameTaken):
		// Existing routes are never overwritten.
		writeError(w, http.StatusConflict, "Dieser Hostname wird bereits von einer anderen Route verwendet")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Route nicht gefunden (Label-Routen lassen sich nur über die Labels ändern)")
	default:
		s.fail(w, r, err)
	}
}

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	var req routeRequest
	if !readJSON(w, r, &req) {
		return
	}
	route, err := s.toRoute(r, req)
	if err == nil {
		route.ID, err = s.Store.CreateRoute(r.Context(), route)
	}
	if err != nil {
		s.routeError(w, r, err)
		return
	}
	s.Log.Info("route created", "hostname", route.Hostname, "source", route.Source)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": route.ID})
}

func (s *Server) handleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req routeRequest
	if !readJSON(w, r, &req) {
		return
	}
	route, err := s.toRoute(r, req)
	if err == nil {
		route.ID = id
		err = s.Store.UpdateRoute(r.Context(), route)
	}
	if err != nil {
		s.routeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteRoute(r.Context(), id); err != nil {
		s.routeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pathID reads the {id} part of the URL.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "Ungültige ID")
		return 0, false
	}
	return id, true
}
