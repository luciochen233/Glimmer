package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"glimmer/internal/config"
	"glimmer/internal/db"

	"golang.org/x/crypto/bcrypt"
)

func TestMCPRequiresAuthentication(t *testing.T) {
	srv := &Server{cfg: &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "test-key"},
	}}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMCPListsToolsWithAPIKey(t *testing.T) {
	srv := &Server{cfg: &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "test-key"},
	}}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"list_links"`) || !strings.Contains(body, `"create_paste"`) {
		t.Fatalf("expected tool list in response, got %s", body)
	}
}

func TestMCPRejectsUnexpectedOrigin(t *testing.T) {
	srv := &Server{cfg: &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "test-key"},
	}}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestMCPAcceptsBasicAuth(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	srv := &Server{cfg: &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
		Admin:  config.AdminConfig{Username: "admin", PasswordHash: string(hash)},
		MCP:    config.MCPConfig{Enabled: true},
	}}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMCPCreateAndListContent(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "urls.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	srv := &Server{
		cfg: &config.Config{
			Server: config.ServerConfig{BaseURL: "http://localhost:8888"},
			Slugs:  config.SlugConfig{Length: 3},
			Upload: config.UploadConfig{Dir: t.TempDir(), MaxSize: 50},
		},
		db: database,
	}

	linkResult, err := srv.mcpCreateLink(map[string]any{
		"url":  "example.com/docs",
		"slug": "docs",
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	link := linkResult.(mcpLink)
	if link.URL != "https://example.com/docs" || link.ShortURL != "http://localhost:8888/docs" {
		t.Fatalf("unexpected link result: %#v", link)
	}

	pasteResult, err := srv.mcpCreatePaste(map[string]any{
		"name":           "release-notes",
		"title":          "Release notes",
		"content":        "Shipped MCP support",
		"generate_token": true,
		"hidden":         true,
	})
	if err != nil {
		t.Fatalf("create paste: %v", err)
	}
	paste := pasteResult.(mcpPaste)
	if paste.Token == "" || paste.TokenURL == "" || !paste.Hidden {
		t.Fatalf("expected protected paste, got %#v", paste)
	}

	links, err := srv.mcpListLinks(map[string]any{"query": "docs"})
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if got := links.([]mcpLink); len(got) != 1 || got[0].Slug != "docs" {
		t.Fatalf("expected docs link, got %#v", got)
	}
}
