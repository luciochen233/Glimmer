package httpguard

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// rawGet sends a request line verbatim (no client-side path cleaning) and
// returns the status code and Location header.
func rawGet(t *testing.T, addr, target string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n", target)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response for %q: %v", target, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// A request for //EVIL.example/X must never be answered with a redirect whose
// Location a browser would resolve to another host.
func TestLowercasePathNoOpenRedirect(t *testing.T) {
	srv := httptest.NewServer(LowercasePath(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "/static/", "/admin"))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	for _, target := range []string{"//EVIL.example/X", "///EVIL.example/X", "/\\EVIL.example/X", "//EVIL.example", "/%2FEVIL.example/X", "/ABC"} {
		code, loc := rawGet(t, addr, target)
		if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, "/\\") || strings.Contains(loc, "://") {
			t.Errorf("%q: open redirect via Location %q (status %d)", target, loc, code)
		}
	}
	if code, loc := rawGet(t, addr, "/ABC?Q=1"); code != http.StatusMovedPermanently || loc != "/abc?Q=1" {
		t.Errorf("/ABC?Q=1: got %d %q, want 301 /abc?Q=1", code, loc)
	}
	// Exempt prefixes and already-lowercase paths pass straight through.
	for _, target := range []string{"/static/Logo.PNG", "/admin/Edit/1", "/ADMIN/x", "/abc"} {
		if code, loc := rawGet(t, addr, target); code != http.StatusNoContent {
			t.Errorf("%s: got %d (Location %q), want pass-through", target, code, loc)
		}
	}
}

func TestClientIPUsesTrustedHopFromRight(t *testing.T) {
	mk := func(xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	realIP := mk()
	realIP.Header.Set("X-Real-Ip", "203.0.113.9")
	cases := []struct {
		name string
		r    *http.Request
		hops int
		want string
	}{
		{"untrusted ignores header", mk("6.6.6.6"), 0, "10.0.0.1"},
		{"spoofed prefix ignored", mk("6.6.6.6, 203.0.113.9"), 1, "203.0.113.9"},
		{"spoofed separate header line ignored", mk("6.6.6.6", "203.0.113.9"), 1, "203.0.113.9"},
		{"two hops", mk("6.6.6.6, 203.0.113.9, 172.16.0.2"), 2, "203.0.113.9"},
		{"chain shorter than hops falls back", mk("6.6.6.6"), 2, "10.0.0.1"},
		{"no header", mk(), 1, "10.0.0.1"},
		{"X-Real-Ip with one hop", realIP, 1, "203.0.113.9"},
		{"X-Real-Ip ignored when untrusted", realIP, 0, "10.0.0.1"},
	}
	for _, c := range cases {
		if got := ClientIP(c.r, c.hops); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRateLimiterAllowAndGuard(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	if !rl.Allow("a") {
		t.Fatal("first Allow should pass")
	}
	if rl.Allow("a") {
		t.Fatal("second Allow inside the window should be refused")
	}
	if !rl.Allow("b") {
		t.Fatal("a different key must not be affected")
	}

	rl.Forget("a")
	if !rl.Allow("a") {
		t.Fatal("Allow should pass again after Forget")
	}

	g := NewRateLimiter(time.Minute)
	if ok, limited := g.Guard("ip", func() bool { return true }); !ok || limited {
		t.Fatalf("success: ok=%v limited=%v", ok, limited)
	}
	if ok, limited := g.Guard("ip", func() bool { return true }); !ok || limited {
		t.Fatal("successes must never start the window")
	}
	if ok, limited := g.Guard("ip", func() bool { return false }); ok || limited {
		t.Fatalf("first failure: ok=%v limited=%v", ok, limited)
	}
	ran := false
	if ok, limited := g.Guard("ip", func() bool { ran = true; return true }); ok || !limited || ran {
		t.Fatalf("after a failure the check must be skipped: ok=%v limited=%v ran=%v", ok, limited, ran)
	}
}

func TestGlobalRateLimiterBurst(t *testing.T) {
	g := NewGlobalRateLimiter(10, 10)
	for i := range 10 {
		if !g.Allow() {
			t.Fatalf("allow #%d denied inside the burst", i+1)
		}
	}
	if g.Allow() {
		t.Fatal("allow should be denied once the burst is spent")
	}
}

// A state-changing request from another origin — including a sibling
// subdomain, which is same-site — must be refused before reaching a handler.
func TestCrossOriginGuard(t *testing.T) {
	reached := false
	h := CrossOriginGuard("https://app.example", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		allowed bool
	}{
		{"same-origin form post", "POST", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://app.example"}, true},
		{"sibling subdomain (same-site)", "POST", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://other.app.example"}, false},
		{"unrelated site", "POST", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		{"old browser, foreign Origin only", "POST", map[string]string{"Origin": "https://evil.example"}, false},
		{"old browser, own Origin behind host-rewriting proxy", "POST", map[string]string{"Origin": "https://app.example"}, true},
		{"non-browser client", "POST", nil, true},
		{"cross-site GET is a plain navigation", "GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
	}
	for _, c := range cases {
		reached = false
		req := httptest.NewRequest(c.method, "http://127.0.0.1:8888/admin/delete/1", nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if reached != c.allowed {
			t.Errorf("%s: reached handler = %v, want %v (status %d)", c.name, reached, c.allowed, rec.Code)
		}
	}
}

func TestNoDirFSServesFilesNotListings(t *testing.T) {
	files := fstest.MapFS{
		"css/style.css": {Data: []byte("body{}")},
		"js/app.js":     {Data: []byte("1")},
	}
	h := http.StripPrefix("/static/", http.FileServer(NoDirFS{FS: http.FS(files)}))
	for path, want := range map[string]int{
		"/static/":              http.StatusNotFound,
		"/static/js/":           http.StatusNotFound,
		"/static/js":            http.StatusNotFound,
		"/static/css/style.css": http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestLimitBody(t *testing.T) {
	h := LimitBody(8, func(r *http.Request) bool { return r.URL.Path == "/upload" },
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.ReadAll(r.Body); err != nil {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
			}
		}))
	post := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(strings.Repeat("x", 64))))
		return rec.Code
	}
	if got := post("/form"); got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized ordinary POST: got %d, want 413", got)
	}
	if got := post("/upload"); got != http.StatusOK {
		t.Errorf("exempt route: got %d, want 200", got)
	}
}

func TestLimitConcurrency(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	h := LimitConcurrency(1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	<-entered
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	close(release)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("second concurrent request: got %d, want 503", rec.Code)
	}
}

func TestPasswordGate(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("right"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	g := NewPasswordGate(0) // clamps to 1
	if ok, avail := g.Check(string(hash), "right"); !ok || !avail {
		t.Errorf("correct password: ok=%v available=%v", ok, avail)
	}
	if ok, avail := g.Check(string(hash), "wrong"); ok || !avail {
		t.Errorf("wrong password: ok=%v available=%v", ok, avail)
	}
}

func TestSessionKeyIsNotTheToken(t *testing.T) {
	token := strings.Repeat("ab", 32)
	key := SessionKey(token)
	if key == token || len(key) != 64 {
		t.Fatalf("unexpected key %q", key)
	}
	if SessionKey(token) != key || SessionKey(token+"x") == key {
		t.Fatal("SessionKey must be deterministic and input-sensitive")
	}
}
