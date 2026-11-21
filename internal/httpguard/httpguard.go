// Package httpguard is the HTTP hardening shared by Glimmer and Solis: client
// IP resolution behind proxies, rate limiting, cross-origin (CSRF) rejection,
// safe path normalisation, body and concurrency caps, and password checking.
//
// SHARED CODE: this directory is kept byte-identical in both repositories
// (github.com/luciochen233/Glimmer and github.com/luciochen233/Solis). It
// depends only on the standard library and golang.org/x/crypto, and on nothing
// app-specific. Change it in one repository, run its tests, then copy the
// directory to the other — never let the two copies drift.
package httpguard

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---- Client IP ----

// ClientIP returns the client IP for rate limiting and logging. proxyHops is
// the number of trusted reverse proxies in front of the server (0 = none:
// forwarded headers are ignored, because any client can set them).
//
// Each proxy APPENDS the address it received the connection from to
// X-Forwarded-For; it does not remove what the client sent. So only the last
// proxyHops entries are trustworthy and the real client is the proxyHops-th
// entry from the right. Taking the first entry instead would let anyone forge
// an arbitrary IP and sidestep every per-IP throttle.
func ClientIP(r *http.Request, proxyHops int) string {
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

// ---- Rate limiting ----

// RateLimiter allows one event per key (normally a client IP) per window.
type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]time.Time
	window  time.Duration
}

func NewRateLimiter(window time.Duration) *RateLimiter {
	return &RateLimiter{clients: make(map[string]time.Time), window: window}
}

// Allow reports whether key may proceed, and if so starts its window.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if last, ok := rl.clients[key]; ok && now.Sub(last) < rl.window {
		return false
	}
	rl.clients[key] = now
	rl.purgeLocked(now)
	return true
}

// Forget clears key's window. Call it after a successful attempt that was
// admitted with Allow, so that only failures end up throttled. Unlike Guard,
// this pattern suits expensive checks (bcrypt): the window is reserved before
// the check runs, without holding the limiter lock during it.
func (rl *RateLimiter) Forget(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.clients, key)
}

// Guard throttles failed secret guesses per key. check is skipped
// (limited=true) while key has a failure inside the window; otherwise it runs,
// and a false result starts the window. Successes are never throttled. check
// runs under the limiter lock so concurrent guesses from one key cannot race
// past it — keep it cheap (a constant-time compare, not bcrypt).
func (rl *RateLimiter) Guard(key string, check func() bool) (ok, limited bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if last, found := rl.clients[key]; found && now.Sub(last) < rl.window {
		return false, true
	}
	if check() {
		return true, false
	}
	rl.clients[key] = now
	rl.purgeLocked(now)
	return false, false
}

// purgeLocked drops stale entries once the map grows large. Caller holds mu.
func (rl *RateLimiter) purgeLocked(now time.Time) {
	if len(rl.clients) > 10000 {
		for k, v := range rl.clients {
			if now.Sub(v) > rl.window*10 {
				delete(rl.clients, k)
			}
		}
	}
}

// GlobalRateLimiter is a server-wide token bucket that caps total throughput
// across ALL clients (unlike RateLimiter, which is per key). A distributed
// flood still runs into a single ceiling. Tokens refill continuously at
// ratePerSec up to burst.
type GlobalRateLimiter struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillPerS float64
	last       time.Time
}

func NewGlobalRateLimiter(ratePerSec, burst int) *GlobalRateLimiter {
	return &GlobalRateLimiter{
		tokens:     float64(burst),
		maxTokens:  float64(burst),
		refillPerS: float64(ratePerSec),
		last:       time.Now(),
	}
}

func (g *GlobalRateLimiter) Allow() bool {
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

// ---- Password checking ----

// PasswordGate runs bcrypt comparisons while bounding how many run at once.
// bcrypt is deliberately CPU-heavy, so an unbounded flood of login attempts
// can exhaust a small machine.
type PasswordGate struct {
	sem chan struct{}
}

// NewPasswordGate allows at most workers concurrent comparisons (minimum 1).
func NewPasswordGate(workers int) *PasswordGate {
	if workers < 1 {
		workers = 1
	}
	return &PasswordGate{sem: make(chan struct{}, workers)}
}

// Check compares password against a bcrypt hash. available is false when the
// gate could not be acquired within two seconds; the caller should answer 503
// rather than queue more work.
func (g *PasswordGate) Check(hash, password string) (ok bool, available bool) {
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	case <-time.After(2 * time.Second):
		return false, false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil, true
}

// ---- Sessions ----

// SessionKey is what a session store should keep for a session cookie: the
// SHA-256 of the cookie value, never the value itself, so a copy of the store
// cannot be replayed as a login. The token must be high-entropy random data,
// which is what makes an unsalted fast hash sufficient.
func SessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---- Middleware ----

// CrossOriginGuard rejects state-changing browser requests that do not come
// from this site's own pages (Sec-Fetch-Site / Origin check, see
// http.CrossOriginProtection). It backs up a double-submit CSRF token: a page
// on a sibling subdomain can plant its own csrf cookie for the parent domain
// and, being same-site, still gets the session cookie sent along — so the
// token alone does not stop it. Requests with neither header (curl, MCP/API
// clients) are unaffected. baseURL is trusted explicitly so a proxy that
// rewrites Host cannot lock the admin out.
func CrossOriginGuard(baseURL string, next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
		_ = cop.AddTrustedOrigin(u.Scheme + "://" + u.Host)
	}
	return cop.Handler(next)
}

// LowercasePath redirects paths containing uppercase letters to their
// lowercase form (QR codes are generated in upper case). Paths whose lowercase
// form starts with one of exemptPrefixes are passed through untouched.
//
// The Location is built from a single-slash-rooted path. Echoing the request
// path back verbatim would turn "//EVIL.example/x" (or "/%2FEVIL.example")
// into a scheme-relative Location, i.e. an open redirect to another host.
func LowercasePath(next http.Handler, exemptPrefixes ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lower := strings.ToLower(r.URL.Path)
		if lower == r.URL.Path {
			next.ServeHTTP(w, r)
			return
		}
		for _, p := range exemptPrefixes {
			if strings.HasPrefix(lower, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		target := url.URL{Path: "/" + strings.TrimLeft(lower, `/\`), RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, target.String(), http.StatusMovedPermanently)
	})
}

// NoDirFS serves files but never directories, so a static file server returns
// 404 for a directory instead of an auto-generated listing.
type NoDirFS struct{ FS http.FileSystem }

func (n NoDirFS) Open(name string) (http.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// LimitBody caps the request body of POST requests so a large body cannot
// exhaust memory. Requests for which exempt returns true are left alone: those
// handlers (file uploads) must set their own, larger http.MaxBytesReader
// before anything parses the form.
func LimitBody(maxBytes int64, exempt func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (exempt == nil || !exempt(r)) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// LimitConcurrency bounds the number of in-flight requests. Beyond the limit
// it answers 503 instead of letting goroutines and buffers pile up.
func LimitConcurrency(max int, next http.Handler) http.Handler {
	if max < 1 {
		max = 1
	}
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
