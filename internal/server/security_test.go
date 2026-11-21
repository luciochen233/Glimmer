package server

import (
	"bufio"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"glimmer/internal/config"
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
// Location a browser would resolve to another host (scheme-relative "//host").
func TestLowercasePathNoOpenRedirect(t *testing.T) {
	srv := httptest.NewServer(lowercasePath(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	for _, target := range []string{"//EVIL.example/X", "///EVIL.example/X", "/\\EVIL.example/X", "//EVIL.example", "/%2FEVIL.example/X", "/ABC"} {
		code, loc := rawGet(t, addr, target)
		t.Logf("%q -> %d Location=%q", target, code, loc)
		if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, "/\\") || strings.Contains(loc, "://") {
			t.Errorf("%q: open redirect via Location %q", target, loc)
		}
	}
	if code, loc := rawGet(t, addr, "/ABC?Q=1"); code != http.StatusMovedPermanently || loc != "/abc?Q=1" {
		t.Errorf("/ABC?Q=1: got %d %q, want 301 /abc?Q=1", code, loc)
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
	}
	for _, c := range cases {
		if got := clientIP(c.r, c.hops); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func binMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /bin/{name}", s.handleBinView)
	mux.HandleFunc("GET /bin/{name}/{token}", s.handleBinView)
	return mux
}

func binGet(mux http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// A hidden paste probed without (or with a wrong) token must look exactly like
// a paste that does not exist, or its name can be confirmed by an outsider.
func TestHiddenPasteIndistinguishableFromMissing(t *testing.T) {
	s := webTestServer(t, nil)
	if _, err := s.db.CreatePaste("secret", "", "body", "", "", "text", "tok123", true); err != nil {
		t.Fatal(err)
	}
	mux := binMux(s)
	missing := binGet(mux, "/bin/nope")
	for _, p := range []string{"/bin/secret", "/bin/secret/wrong"} {
		got := binGet(mux, p)
		if got.Code != missing.Code || got.Body.String() != missing.Body.String() ||
			got.Header().Get("Content-Type") != missing.Header().Get("Content-Type") {
			t.Errorf("%s (%d) is distinguishable from a missing paste (%d)", p, got.Code, missing.Code)
		}
	}
	if got := binGet(mux, "/bin/secret/tok123"); got.Code != http.StatusOK {
		t.Errorf("correct token: got %d, want 200", got.Code)
	}
}

func TestPasteTokenGuessesAreThrottled(t *testing.T) {
	s := webTestServer(t, nil)
	s.tokenLimiter = newRateLimiter(time.Minute)
	if _, err := s.db.CreatePaste("locked", "", "body", "", "", "text", "tok123", false); err != nil {
		t.Fatal(err)
	}
	mux := binMux(s)
	// The token prompt itself (no guess) is never counted.
	if got := binGet(mux, "/bin/locked"); got.Code != http.StatusForbidden {
		t.Fatalf("prompt: got %d, want 403", got.Code)
	}
	if got := binGet(mux, "/bin/locked/tok123"); got.Code != http.StatusOK {
		t.Fatalf("correct token before any failure: got %d, want 200", got.Code)
	}
	if got := binGet(mux, "/bin/locked/wrong"); got.Code != http.StatusForbidden {
		t.Fatalf("first wrong guess: got %d, want 403", got.Code)
	}
	// Inside the window even the right token is refused, so a guesser learns
	// nothing from further attempts.
	for _, p := range []string{"/bin/locked/wrong2", "/bin/locked/tok123"} {
		if got := binGet(mux, p); got.Code != http.StatusTooManyRequests {
			t.Errorf("%s inside window: got %d, want 429", p, got.Code)
		}
	}
}

func TestVerifyCSRFRejectsEmptyToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("csrf_token="))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "csrf", Value: ""})
	if verifyCSRF(r) {
		t.Fatal("empty csrf cookie + empty form token must not verify")
	}
}

func TestGenerateUniqueSlugGrowsWhenSpaceExhausted(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Slugs.Length = 1 })
	for _, ch := range "abcdefghijklmnopqrstuvwxyz0123456789" {
		if _, err := s.db.Create(string(ch), "https://example.com/"+string(ch), "anonymous"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.generateUniqueSlug()
	if err != nil || len(got) < 2 {
		t.Fatalf("got %q, %v; want a longer slug", got, err)
	}
}

// A state-changing request from another origin must be refused before it
// reaches a handler — including one from a sibling subdomain, which is
// same-site (so SameSite=Lax cookies are sent) and can plant its own csrf
// cookie for the parent domain.
func TestCrossOriginGuard(t *testing.T) {
	reached := false
	h := crossOriginGuard("https://luci.example", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		allowed bool
	}{
		{"same-origin form post", "POST", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://luci.example"}, true},
		{"sibling subdomain (same-site)", "POST", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://other.luci.example"}, false},
		{"unrelated site", "POST", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		{"old browser, foreign Origin only", "POST", map[string]string{"Origin": "https://evil.example"}, false},
		{"old browser, own Origin behind host-rewriting proxy", "POST", map[string]string{"Origin": "https://luci.example"}, true},
		{"non-browser client (curl, MCP)", "POST", nil, true},
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
		if !c.allowed && rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", c.name, rec.Code)
		}
	}
}

// The database must never hold a value that works as a session cookie.
func TestSessionTokenNotStoredInClear(t *testing.T) {
	s := webTestServer(t, nil)
	token, err := s.sessions.Create()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !s.sessions.Valid(token) {
		t.Fatal("fresh session token should be valid")
	}
	if ok, _ := s.db.SessionValid(token); ok {
		t.Fatal("raw cookie value is stored in the sessions table")
	}
	// What the table does hold must not be usable as a cookie either.
	if s.sessions.Valid(sessionKey(token)) {
		t.Fatal("stored session key is accepted as a session token")
	}
	s.sessions.Delete(token)
	if s.sessions.Valid(token) {
		t.Fatal("session still valid after delete")
	}
}

// /static/ must serve files but never an auto-generated directory listing.
func TestStaticServesFilesNotListings(t *testing.T) {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	h := http.StripPrefix("/static/", http.FileServer(noDirFS{FS: http.FS(staticSub)}))
	for path, want := range map[string]int{
		"/static/":              http.StatusNotFound,
		"/static/js/":           http.StatusNotFound,
		"/static/js":            http.StatusNotFound,
		"/static/css/style.css": http.StatusOK,
		"/static/js/base.js":    http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

// The upload handlers extend their deadlines through http.ResponseController,
// which only works if the request-log wrapper exposes the real writer.
func TestStatusRecorderAllowsDeadlineExtension(t *testing.T) {
	var deadlineErr error
	s := webTestServer(t, nil)
	s.reqlog = newRequestLog(10)
	srv := httptest.NewServer(s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadlineErr = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Minute))
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if deadlineErr != nil {
		t.Fatalf("SetReadDeadline through the request-log wrapper: %v", deadlineErr)
	}
}
