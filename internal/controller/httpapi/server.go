// Package httpapi contains the controller's HTTP handlers: the JSON API
// for the web interface, the report endpoint for agents and the
// configuration endpoint for Traefik.
package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"lanproxy/api"
	"lanproxy/internal/controller/certs"
	"lanproxy/internal/controller/config"
	"lanproxy/internal/controller/routing"
	"lanproxy/internal/controller/store"
	"lanproxy/internal/controller/traefik"
)

// Server bundles everything the handlers need.
type Server struct {
	Config  config.Config
	Store   *store.Store
	Traefik *traefik.Manager
	Paths   traefik.Paths
	// Logs may be nil (tests).
	Logs    *certs.LogWatcher
	Log     *slog.Logger
	Version string
	// UI is the compiled web interface.
	UI fs.FS
	// Now is the clock; tests replace it.
	Now func() time.Time

	logins loginLimiter
}

// Handler returns the handler for the public listener: web interface,
// its API and the agent endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Reachable without a session.
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/setup", s.handleSetupDefaults)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Agents authenticate with their token, not with a session.
	mux.HandleFunc("POST "+api.ReportPath, s.handleReport)

	// Everything else requires a logged-in admin.
	admin := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.requireSession(h))
	}
	admin("GET /api/settings", s.handleGetSettings)
	admin("PUT /api/settings", s.handlePutSettings)
	admin("POST /api/password", s.handleChangePassword)
	admin("GET /api/certificate", s.handleCertificate)
	admin("GET /api/routes", s.handleListRoutes)
	admin("POST /api/routes", s.handleCreateRoute)
	admin("PUT /api/routes/{id}", s.handleUpdateRoute)
	admin("DELETE /api/routes/{id}", s.handleDeleteRoute)
	admin("GET /api/containers", s.handleListContainers)
	admin("GET /api/agents", s.handleListAgents)
	admin("POST /api/agents", s.handleCreateAgent)
	admin("POST /api/agents/{id}/token", s.handleRenewAgentToken)
	admin("DELETE /api/agents/{id}", s.handleDeleteAgent)

	// Unknown API paths get a JSON 404 instead of the web interface.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "Unbekannter API-Pfad")
	})
	mux.Handle("/", s.uiHandler())

	return s.protect(mux)
}

// ProviderHandler returns the handler for the loopback-only listener
// from which Traefik polls its dynamic configuration.
func (s *Server) ProviderHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+config.ProviderPath, s.handleTraefikConfig)
	return mux
}

// handleTraefikConfig computes the dynamic configuration from the
// current state. Traefik asks every few seconds, so a route whose agent
// went silent disappears without any background job.
func (s *Server) handleTraefikConfig(w http.ResponseWriter, r *http.Request) {
	settings, views, err := s.resolveRoutes(r)
	if err != nil {
		// Answering with an error makes Traefik keep its last good
		// configuration, which is better than serving an empty one.
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, traefik.DynamicConfig(settings, views, s.Config.UIBackendURL()))
}

// resolveRoutes loads everything and computes the state of all routes.
func (s *Server) resolveRoutes(r *http.Request) (store.Settings, []routing.View, error) {
	ctx := r.Context()
	settings, err := s.Store.LoadSettings(ctx)
	if err != nil {
		return settings, nil, err
	}
	routes, err := s.Store.Routes(ctx)
	if err != nil {
		return settings, nil, err
	}
	agents, err := s.Store.Agents(ctx)
	if err != nil {
		return settings, nil, err
	}
	containers, err := s.Store.Containers(ctx)
	if err != nil {
		return settings, nil, err
	}
	return settings, routing.Resolve(routes, agents, containers, s.Now(), settings.AgentTimeout()), nil
}

// protect wraps all handlers of the public listener with the checks
// that apply to every request.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY") // no embedding in other pages
		h.Set("Referrer-Policy", "same-origin")

		// CSRF protection. A foreign web page open in the admin's
		// browser could try to send requests to us; the browser would
		// attach the session cookie. Browsers state where a request
		// comes from in the Origin header, so we reject changes that
		// originate from another site. (The cookie is SameSite=Strict
		// as a second line of defence.) Agents are not browsers and
		// send no Origin header.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
				writeError(w, http.StatusForbidden, "Anfrage von fremder Herkunft abgelehnt")
				return
			}
		}
		// No request needs a large body; this bounds memory use.
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// maxBodyBytes is generous enough for a report from a host with
// hundreds of containers.
const maxBodyBytes = 8 << 20

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, host)
}

// uiHandler serves the embedded single-page application.
func (s *Server) uiHandler() http.Handler {
	files := http.FileServerFS(s.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if info, err := fs.Stat(s.UI, path); err == nil && !info.IsDir() {
			if strings.HasPrefix(path, "_app/immutable/") {
				// These file names contain a hash of their content.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		// Any other path is a page of the application, which does its
		// own routing in the browser: always hand out index.html.
		index, err := fs.ReadFile(s.UI, "index.html")
		if err != nil {
			http.Error(w, "The web interface is not part of this build. Run \"make ui\" and rebuild.", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

// --- helpers shared by all handlers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends an error message meant for the admin.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// fail handles unexpected errors: details go to the log, the client
// only learns that something went wrong.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "Interner Fehler, Details stehen im Log")
}

// readJSON decodes the request body into v and answers 400 itself if
// that fails.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // catches typos in field names early
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Anfrage zu groß")
		} else {
			writeError(w, http.StatusBadRequest, "Ungültige Anfrage: "+err.Error())
		}
		return false
	}
	return true
}

// viaLocalProxy reports whether the request came through Traefik in our
// own container. Only then may the X-Forwarded-* headers be believed:
// a client connecting directly could put anything into them.
func viaLocalProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// clientIP returns the address of the client that sent the request.
func clientIP(r *http.Request) string {
	if viaLocalProxy(r) {
		// Traefik appends the address it saw as the last element.
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isHTTPS reports whether the client used HTTPS to reach us.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (viaLocalProxy(r) && r.Header.Get("X-Forwarded-Proto") == "https")
}
