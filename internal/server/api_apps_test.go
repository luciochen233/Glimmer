package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"glimmer/internal/config"
)

const (
	adminKey = "admin-key-0123456789abcdef"
	solisKey = "solis-key-0123456789abcdef"
	otherKey = "other-key-0123456789abcdef"
)

// appAPIServer has the administrator's key plus two application keys.
func appAPIServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := apiTestServer(t, adminKey)
	s.cfg.API.Keys = []config.APIKey{{Name: "solis", Key: solisKey}, {Name: "other", Key: otherKey}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/links", s.apiCreateLink)
	mux.HandleFunc("GET /api/links", s.apiListLinks)
	mux.HandleFunc("GET /api/links/{slug}", s.apiGetLink)
	mux.HandleFunc("PATCH /api/links/{slug}", s.apiUpdateLink)
	mux.HandleFunc("DELETE /api/links/{slug}", s.apiDeleteLink)
	return s, mux
}

func apiCall(t *testing.T, h http.Handler, method, path, key, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAppKeyLinkLifecycle(t *testing.T) {
	_, h := appAPIServer(t)

	code, created := apiCall(t, h, "POST", "/api/links", solisKey, `{"url":"https://solis.example/s/ab12cd"}`)
	if code != http.StatusCreated || created["created_by"] != "api:solis" {
		t.Fatalf("create: %d %v", code, created)
	}
	slug := created["slug"].(string)
	if created["short_url"] != "http://localhost:8888/"+slug {
		t.Errorf("short_url = %v", created["short_url"])
	}

	// Same URL again returns the same link rather than a second one.
	if code, again := apiCall(t, h, "POST", "/api/links", solisKey, `{"url":"https://solis.example/s/ab12cd"}`); code != http.StatusOK || again["slug"] != slug {
		t.Errorf("repeat create: %d %v", code, again)
	}

	if code, got := apiCall(t, h, "GET", "/api/links/"+slug, solisKey, ""); code != http.StatusOK || got["url"] != "https://solis.example/s/ab12cd" {
		t.Errorf("get: %d %v", code, got)
	}

	code, updated := apiCall(t, h, "PATCH", "/api/links/"+slug, solisKey, `{"url":"https://solis.example/s/zz99"}`)
	if code != http.StatusOK || updated["url"] != "https://solis.example/s/zz99" || updated["slug"] != slug {
		t.Errorf("update url: %d %v", code, updated)
	}
	if code, renamed := apiCall(t, h, "PATCH", "/api/links/"+slug, solisKey, `{"slug":"My-Item"}`); code != http.StatusOK || renamed["slug"] != "my-item" {
		t.Errorf("rename: %d %v", code, renamed)
	}
	if code, _ := apiCall(t, h, "GET", "/api/links/"+slug, solisKey, ""); code != http.StatusNotFound {
		t.Errorf("old slug after rename: %d, want 404", code)
	}

	if code, list := apiCall(t, h, "GET", "/api/links", solisKey, ""); code != http.StatusOK || len(list["links"].([]any)) != 1 {
		t.Errorf("list: %d %v", code, list)
	}

	if code, del := apiCall(t, h, "DELETE", "/api/links/my-item", solisKey, ""); code != http.StatusOK || del["deleted"] != true {
		t.Errorf("delete: %d %v", code, del)
	}
	if code, _ := apiCall(t, h, "GET", "/api/links/my-item", solisKey, ""); code != http.StatusNotFound {
		t.Errorf("get after delete: %d, want 404", code)
	}
}

// An application key reaches only the links it created. Everything else looks
// like it does not exist; the administrator's key reaches all of it.
func TestAppKeyCannotTouchOtherCreatorsLinks(t *testing.T) {
	s, h := appAPIServer(t)
	if _, err := s.db.Create("mine", "https://admin.example/private", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Create("anon", "https://anon.example/", "anonymous"); err != nil {
		t.Fatal(err)
	}
	_, other := apiCall(t, h, "POST", "/api/links", otherKey, `{"url":"https://other.example/x"}`)
	otherSlug := other["slug"].(string)

	for _, slug := range []string{"mine", "anon", otherSlug} {
		if code, body := apiCall(t, h, "GET", "/api/links/"+slug, solisKey, ""); code != http.StatusNotFound || body["url"] != nil {
			t.Errorf("GET %s with another app's key: %d %v, want a plain 404", slug, code, body)
		}
		if code, _ := apiCall(t, h, "PATCH", "/api/links/"+slug, solisKey, `{"url":"https://evil.example/"}`); code != http.StatusNotFound {
			t.Errorf("PATCH %s: %d, want 404", slug, code)
		}
		if code, _ := apiCall(t, h, "DELETE", "/api/links/"+slug, solisKey, ""); code != http.StatusNotFound {
			t.Errorf("DELETE %s: %d, want 404", slug, code)
		}
	}
	// Nothing was changed or removed.
	if l, err := s.db.GetBySlug("mine"); err != nil || l.URL != "https://admin.example/private" {
		t.Errorf("admin link was modified: %+v %v", l, err)
	}
	if _, err := s.db.GetBySlug(otherSlug); err != nil {
		t.Errorf("other app's link was deleted: %v", err)
	}

	// An existing link to the same URL by someone else is not handed over.
	code, dup := apiCall(t, h, "POST", "/api/links", solisKey, `{"url":"https://admin.example/private"}`)
	if code != http.StatusCreated || dup["slug"] == "mine" || dup["created_by"] != "api:solis" {
		t.Errorf("create for a URL the admin already shortened: %d %v", code, dup)
	}
	// Taking a slug that exists is a conflict, whoever owns it.
	if code, _ := apiCall(t, h, "POST", "/api/links", solisKey, `{"url":"https://x.example/","slug":"mine"}`); code != http.StatusConflict {
		t.Errorf("claiming a taken slug: %d, want 409", code)
	}
	if code, _ := apiCall(t, h, "PATCH", "/api/links/"+dup["slug"].(string), solisKey, `{"slug":"mine"}`); code != http.StatusConflict {
		t.Errorf("renaming onto a taken slug: %d, want 409", code)
	}

	if code, list := apiCall(t, h, "GET", "/api/links", solisKey, ""); code != http.StatusOK || len(list["links"].([]any)) != 1 {
		t.Errorf("app list should hold only its own link: %d %v", code, list)
	}
	if code, list := apiCall(t, h, "GET", "/api/links", adminKey, ""); code != http.StatusOK || len(list["links"].([]any)) != 4 {
		t.Errorf("admin list should hold every link: %d %v", code, list)
	}
	if code, got := apiCall(t, h, "GET", "/api/links/"+otherSlug, adminKey, ""); code != http.StatusOK || got["created_by"] != "api:other" {
		t.Errorf("admin get of an app link: %d %v", code, got)
	}
}

func TestAPIRejectsBadInputAndKeys(t *testing.T) {
	s, h := appAPIServer(t)
	_, created := apiCall(t, h, "POST", "/api/links", solisKey, `{"url":"https://solis.example/a"}`)
	slug := created["slug"].(string)

	for name, c := range map[string]struct {
		method, path, key, body string
		want                    int
	}{
		"no key":             {"GET", "/api/links/" + slug, "", "", http.StatusUnauthorized},
		"wrong key":          {"GET", "/api/links/" + slug, "nope-nope-nope-nope-nope-nope", "", http.StatusUnauthorized},
		"empty patch":        {"PATCH", "/api/links/" + slug, solisKey, `{}`, http.StatusBadRequest},
		"javascript url":     {"PATCH", "/api/links/" + slug, solisKey, `{"url":"javascript:alert(1)"}`, http.StatusBadRequest},
		"reserved slug":      {"PATCH", "/api/links/" + slug, solisKey, `{"slug":"admin"}`, http.StatusBadRequest},
		"unknown field":      {"PATCH", "/api/links/" + slug, solisKey, `{"created_by":"admin"}`, http.StatusBadRequest},
		"malformed json":     {"PATCH", "/api/links/" + slug, solisKey, `{`, http.StatusBadRequest},
		"create without url": {"POST", "/api/links", solisKey, `{}`, http.StatusBadRequest},
	} {
		if code, body := apiCall(t, h, c.method, c.path, c.key, c.body); code != c.want {
			t.Errorf("%s: got %d %v, want %d", name, code, body, c.want)
		}
	}
	if l, _ := s.db.GetBySlug(slug); l == nil || l.URL != "https://solis.example/a" || l.CreatedBy != "api:solis" {
		t.Errorf("link changed by rejected requests: %+v", l)
	}
}

// Wrong keys are throttled per client address; a correct key never is.
func TestAPIWrongKeysAreThrottled(t *testing.T) {
	s, h := appAPIServer(t)
	s.authLimiter = newRateLimiter(time.Minute)

	if code, _ := apiCall(t, h, "GET", "/api/links", "wrong-wrong-wrong-wrong-wrong", ""); code != http.StatusUnauthorized {
		t.Fatalf("first wrong key: %d, want 401", code)
	}
	if code, _ := apiCall(t, h, "GET", "/api/links", solisKey, ""); code != http.StatusTooManyRequests {
		t.Errorf("request right after a wrong key from the same address: %d, want 429", code)
	}

	s.authLimiter = newRateLimiter(time.Minute)
	for i := range 5 {
		if code, _ := apiCall(t, h, "GET", "/api/links", solisKey, ""); code != http.StatusOK {
			t.Errorf("valid key, request %d: %d, want 200", i+1, code)
		}
	}
	// A request with no key at all is refused without starting the window.
	if code, _ := apiCall(t, h, "GET", "/api/links", "", ""); code != http.StatusUnauthorized {
		t.Errorf("no key: %d, want 401", code)
	}
	if code, _ := apiCall(t, h, "GET", "/api/links", solisKey, ""); code != http.StatusOK {
		t.Errorf("valid key after an unauthenticated request: %d, want 200", code)
	}
}

// The API is reachable only when some key can authenticate.
func TestAPIEnabledOnlyWithAKey(t *testing.T) {
	s := apiTestServer(t, "")
	if s.apiEnabled() {
		t.Error("no keys configured: API must be off")
	}
	s.cfg.API.Keys = []config.APIKey{{Name: "solis", Key: solisKey}}
	if !s.apiEnabled() {
		t.Error("an application key should enable the API")
	}
	s.cfg.API.Keys = nil
	s.cfg.MCP.APIKey = adminKey
	s.cfg.MCP.Enabled = false
	if s.apiEnabled() {
		t.Error("the mcp key only counts while mcp is enabled")
	}
	if _, ok := s.apiPrincipalFor(adminKey); ok {
		t.Error("the mcp key must not authenticate while mcp is disabled")
	}
}
