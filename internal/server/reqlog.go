package server

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// requestEntry is a single line in the in-RAM request log. Kept small so the
// whole ring (capacity 100) costs only a few KB of RAM.
type requestEntry struct {
	Time       time.Time
	Method     string
	Path       string
	Status     int
	DurationMS int64
	IP         string
	Actor      string // "anon" | "admin" | "api" | "mcp"
}

// requestLog is a fixed-capacity ring buffer of the most recent request
// entries. It lives entirely in RAM and is bounded so it cannot grow
// unbounded on small hardware. Writes are mutex-guarded; reads are cheap.
type requestLog struct {
	mu       sync.Mutex
	entries  []requestEntry
	capacity int
}

func newRequestLog(capacity int) *requestLog {
	if capacity < 1 {
		capacity = 100
	}
	return &requestLog{
		entries:  make([]requestEntry, 0, capacity),
		capacity: capacity,
	}
}

func (l *requestLog) Add(e requestEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	// Drop oldest once over capacity. For a 100-entry ring this trivial copy
	// is far cheaper than allocating a proper circular buffer.
	if len(l.entries) > l.capacity {
		l.entries = l.entries[len(l.entries)-l.capacity:]
	}
}

// Recent returns a newest-first copy of the buffered entries.
func (l *requestLog) Recent() []requestEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.entries)
	out := make([]requestEntry, n)
	for i, e := range l.entries {
		out[n-1-i] = e
	}
	return out
}

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// shouldLogRequest reports whether a path is worth a slot in the ring buffer.
// Noisy static/asset/health paths are excluded so the buffer stays full of
// useful application traffic instead of CSS/JS/thumbnail fetches.
func shouldLogRequest(path string) bool {
	if strings.HasPrefix(path, "/static/") {
		return false
	}
	if strings.HasPrefix(path, "/uploads/thumb/") {
		return false
	}
	if path == "/healthz" {
		return false
	}
	if path == "/favicon.ico" || path == "/favicon.png" {
		return false
	}
	return true
}

// actorFor classifies the request source without a DB lookup: API/MCP by path
// prefix, admin by presence of a session cookie, everything else anonymous.
// Cookie presence (not validity) is intentional — it is free and good enough
// for an eyeball log.
func actorFor(r *http.Request) string {
	switch {
	case strings.HasPrefix(r.URL.Path, "/mcp"):
		return "mcp"
	case strings.HasPrefix(r.URL.Path, "/api/"):
		return "api"
	default:
		if c, err := r.Cookie("session"); err == nil && c.Value != "" {
			return "admin"
		}
		return "anon"
	}
}

// logRequests records a compact entry for each non-noisy request after it is
// served. It wraps the inner handler with a statusRecorder to capture the
// response code. Allocation per request is one small struct plus the ring
// append; well within the RPi Zero's budget.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if !shouldLogRequest(r.URL.Path) {
			return
		}
		s.reqlog.Add(requestEntry{
			Time:       start,
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     rec.status,
			DurationMS: time.Since(start).Milliseconds(),
			IP:         clientIP(r, s.cfg.Server.TrustProxy),
			Actor:      actorFor(r),
		})
	})
}
