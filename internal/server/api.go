package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"glimmer/internal/db"
	"glimmer/internal/slug"
)

// The REST link API (/api/links) lets agents and other applications create,
// read, change and delete short links.
//
// Two kinds of key can use it:
//   - the administrator's key, [mcp] api_key, which reaches every link;
//   - application keys from [[api.keys]], each of which only sees and changes
//     the links it created itself (recorded with creator "api:<name>").
//
// To an application, another creator's link looks exactly like a link that
// does not exist.

// apiPrincipal is who an API request is acting as.
type apiPrincipal struct {
	Name  string
	Admin bool
}

// creator is the created_by value for links this principal makes.
func (p apiPrincipal) creator() string {
	if p.Admin {
		return "admin"
	}
	return "api:" + p.Name
}

func (p apiPrincipal) owns(link *db.Link) bool {
	return p.Admin || link.CreatedBy == p.creator()
}

// apiEnabled reports whether any key can authenticate against the REST API.
func (s *Server) apiEnabled() bool {
	return (s.cfg.MCP.Enabled && s.cfg.MCP.APIKey != "") || len(s.cfg.API.Keys) > 0
}

// apiPrincipalFor resolves a presented key, comparing in constant time
// against every configured key so timing does not reveal which one matched.
func (s *Server) apiPrincipalFor(token string) (apiPrincipal, bool) {
	var p apiPrincipal
	found := false
	if s.cfg.MCP.Enabled && s.cfg.MCP.APIKey != "" && constantTimeEqual(token, s.cfg.MCP.APIKey) {
		p, found = apiPrincipal{Name: "admin", Admin: true}, true
	}
	for _, k := range s.cfg.API.Keys {
		if k.Key != "" && constantTimeEqual(token, k.Key) && !found {
			p, found = apiPrincipal{Name: k.Name}, true
		}
	}
	return p, found
}

// apiKeyAuth reports whether the request carries the administrator's key.
// Kept for the MCP endpoint's callers and as a defence-in-depth helper.
func (s *Server) apiKeyAuth(r *http.Request) bool {
	p, ok := s.apiAuthenticate(r)
	return ok && p.Admin
}

// apiAuthenticate reads the key from "Authorization: Bearer" or "X-API-Key".
func (s *Server) apiAuthenticate(r *http.Request) (apiPrincipal, bool) {
	for _, token := range []string{bearerToken(r.Header.Get("Authorization")), r.Header.Get("X-API-Key")} {
		if token == "" {
			continue
		}
		if p, ok := s.apiPrincipalFor(token); ok {
			return p, true
		}
	}
	return apiPrincipal{}, false
}

// apiAuthorize authenticates an API request or writes the refusal. Wrong keys
// are throttled per client IP and logged; requests without any key just get
// the 401 challenge.
func (s *Server) apiAuthorize(w http.ResponseWriter, r *http.Request) (apiPrincipal, bool) {
	if r.Header.Get("Authorization") == "" && r.Header.Get("X-API-Key") == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Glimmer API"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return apiPrincipal{}, false
	}
	var p apiPrincipal
	ip := s.clientIP(r)
	ok, limited := s.authLimiter.Guard(ip, func() bool {
		var found bool
		p, found = s.apiAuthenticate(r)
		return found
	})
	if limited {
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusTooManyRequests, "too many failed attempts")
		return apiPrincipal{}, false
	}
	if !ok {
		log.Printf("api auth FAILED from %s", ip)
		w.Header().Set("WWW-Authenticate", `Bearer realm="Glimmer API"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return apiPrincipal{}, false
	}
	return p, true
}

// apiCreateLinkRequest is the body of POST /api/links.
type apiCreateLinkRequest struct {
	URL  string `json:"url"`
	Slug string `json:"slug,omitempty"`
}

// apiUpdateLinkRequest is the body of PATCH /api/links/{slug}. Omitted fields
// are left unchanged.
type apiUpdateLinkRequest struct {
	URL  *string `json:"url,omitempty"`
	Slug *string `json:"slug,omitempty"`
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

// decodeAPIBody reads a small JSON body, refusing unknown fields.
func decodeAPIBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	// JSON bodies are tiny; 4 KB is plenty and stops a misbehaving client
	// from streaming a large body.
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// apiOwnedLink loads the link named in the path if the caller may touch it.
// Unknown links and other creators' links both yield the same 404.
func (s *Server) apiOwnedLink(w http.ResponseWriter, r *http.Request, p apiPrincipal) (*db.Link, bool) {
	link, err := s.db.GetBySlug(strings.ToLower(r.PathValue("slug")))
	if err == nil && p.owns(link) {
		return link, true
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, http.StatusInternalServerError, "database error")
		return nil, false
	}
	writeJSONError(w, http.StatusNotFound, "link not found")
	return nil, false
}

// apiCreateLink handles POST /api/links: validate the URL, honour an optional
// custom slug, reuse the caller's existing link for the same URL, and return
// the link.
func (s *Server) apiCreateLink(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiAuthorize(w, r)
	if !ok {
		return
	}
	var req apiCreateLinkRequest
	if !decodeAPIBody(w, r, &req) {
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
		// Deduplicate identical URLs so clients that retry do not pile up
		// links. The administrator's key reuses any existing link (as the web
		// UI does); an application only ever gets back a link of its own.
		var existing *db.Link
		var err error
		if p.Admin {
			existing, err = s.db.GetByURL(rawURL)
		} else {
			existing, err = s.db.GetByURLAndCreator(rawURL, p.creator())
		}
		if err == nil {
			writeJSON(w, http.StatusOK, s.toAPILink(*existing))
			return
		}

		finalSlug, err = s.generateUniqueSlug()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	link, err := s.db.Create(finalSlug, rawURL, p.creator())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create link")
		return
	}
	writeJSON(w, http.StatusCreated, s.toAPILink(*link))
}

// apiListLinks handles GET /api/links: the caller's links, newest first
// (every link for the administrator's key), capped at 1000.
func (s *Server) apiListLinks(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiAuthorize(w, r)
	if !ok {
		return
	}
	var links []db.Link
	var err error
	if p.Admin {
		links, err = s.db.ListRecent(1000)
	} else {
		links, err = s.db.ListRecentByCreator(p.creator(), 1000)
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}
	out := make([]apiLinkResponse, 0, len(links))
	for _, l := range links {
		out = append(out, s.toAPILink(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// apiGetLink handles GET /api/links/{slug}.
func (s *Server) apiGetLink(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiAuthorize(w, r)
	if !ok {
		return
	}
	link, ok := s.apiOwnedLink(w, r, p)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toAPILink(*link))
}

// apiUpdateLink handles PATCH /api/links/{slug}: change the destination URL
// and/or the slug.
func (s *Server) apiUpdateLink(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiAuthorize(w, r)
	if !ok {
		return
	}
	link, ok := s.apiOwnedLink(w, r, p)
	if !ok {
		return
	}
	var req apiUpdateLinkRequest
	if !decodeAPIBody(w, r, &req) {
		return
	}
	if req.URL == nil && req.Slug == nil {
		writeJSONError(w, http.StatusBadRequest, "nothing to update: provide url and/or slug")
		return
	}

	newURL, newSlug := link.URL, link.Slug
	if req.URL != nil {
		newURL = strings.TrimSpace(*req.URL)
		if err := validateRedirectURL(&newURL); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Slug != nil {
		newSlug = strings.ToLower(strings.TrimSpace(*req.Slug))
		if err := validateAPISlug(newSlug); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if newSlug != link.Slug {
			exists, err := s.db.SlugExists(newSlug)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "database error")
				return
			}
			if exists {
				writeJSONError(w, http.StatusConflict, "slug already taken")
				return
			}
		}
	}

	if err := s.db.Update(link.ID, newSlug, newURL); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to update link")
		return
	}
	updated, err := s.db.GetByID(link.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, s.toAPILink(*updated))
}

// apiDeleteLink handles DELETE /api/links/{slug}.
func (s *Server) apiDeleteLink(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiAuthorize(w, r)
	if !ok {
		return
	}
	link, ok := s.apiOwnedLink(w, r, p)
	if !ok {
		return
	}
	if err := s.db.Delete(link.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to delete link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "slug": link.Slug})
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
