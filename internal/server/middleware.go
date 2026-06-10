package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"glimmer/internal/db"
	"golang.org/x/crypto/bcrypt"
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
	if err := s.db.CreateSession(token, time.Now().Add(s.ttl)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *sessionStore) Valid(token string) bool {
	ok, _ := s.db.SessionValid(token)
	return ok
}

func (s *sessionStore) Delete(token string) {
	s.db.DeleteSession(token)
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
func csrfToken(w http.ResponseWriter, r *http.Request) string {
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
		MaxAge:   86400,
	})
	return token
}

func verifyCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("csrf")
	if err != nil {
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

// rateLimiter tracks the last request time per IP.
type rateLimiter struct {
	mu      sync.Mutex
	clients map[string]time.Time
	window  time.Duration
}

func newRateLimiter(window time.Duration) *rateLimiter {
	return &rateLimiter{
		clients: make(map[string]time.Time),
		window:  window,
	}
}

func (rl *rateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if last, ok := rl.clients[ip]; ok && now.Sub(last) < rl.window {
		return false
	}
	rl.clients[ip] = now
	// Lazy cleanup: purge stale entries when map grows large
	if len(rl.clients) > 10000 {
		for k, v := range rl.clients {
			if now.Sub(v) > rl.window*10 {
				delete(rl.clients, k)
			}
		}
	}
	return true
}

// clientIP returns the client IP for rate-limiting purposes. Forwarded headers
// are only honoured when trustProxy is true, because any client can set them —
// trusting them unconditionally lets an attacker rotate X-Forwarded-For to
// bypass per-IP limits.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// First IP in the chain is the original client
			if i := strings.Index(xff, ","); i > 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// checkPassword runs a bcrypt comparison while bounding the number of
// concurrent bcrypt operations. bcrypt is deliberately CPU-heavy, so an
// unbounded flood of auth attempts can exhaust a single-core machine (e.g. an
// RPi Zero). The semaphore caps concurrency; if the gate cannot be acquired
// quickly the request is treated as temporarily unavailable (available=false)
// and the caller should return 503 rather than queueing more work.
func (srv *Server) checkPassword(hash, password string) (ok bool, available bool) {
	select {
	case srv.bcryptSem <- struct{}{}:
		defer func() { <-srv.bcryptSem }()
	case <-time.After(2 * time.Second):
		return false, false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil, true
}

// limitConcurrency bounds the number of in-flight requests. Beyond the limit it
// responds 503 instead of letting goroutines/buffers pile up and exhaust memory
// on small hardware.
func limitConcurrency(max int, next http.Handler) http.Handler {
	sem := make(chan struct{}, max)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Server busy", http.StatusServiceUnavailable)
		}
	})
}

// limitBody caps the request body size for ordinary routes so a large POST
// cannot exhaust memory. Upload handlers set their own (larger) MaxBytesReader
// and are excluded.
func limitBody(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exact-match the two upload endpoints: a prefix match would also
		// exempt /admin/uploads/* (delete, resize) from the body cap.
		if r.Method == http.MethodPost && r.URL.Path != "/admin/upload" && r.URL.Path != "/admin/upload-file" {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}
