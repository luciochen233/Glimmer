package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
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

// sessionKey is what the database stores for a session: the SHA-256 of the
// cookie value, never the value itself. A copy of urls.db (a backup, a stray
// read) therefore cannot be replayed as a login. The token is 256 random bits,
// so an unsalted fast hash is sufficient.
func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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

// crossOriginGuard rejects state-changing browser requests that do not come
// from this site's own pages (Sec-Fetch-Site / Origin check, see
// http.CrossOriginProtection). It backs up the CSRF token, which is a
// double-submit cookie: a page on a sibling subdomain can plant its own "csrf"
// cookie for the parent domain and, being same-site, still gets the session
// cookie sent along — so the token alone does not stop it. Requests with
// neither header (curl, MCP/API clients) are unaffected. baseURL is trusted
// explicitly so a proxy that rewrites Host cannot lock the admin out.
func crossOriginGuard(baseURL string, next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
		_ = cop.AddTrustedOrigin(u.Scheme + "://" + u.Host)
	}
	return cop.Handler(next)
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
	rl.purgeLocked(now)
	return true
}

// purgeLocked drops stale entries once the map grows large. Caller holds mu.
func (rl *rateLimiter) purgeLocked(now time.Time) {
	if len(rl.clients) > 10000 {
		for k, v := range rl.clients {
			if now.Sub(v) > rl.window*10 {
				delete(rl.clients, k)
			}
		}
	}
}

// Guard throttles failed secret guesses per IP. check is skipped (limited=true)
// while ip has a failure inside the window; otherwise it runs, and a false
// result starts the window. Successes are never throttled. check runs under
// the limiter lock so concurrent guesses from one IP cannot race past it —
// keep it cheap.
func (rl *rateLimiter) Guard(ip string, check func() bool) (ok, limited bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if last, found := rl.clients[ip]; found && now.Sub(last) < rl.window {
		return false, true
	}
	if check() {
		return true, false
	}
	rl.clients[ip] = now
	rl.purgeLocked(now)
	return false, false
}

// globalRateLimiter is a server-wide token bucket that caps total throughput
// across ALL clients (unlike rateLimiter, which is per-IP). A distributed
// flood still runs into a single ceiling. tokens refill continuously at
// refillPerS up to maxTokens (the burst size).
type globalRateLimiter struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillPerS float64
	last       time.Time
}

func newGlobalRateLimiter(ratePerSec, burst int) *globalRateLimiter {
	return &globalRateLimiter{
		tokens:     float64(burst),
		maxTokens:  float64(burst),
		refillPerS: float64(ratePerSec),
		last:       time.Now(),
	}
}

func (g *globalRateLimiter) Allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.tokens += now.Sub(g.last).Seconds() * g.refillPerS
	if g.tokens > g.maxTokens {
		g.tokens = g.maxTokens
	}
	g.last = now
	if g.tokens >= 1 {
		g.tokens--
		return true
	}
	return false
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

// clientIP returns the client IP. proxyHops is the number of trusted reverse
// proxies in front of the server (0 = none: forwarded headers are ignored,
// because any client can set them).
//
// Each proxy APPENDS the address it received the connection from to
// X-Forwarded-For; it does not remove what the client sent. So only the last
// proxyHops entries are trustworthy and the real client is the proxyHops-th
// entry from the right. Taking the first entry instead would let anyone forge
// an arbitrary IP and sidestep the login / shorten / paste-token throttles.
func clientIP(r *http.Request, proxyHops int) string {
	if proxyHops > 0 {
		if xff := strings.Join(r.Header.Values("X-Forwarded-For"), ","); xff != "" {
			parts := strings.Split(xff, ",")
			if i := len(parts) - proxyHops; i >= 0 {
				if ip := strings.TrimSpace(parts[i]); ip != "" {
					return ip
				}
			}
			// Shorter chain than configured: the request did not come through
			// all the proxies, so nothing in the header can be trusted.
		} else if xri := strings.TrimSpace(r.Header.Get("X-Real-Ip")); xri != "" && proxyHops == 1 {
			return xri
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
