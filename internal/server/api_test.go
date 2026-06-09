package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"glimmer/internal/config"
	"glimmer/internal/db"
)

func TestAPICreateLinkRequiresAuth(t *testing.T) {
	srv := newTestServer(&config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "secret"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com"}`))
	rec := httptest.NewRecorder()

	srv.apiCreateLink(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("expected WWW-Authenticate header, got %q", rec.Header().Get("WWW-Authenticate"))
	}
}

func TestAPICreateLinkRejectsBadKey(t *testing.T) {
	srv := newTestServer(&config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "secret"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()

	srv.apiCreateLink(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAPICreateLinkRejectsEmptyKeyConfig(t *testing.T) {
	// Even if the route somehow got registered with no key configured,
	// apiKeyAuth must refuse. This is the defence-in-depth check.
	srv := newTestServer(&config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: ""},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()

	srv.apiCreateLink(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no key is configured, got %d", rec.Code)
	}
}

func TestAPICreateLinkWithBearerKey(t *testing.T) {
	srv := apiTestServer(t, "secret")

	body := strings.NewReader(`{"url":"https://example.com/page"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/links", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.apiCreateLink(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp apiLinkResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.URL != "https://example.com/page" {
		t.Fatalf("unexpected URL: %q", resp.URL)
	}
	if resp.ShortURL != "http://localhost:8888/"+resp.Slug {
		t.Fatalf("unexpected short_url: %q", resp.ShortURL)
	}
	if resp.Slug == "" {
		t.Fatalf("expected auto-generated slug, got empty")
	}
	if resp.CreatedBy != "admin" {
		t.Fatalf("expected created_by=admin, got %q", resp.CreatedBy)
	}
}

func TestAPICreateLinkWithXAPIKeyHeader(t *testing.T) {
	srv := apiTestServer(t, "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set("X-API-Key", "secret")
	rec := httptest.NewRecorder()

	srv.apiCreateLink(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
}

func TestAPICreateLinkWithCustomSlug(t *testing.T) {
	srv := apiTestServer(t, "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com","slug":"docs"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	srv.apiCreateLink(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp apiLinkResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Slug != "docs" {
		t.Fatalf("expected slug=docs, got %q", resp.Slug)
	}
}

func TestAPICreateLinkRejectsBadURL(t *testing.T) {
	srv := apiTestServer(t, "secret")

	cases := []string{
		`{"url":""}`,
		`{"url":"not a url at all with spaces"}`,
		`{"url":"javascript:alert(1)"}`,
		`{"url":"ftp://example.com"}`,
		`{"url":"//missing-scheme"}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			srv.apiCreateLink(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", body, rec.Code)
			}
		})
	}
}

func TestAPICreateLinkAutoSchemesBareDomain(t *testing.T) {
	srv := apiTestServer(t, "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"example.com/foo"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.apiCreateLink(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp apiLinkResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.URL != "https://example.com/foo" {
		t.Fatalf("expected auto-scheme upgrade, got %q", resp.URL)
	}
}

func TestAPICreateLinkRejectsBadSlug(t *testing.T) {
	srv := apiTestServer(t, "secret")

	cases := []string{
		`{"url":"https://example.com","slug":"with space"}`,
		`{"url":"https://example.com","slug":"with/slash"}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			srv.apiCreateLink(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", body, rec.Code)
			}
		})
	}
}

func TestAPICreateLinkRejectsDuplicateSlug(t *testing.T) {
	srv := apiTestServer(t, "secret")

	first := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com/a","slug":"docs"}`))
	first.Header.Set("Authorization", "Bearer secret")
	rec1 := httptest.NewRecorder()
	srv.apiCreateLink(rec1, first)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d", rec1.Code)
	}

	second := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com/b","slug":"docs"}`))
	second.Header.Set("Authorization", "Bearer secret")
	rec2 := httptest.NewRecorder()
	srv.apiCreateLink(rec2, second)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("duplicate slug: expected 409, got %d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestAPICreateLinkDeduplicatesURL(t *testing.T) {
	srv := apiTestServer(t, "secret")

	body := `{"url":"https://example.com/dedupe"}`

	// First call: 201 Created with a fresh slug.
	req1 := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(body))
	req1.Header.Set("Authorization", "Bearer secret")
	rec1 := httptest.NewRecorder()
	srv.apiCreateLink(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first call: expected 201, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	var first apiLinkResponse
	_ = json.NewDecoder(rec1.Body).Decode(&first)

	// Second call: same URL, 200 OK with the existing link (idempotent).
	req2 := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer secret")
	rec2 := httptest.NewRecorder()
	srv.apiCreateLink(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second call: expected 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var second apiLinkResponse
	_ = json.NewDecoder(rec2.Body).Decode(&second)

	if first.ID != second.ID || first.Slug != second.Slug {
		t.Fatalf("dedupe mismatch: first=%+v second=%+v", first, second)
	}

	// And exactly one row was inserted.
	links, err := srv.db.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	count := 0
	for _, l := range links {
		if l.URL == "https://example.com/dedupe" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 link for the URL, got %d", count)
	}
}

func TestAPICreateLinkRejectsMalformedJSON(t *testing.T) {
	srv := apiTestServer(t, "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url": this is not json}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.apiCreateLink(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAPICreateLinkRejectsUnknownFields(t *testing.T) {
	srv := apiTestServer(t, "secret")

	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com","unexpected":"field"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.apiCreateLink(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field, got %d", rec.Code)
	}
}

// apiTestServer builds a Server wired up with a real on-disk SQLite DB so
// the full happy-path can be exercised. Reuses newTestServer for the auth
// fields; only adds a db handle.
func apiTestServer(t *testing.T, apiKey string) *Server {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "urls.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	return &Server{
		cfg: &config.Config{
			Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
			MCP:    config.MCPConfig{Enabled: true, APIKey: apiKey},
			Slugs:  config.SlugConfig{Length: 3},
		},
		db:          database,
		authLimiter: newRateLimiter(0),
		bcryptSem:   make(chan struct{}, 1),
	}
}
