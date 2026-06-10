package server

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glimmer/internal/config"
	"glimmer/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// webTestServer builds a Server wired like production (DB, sessions, upload
// dir, templates) for exercising the web handlers. mutate can adjust the
// config before the server is built.
func webTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	initTemplates()
	database, err := db.Open(filepath.Join(t.TempDir(), "urls.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		Admin:  config.AdminConfig{Username: "admin", SessionHours: 1},
		Slugs:  config.SlugConfig{Length: 3},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxSize: 50},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return &Server{
		cfg:         cfg,
		db:          database,
		sessions:    newSessionStore(database, time.Hour),
		limiter:     newRateLimiter(0),
		authLimiter: newRateLimiter(0),
		bcryptSem:   make(chan struct{}, 1),
	}
}

func sessionCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	token, err := s.sessions.Create()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: "session", Value: token}
}

// ---- Auth ----

func TestRequireAuthRedirectsAnonymous(t *testing.T) {
	s := webTestServer(t, nil)
	handler := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run without a session")
	})

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/login" {
		t.Fatalf("expected redirect to /admin/login, got %q", loc)
	}
}

func TestRequireAuthAcceptsValidSession(t *testing.T) {
	s := webTestServer(t, nil)
	called := false
	handler := s.requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(sessionCookie(t, s))
	handler(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("handler should run with a valid session")
	}
}

func TestLoginWrongPasswordRejected(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	s := webTestServer(t, func(cfg *config.Config) {
		cfg.Admin.PasswordHash = string(hash)
	})

	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			t.Fatal("no session cookie may be set on failed login")
		}
	}
}

func TestLoginSuccessSetsSessionCookie(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	s := webTestServer(t, func(cfg *config.Config) {
		cfg.Admin.PasswordHash = string(hash)
	})

	form := url.Values{"username": {"admin"}, "password": {"correct-horse"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			session = c
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("expected a session cookie on successful login")
	}
	if !session.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}
	if !s.sessions.Valid(session.Value) {
		t.Fatal("issued session token should validate")
	}
}

// ---- CSRF ----

func TestRequireCSRFRejectsMissingToken(t *testing.T) {
	s := webTestServer(t, nil)
	handler := s.requireCSRF(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run without a CSRF token")
	})

	req := httptest.NewRequest(http.MethodPost, "/admin/delete/1", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireCSRFAcceptsMatchingToken(t *testing.T) {
	s := webTestServer(t, nil)
	called := false
	handler := s.requireCSRF(func(w http.ResponseWriter, r *http.Request) { called = true })

	token := strings.Repeat("a", 64)
	form := url.Values{"csrf_token": {token}}
	req := httptest.NewRequest(http.MethodPost, "/admin/delete/1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf", Value: token})
	handler(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("handler should run with matching CSRF cookie and form token")
	}
}

func TestResizeRejectsMissingCSRF(t *testing.T) {
	s := webTestServer(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/admin/uploads/resize/x", strings.NewReader("max_dim=512"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("filename", strings.Repeat("a", 32)+".png")
	rec := httptest.NewRecorder()
	s.handleAdminUploadResize(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF token, got %d", rec.Code)
	}
}

func TestCSRFCookieSecureFollowsBaseURL(t *testing.T) {
	s := webTestServer(t, func(cfg *config.Config) {
		cfg.Server.BaseURL = "https://luci.ooo"
	})
	rec := httptest.NewRecorder()
	s.csrfToken(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var csrf *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "csrf" {
			csrf = c
		}
	}
	if csrf == nil {
		t.Fatal("expected a csrf cookie to be set")
	}
	if !csrf.Secure {
		t.Fatal("csrf cookie must be Secure when base_url is https")
	}
}

// ---- Upload serving ----

func writeUploadFile(t *testing.T, s *Server, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.cfg.Upload.Dir, name), content, 0644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
}

func TestUploadsServeRejectsInvalidNames(t *testing.T) {
	s := webTestServer(t, nil)
	for _, name := range []string{
		"../config.toml",
		"..%2f..%2fconfig.toml",
		"notahash.txt",
		strings.Repeat("a", 32), // no extension
		strings.Repeat("a", 32) + ".html/extra",
	} {
		req := httptest.NewRequest(http.MethodGet, "/uploads/"+name, nil)
		rec := httptest.NewRecorder()
		s.handleUploadsServe(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("name %q: expected 404, got %d", name, rec.Code)
		}
	}
}

func TestUploadsServeForcesAttachmentForNonImages(t *testing.T) {
	s := webTestServer(t, nil)
	name := strings.Repeat("ab", 16) + ".html"
	writeUploadFile(t, s, name, []byte("<script>alert(1)</script>"))

	req := httptest.NewRequest(http.MethodGet, "/uploads/"+name, nil)
	rec := httptest.NewRecorder()
	s.handleUploadsServe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("expected octet-stream, got %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("expected attachment disposition, got %q", cd)
	}
}

func TestUploadsServeImagesInline(t *testing.T) {
	s := webTestServer(t, nil)
	name := strings.Repeat("cd", 16) + ".png"
	writeUploadFile(t, s, name, smallPNG(t))

	req := httptest.NewRequest(http.MethodGet, "/uploads/"+name, nil)
	rec := httptest.NewRecorder()
	s.handleUploadsServe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("images should be served inline, got disposition %q", cd)
	}
}

// ---- Decompression-bomb guard ----

func smallPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 10, 10))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// bombPNGHeader builds a syntactically valid PNG signature + IHDR chunk that
// declares the given dimensions. DecodeConfig succeeds on it; a full decode
// of real data at this size would allocate width*height*4 bytes.
func bombPNGHeader(width, height uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8  // bit depth
	ihdr[9] = 6  // color type RGBA
	chunk := append([]byte("IHDR"), ihdr...)
	binary.Write(&buf, binary.BigEndian, uint32(13))
	buf.Write(chunk)
	binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return buf.Bytes()
}

func TestDecodeImageBoundedRejectsHugeDimensions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bomb.png")
	// 50000x50000 = 2.5 gigapixels — would decode to ~10 GB of NRGBA.
	if err := os.WriteFile(path, bombPNGHeader(50000, 50000), 0644); err != nil {
		t.Fatalf("write bomb: %v", err)
	}
	if _, err := decodeImageBounded(path); err == nil {
		t.Fatal("expected oversized image to be rejected before decode")
	} else if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected dimension error, got: %v", err)
	}
}

func TestDecodeImageBoundedAcceptsNormalImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.png")
	if err := os.WriteFile(path, smallPNG(t), 0644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	img, err := decodeImageBounded(path)
	if err != nil {
		t.Fatalf("expected small image to decode, got: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 10 || b.Dy() != 10 {
		t.Fatalf("unexpected bounds: %v", b)
	}
}

func TestThumbRouteRefusesBombViaFallback(t *testing.T) {
	// A bomb header upload must not OOM the public thumb route: generation
	// fails the bounds check and the handler falls back to serving the
	// original bytes without decoding them.
	s := webTestServer(t, nil)
	name := strings.Repeat("ef", 16) + ".png"
	writeUploadFile(t, s, name, bombPNGHeader(50000, 50000))

	req := httptest.NewRequest(http.MethodGet, "/uploads/thumb/"+name, nil)
	req.SetPathValue("filename", name)
	rec := httptest.NewRecorder()
	s.handleThumb(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 fallback, got %d", rec.Code)
	}
	if _, err := os.Stat(s.thumbPath(name)); !os.IsNotExist(err) {
		t.Fatal("no thumbnail may be generated for an oversized image")
	}
}
