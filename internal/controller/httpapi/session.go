package httpapi

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"lanproxy/internal/controller/auth"
)

const (
	sessionCookie = "lanproxy_session"
	sessionTTL    = 7 * 24 * time.Hour
)

// startSession creates a session and hands its token to the browser.
//
// The token is a random secret. The browser keeps it in a cookie, the
// database only stores its hash (like for agent tokens), so reading the
// database does not allow taking over a session.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) error {
	token, err := auth.NewSecret()
	if err != nil {
		return err
	}
	now := s.Now()
	// Take the opportunity to remove expired sessions.
	if err := s.Store.PurgeSessions(r.Context(), now); err != nil {
		return err
	}
	if err := s.Store.CreateSession(r.Context(), auth.HashSecret(token), now.Add(sessionTTL)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,                    // invisible to JavaScript
		SameSite: http.SameSiteStrictMode, // never sent from other sites
		// Over HTTPS the cookie is marked Secure so the browser never
		// sends it over plain HTTP. Via IP:port there is no HTTPS, and
		// a Secure cookie would make login impossible there.
		Secure: isHTTPS(r),
	})
	return nil
}

// authenticated reports whether the request carries a valid session.
func (s *Server) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	ok, err := s.Store.SessionValid(r.Context(), auth.HashSecret(cookie.Value), s.Now())
	return err == nil && ok
}

// requireSession lets only logged-in admins through.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "Nicht angemeldet")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	_, setupDone, err := s.Store.AdminPasswordHash(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_done":    setupDone,
		"authenticated": s.authenticated(r),
		"version":       s.Version,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := clientIP(r)
	if !s.logins.allow(ip, s.Now()) {
		writeError(w, http.StatusTooManyRequests, "Zu viele Fehlversuche, bitte eine Minute warten")
		return
	}
	hash, ok, err := s.Store.AdminPasswordHash(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok || !auth.CheckPassword(hash, req.Password) {
		s.logins.fail(ip, s.Now())
		// The password itself is of course not logged.
		s.Log.Warn("failed login", "client", ip)
		writeError(w, http.StatusUnauthorized, "Falsches Passwort")
		return
	}
	if err := s.startSession(w, r); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Store.DeleteSession(r.Context(), auth.HashSecret(cookie.Value)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	// MaxAge -1 tells the browser to delete the cookie.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	hash, _, err := s.Store.AdminPasswordHash(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Asking for the current password again protects an unattended,
	// logged-in browser.
	if !auth.CheckPassword(hash, req.Current) {
		writeError(w, http.StatusForbidden, "Das aktuelle Passwort ist falsch")
		return
	}
	newHash, err := auth.HashPassword(req.New)
	if err != nil {
		writeError(w, http.StatusBadRequest, passwordMessage(err))
		return
	}
	if err := s.Store.SetAdminPasswordHash(r.Context(), newHash); err != nil {
		s.fail(w, r, err)
		return
	}
	// End all sessions, including stolen ones, and start a fresh one
	// for this browser.
	if err := s.Store.DeleteAllSessions(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(w, r); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func passwordMessage(err error) string {
	if errors.Is(err, auth.ErrPasswordLength) {
		return "Das Passwort muss 8 bis 72 Zeichen lang sein"
	}
	return "Das Passwort konnte nicht gesetzt werden"
}

// loginLimiter slows down password guessing: after maxFailures wrong
// passwords within window, an address has to wait.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	maxFailures   = 5
	failureWindow = time.Minute
)

// allow reports whether ip may try a password now.
func (l *loginLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip, now)) < maxFailures
}

// fail records a wrong password.
func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = make(map[string][]time.Time)
	}
	l.failures[ip] = append(l.recent(ip, now), now)
}

// recent drops failures older than the window and returns the rest.
// The caller must hold the mutex.
func (l *loginLimiter) recent(ip string, now time.Time) []time.Time {
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if now.Sub(t) < failureWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip) // keeps the map from growing forever
		return nil
	}
	l.failures[ip] = kept
	return kept
}
