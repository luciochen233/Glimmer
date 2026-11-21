package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"

	"glimmer/internal/db"
	"glimmer/internal/httpguard"
)

// The generic hardening (client IP, rate limiters, cross-origin guard, body and
// concurrency caps, password gate) lives in internal/httpguard, which is shared
// verbatim with Solis. These aliases keep the server package's own names.
type (
	rateLimiter       = httpguard.RateLimiter
	globalRateLimiter = httpguard.GlobalRateLimiter
)

var (
	newRateLimiter       = httpguard.NewRateLimiter
	newGlobalRateLimiter = httpguard.NewGlobalRateLimiter
	clientIP             = httpguard.ClientIP
	crossOriginGuard     = httpguard.CrossOriginGuard
	sessionKey           = httpguard.SessionKey
)

// sessionStore persists sessions in the database so logins survive a server
// restart. It is a thin wrapper around the DB with a periodic cleanup of
// expired rows.
type sessionStore struct {
	db  *db.DB
	ttl time.Duration
}

func newSessionStore(database *db.DB, ttl time.Duration) *sessionStore {
	s := &sessionStore{db: database, ttl: ttl}
	database.CleanupSessions() // purge anything already expired at startup
	go s.cleanup()
	return s
}

func (s *sessionStore) cleanup() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.db.CleanupSessions()
	}
}

func (s *sessionStore) Create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := s.db.CreateSession(sessionKey(token), time.Now().Add(s.ttl)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *sessionStore) Valid(token string) bool {
	ok, _ := s.db.SessionValid(sessionKey(token))
	return ok
}

func (s *sessionStore) Delete(token string) {
	s.db.DeleteSession(sessionKey(token))
}

func (srv *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil || !srv.sessions.Valid(cookie.Value) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// csrfToken returns the CSRF token for this session, creating one if needed.
// Uses a separate cookie (not HttpOnly so JS can read it, but SameSite=Lax
// already blocks cross-site POSTs for most cases). The token is also embedded
// in each form and verified server-side on every state-changing POST.
func (srv *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("csrf"); err == nil && len(c.Value) == 64 {
		return c.Value
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf",
		Value:    token,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		Secure:   srv.isHTTPS(r),
		MaxAge:   86400,
	})
	return token
}

func verifyCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("csrf")
	// Require a full-length token: an empty cookie must never "match" an
	// empty/missing form field.
	if err != nil || len(cookie.Value) != 64 {
		return false
	}
	formToken := r.FormValue("csrf_token")
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formToken)) == 1
}

func (srv *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyCSRF(r) {
			http.Error(w, "Invalid CSRF token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// clientIP returns the client IP for rate limiting and the request log, using
// the server's proxy settings.
func (srv *Server) clientIP(r *http.Request) string {
	hops := 0
	if srv.cfg.Server.TrustProxy {
		hops = srv.cfg.Server.ProxyHops
		if hops < 1 {
			hops = 1
		}
	}
	return clientIP(r, hops)
}

// checkPassword runs a bcrypt comparison through the shared concurrency gate.
// available is false when the gate is saturated; the caller should return 503
// rather than queueing more work.
func (srv *Server) checkPassword(hash, password string) (ok bool, available bool) {
	return srv.pwGate.Check(hash, password)
}
