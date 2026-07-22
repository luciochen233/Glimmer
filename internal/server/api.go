package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"glimmer/internal/db"
	"glimmer/internal/slug"
)

// apiKeyAuth checks the configured MCP API key via constant-time comparison.
// Reuses the same authentication scheme as POST /mcp so operators only need
// to provision one secret. The route is registered only when [mcp].enabled
// is true and an api_key is configured, but we still refuse empty keys as
// defence in depth.
func (s *Server) apiKeyAuth(r *http.Request) bool {
	if s.cfg.MCP.APIKey == "" {
		return false
	}
	if token := bearerToken(r.Header.Get("Authorization")); token != "" && constantTimeEqual(token, s.cfg.MCP.APIKey) {
		return true
	}
	if token := r.Header.Get("X-API-Key"); token != "" && constantTimeEqual(token, s.cfg.MCP.APIKey) {
		return true
	}
	return false
}

// apiCreateLinkRequest is the body of POST /api/links.
type apiCreateLinkRequest struct {
	URL  string `json:"url"`
	Slug string `json:"slug,omitempty"`
}

// apiLinkResponse is the REST view of a short link. Field names mirror the
// existing MCP payload (snake_case) so anyone already consuming mcpLink can
// reuse the same parsing code on the REST side.
type apiLinkResponse struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	ShortURL  string `json:"short_url"`
	CreatedBy string `json:"created_by"`
	Clicks    int64  `json:"clicks"`
	CreatedAt string `json:"created_at,omitempty"`
}

func (s *Server) toAPILink(link db.Link) apiLinkResponse {
	return apiLinkResponse{
		ID:        link.ID,
		Slug:      link.Slug,
		URL:       link.URL,
		ShortURL:  joinBaseURL(s.baseURL(), "/"+link.Slug),
		CreatedBy: link.CreatedBy,
		Clicks:    link.Clicks,
		CreatedAt: formatMCPTime(link.CreatedAt),
	}
}

// apiCreateLink is the REST equivalent of the MCP create_link tool. It
// accepts a JSON body, validates the URL, optionally honours a custom
// slug, deduplicates identical URLs, and returns the created link.
func (s *Server) apiCreateLink(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeyAuth(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Glimmer API"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// JSON bodies are tiny; 4 KB is plenty and stops a misbehaving client
	// from streaming a large body just to push us against rate limits.
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()

	var req apiCreateLinkRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	rawURL := strings.TrimSpace(req.URL)
	if err := validateRedirectURL(&rawURL); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	finalSlug := strings.ToLower(strings.TrimSpace(req.Slug))
	if finalSlug != "" {
		if err := validateAPISlug(finalSlug); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		exists, err := s.db.SlugExists(finalSlug)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "database error")
			return
		}
		if exists {
			writeJSONError(w, http.StatusConflict, "slug already taken")
			return
		}
	} else {
		// Deduplicate identical URLs — matches the public web UI behaviour
		// and keeps the link count down for API clients that retry.
		if existing, err := s.db.GetByURL(rawURL); err == nil {
			writeJSON(w, http.StatusOK, s.toAPILink(*existing))
			return
		}

		var err error
		finalSlug, err = s.generateUniqueSlug()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	link, err := s.db.Create(finalSlug, rawURL, "admin")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create link")
		return
	}

	writeJSON(w, http.StatusCreated, s.toAPILink(*link))
}

// validateAPISlug enforces the same character rules the admin web UI uses:
// letters, digits, hyphens, and underscores, 1–64 characters. Centralised
// so future rule changes only need one edit.
func validateAPISlug(value string) error {
	if value == "" {
		return errors.New("slug is required")
	}
	if len(value) > 64 {
		return errors.New("slug is too long (max 64 characters)")
	}
	return slug.Validate(value)
}
