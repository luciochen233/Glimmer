package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"glimmer/internal/db"
	"glimmer/internal/slug"

	"golang.org/x/crypto/bcrypt"
)

const mcpProtocolVersion = "2025-06-18"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !s.validMCPOrigin(r) {
		writeMCPHTTPError(w, nil, http.StatusForbidden, -32000, "Forbidden origin")
		return
	}
	if !s.authorizeMCP(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Glimmer MCP", Basic realm="Glimmer MCP"`)
		writeMCPHTTPError(w, nil, http.StatusUnauthorized, -32000, "Unauthorized")
		return
	}

	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeMCPHTTPError(w, nil, http.StatusBadRequest, -32700, "Parse error")
		return
	}
	if req.JSONRPC != "2.0" {
		writeMCPJSON(w, http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32600, Message: "Invalid Request"},
		})
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeMCPResult(w, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "glimmer",
				"version": "0.1.0",
			},
		})
	case "tools/list":
		writeMCPResult(w, req.ID, map[string]any{"tools": mcpTools()})
	case "tools/call":
		s.handleMCPToolCall(w, req)
	default:
		writeMCPJSON(w, http.StatusNotFound, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: "Method not found"},
		})
	}
}

func (s *Server) authorizeMCP(r *http.Request) bool {
	if s.cfg.MCP.APIKey != "" {
		if token := bearerToken(r.Header.Get("Authorization")); token != "" && constantTimeEqual(token, s.cfg.MCP.APIKey) {
			return true
		}
		if token := r.Header.Get("X-API-Key"); token != "" && constantTimeEqual(token, s.cfg.MCP.APIKey) {
			return true
		}
	}

	username, password, ok := r.BasicAuth()
	if !ok || username != s.cfg.Admin.Username || s.cfg.Admin.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(s.cfg.Admin.PasswordHash), []byte(password)) == nil
}

func (s *Server) validMCPOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	baseURL, err := url.Parse(s.cfg.Server.BaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return false
	}
	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(originURL.Scheme, baseURL.Scheme) && strings.EqualFold(originURL.Host, baseURL.Host)
}

func (s *Server) handleMCPToolCall(w http.ResponseWriter, req mcpRequest) {
	var params mcpToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeMCPError(w, req.ID, -32602, "Invalid tool call parameters")
		return
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}

	var result any
	var err error
	switch params.Name {
	case "list_links":
		result, err = s.mcpListLinks(params.Arguments)
	case "get_link":
		result, err = s.mcpGetLink(params.Arguments)
	case "create_link":
		result, err = s.mcpCreateLink(params.Arguments)
	case "update_link":
		result, err = s.mcpUpdateLink(params.Arguments)
	case "delete_link":
		result, err = s.mcpDeleteLink(params.Arguments)
	case "list_pastes":
		result, err = s.mcpListPastes(params.Arguments)
	case "get_paste":
		result, err = s.mcpGetPaste(params.Arguments)
	case "create_paste":
		result, err = s.mcpCreatePaste(params.Arguments)
	case "update_paste":
		result, err = s.mcpUpdatePaste(params.Arguments)
	case "delete_paste":
		result, err = s.mcpDeletePaste(params.Arguments)
	case "list_uploads":
		result, err = s.mcpListUploads(params.Arguments)
	case "delete_upload":
		result, err = s.mcpDeleteUpload(params.Arguments)
	default:
		writeMCPError(w, req.ID, -32602, "Unknown tool: "+params.Name)
		return
	}

	if err != nil {
		writeMCPResult(w, req.ID, map[string]any{
			"content": []map[string]string{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return
	}
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		writeMCPError(w, req.ID, -32603, "Failed to encode tool result")
		return
	}
	writeMCPResult(w, req.ID, map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(payload)}},
		"isError": false,
	})
}

func (s *Server) mcpListLinks(args map[string]any) (any, error) {
	createdBy := strings.TrimSpace(stringArg(args, "created_by", ""))
	var links []db.Link
	var err error
	if createdBy != "" {
		if createdBy != "admin" && createdBy != "anonymous" {
			return nil, fmt.Errorf("created_by must be admin or anonymous")
		}
		links, err = s.db.ListByCreator(createdBy)
	} else {
		links, err = s.db.List()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list links: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(stringArg(args, "query", "")))
	response := make([]mcpLink, 0, len(links))
	for _, link := range links {
		converted := s.toMCPLink(link)
		if query != "" && !mcpLinkMatches(converted, query) {
			continue
		}
		response = append(response, converted)
	}
	return response, nil
}

func (s *Server) mcpGetLink(args map[string]any) (any, error) {
	if id, ok := int64Arg(args, "id"); ok {
		link, err := s.db.GetByID(id)
		if err != nil {
			return nil, notFoundOrWrapped("link", err)
		}
		return s.toMCPLink(*link), nil
	}
	sl := strings.TrimSpace(stringArg(args, "slug", ""))
	if sl == "" {
		return nil, fmt.Errorf("id or slug is required")
	}
	link, err := s.db.GetBySlug(strings.ToLower(sl))
	if err != nil {
		return nil, notFoundOrWrapped("link", err)
	}
	return s.toMCPLink(*link), nil
}

func (s *Server) mcpCreateLink(args map[string]any) (any, error) {
	rawURL := strings.TrimSpace(stringArg(args, "url", ""))
	if err := validateRedirectURL(&rawURL); err != nil {
		return nil, err
	}
	finalSlug := strings.TrimSpace(stringArg(args, "slug", ""))
	if finalSlug != "" {
		finalSlug = strings.ToLower(finalSlug)
		if err := slug.Validate(finalSlug); err != nil {
			return nil, err
		}
		exists, err := s.db.SlugExists(finalSlug)
		if err != nil {
			return nil, fmt.Errorf("failed to check slug: %w", err)
		}
		if exists {
			return nil, fmt.Errorf("slug %q is already taken", finalSlug)
		}
	} else {
		var err error
		finalSlug, err = s.generateUniqueSlug()
		if err != nil {
			return nil, err
		}
	}
	createdBy := strings.TrimSpace(stringArg(args, "created_by", "admin"))
	if createdBy == "" {
		createdBy = "admin"
	}
	if createdBy != "admin" && createdBy != "anonymous" {
		return nil, fmt.Errorf("created_by must be admin or anonymous")
	}
	link, err := s.db.Create(finalSlug, rawURL, createdBy)
	if err != nil {
		return nil, fmt.Errorf("failed to create link: %w", err)
	}
	return s.toMCPLink(*link), nil
}

func (s *Server) mcpUpdateLink(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok {
		return nil, fmt.Errorf("id is required")
	}
	link, err := s.db.GetByID(id)
	if err != nil {
		return nil, notFoundOrWrapped("link", err)
	}
	newSlug := strings.TrimSpace(stringArg(args, "slug", link.Slug))
	newURL := strings.TrimSpace(stringArg(args, "url", link.URL))
	if newSlug == "" {
		return nil, fmt.Errorf("slug is required")
	}
	newSlug = strings.ToLower(newSlug)
	if err := slug.Validate(newSlug); err != nil {
		return nil, err
	}
	if err := validateRedirectURL(&newURL); err != nil {
		return nil, err
	}
	if err := s.db.Update(id, newSlug, newURL); err != nil {
		return nil, fmt.Errorf("failed to update link: %w", err)
	}
	updated, err := s.db.GetByID(id)
	if err != nil {
		return nil, notFoundOrWrapped("link", err)
	}
	return s.toMCPLink(*updated), nil
}

func (s *Server) mcpDeleteLink(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok {
		return nil, fmt.Errorf("id is required")
	}
	if _, err := s.db.GetByID(id); err != nil {
		return nil, notFoundOrWrapped("link", err)
	}
	if err := s.db.Delete(id); err != nil {
		return nil, fmt.Errorf("failed to delete link: %w", err)
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (s *Server) mcpListPastes(args map[string]any) (any, error) {
	pastes, err := s.db.ListPastes()
	if err != nil {
		return nil, fmt.Errorf("failed to list pastes: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(stringArg(args, "query", "")))
	includeContent := boolArg(args, "include_content", false)
	response := make([]mcpPaste, 0, len(pastes))
	for _, paste := range pastes {
		converted := s.toMCPPaste(paste, includeContent)
		if query != "" && !mcpPasteMatches(converted, query) {
			continue
		}
		response = append(response, converted)
	}
	return response, nil
}

func (s *Server) mcpGetPaste(args map[string]any) (any, error) {
	if id, ok := int64Arg(args, "id"); ok {
		paste, err := s.db.GetPasteByID(id)
		if err != nil {
			return nil, notFoundOrWrapped("paste", err)
		}
		return s.toMCPPaste(*paste, true), nil
	}
	name := strings.TrimSpace(stringArg(args, "name", ""))
	if name == "" {
		return nil, fmt.Errorf("id or name is required")
	}
	paste, err := s.db.GetPasteByName(name)
	if err != nil {
		return nil, notFoundOrWrapped("paste", err)
	}
	return s.toMCPPaste(*paste, true), nil
}

func (s *Server) mcpCreatePaste(args map[string]any) (any, error) {
	name := strings.TrimSpace(stringArg(args, "name", ""))
	title := strings.TrimSpace(stringArg(args, "title", ""))
	content := stringArg(args, "content", "")
	format := normalizePasteFormat(stringArg(args, "format", "markdown"))
	if err := validatePasteWrite(name, content); err != nil {
		return nil, err
	}
	exists, err := s.db.PasteNameExists(name)
	if err != nil {
		return nil, fmt.Errorf("failed to check paste name: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("paste name %q is already taken", name)
	}
	token := strings.TrimSpace(stringArg(args, "token", ""))
	if token == "" && boolArg(args, "generate_token", false) {
		token = slug.Generate(12)
	}
	if token != "" && !validPasteName(token) {
		return nil, fmt.Errorf("token must be alphanumeric with hyphens/underscores only")
	}
	paste, err := s.db.CreatePaste(name, title, content, format, token, boolArg(args, "hidden", false))
	if err != nil {
		return nil, fmt.Errorf("failed to create paste: %w", err)
	}
	return s.toMCPPaste(*paste, true), nil
}

func (s *Server) mcpUpdatePaste(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok {
		return nil, fmt.Errorf("id is required")
	}
	paste, err := s.db.GetPasteByID(id)
	if err != nil {
		return nil, notFoundOrWrapped("paste", err)
	}
	name := strings.TrimSpace(stringArg(args, "name", paste.Name))
	title := strings.TrimSpace(stringArg(args, "title", paste.Title))
	content := stringArg(args, "content", paste.Content)
	format := normalizePasteFormat(stringArg(args, "format", paste.Format))
	hidden := boolArg(args, "hidden", paste.Hidden)
	token := paste.Token
	if raw, ok := args["token"]; ok {
		if raw == nil {
			token = ""
		} else if value, ok := raw.(string); ok {
			token = strings.TrimSpace(value)
		} else {
			return nil, fmt.Errorf("token must be a string or null")
		}
	}
	if boolArg(args, "generate_token", false) {
		token = slug.Generate(12)
	}
	if err := validatePasteWrite(name, content); err != nil {
		return nil, err
	}
	if token != "" && !validPasteName(token) {
		return nil, fmt.Errorf("token must be alphanumeric with hyphens/underscores only")
	}
	if err := s.db.UpdatePaste(id, name, title, content, format, token, hidden); err != nil {
		return nil, fmt.Errorf("failed to update paste: %w", err)
	}
	updated, err := s.db.GetPasteByID(id)
	if err != nil {
		return nil, notFoundOrWrapped("paste", err)
	}
	return s.toMCPPaste(*updated, true), nil
}

func (s *Server) mcpDeletePaste(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok {
		return nil, fmt.Errorf("id is required")
	}
	if _, err := s.db.GetPasteByID(id); err != nil {
		return nil, notFoundOrWrapped("paste", err)
	}
	if err := s.db.DeletePaste(id); err != nil {
		return nil, fmt.Errorf("failed to delete paste: %w", err)
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (s *Server) mcpListUploads(args map[string]any) (any, error) {
	entries, err := os.ReadDir(s.cfg.Upload.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []mcpUpload{}, nil
		}
		return nil, fmt.Errorf("failed to list upload directory: %w", err)
	}
	names, _ := s.db.ListUploadNames()
	query := strings.ToLower(strings.TrimSpace(stringArg(args, "query", "")))
	response := make([]mcpUpload, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !validUploadFilename(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		upload := mcpUpload{
			Filename:     entry.Name(),
			OriginalName: names[entry.Name()],
			URL:          joinBaseURL(s.cfg.Server.BaseURL, "/uploads/"+entry.Name()),
			Size:         info.Size(),
			SizeHuman:    formatBytes(info.Size()),
			Ext:          strings.TrimPrefix(strings.ToLower(filepath.Ext(entry.Name())), "."),
			IsImage:      isImageFile(entry.Name()),
			CreatedAt:    formatMCPTime(info.ModTime()),
		}
		if query != "" && !strings.Contains(strings.ToLower(upload.Filename+" "+upload.OriginalName+" "+upload.Ext), query) {
			continue
		}
		response = append(response, upload)
	}
	return response, nil
}

func (s *Server) mcpDeleteUpload(args map[string]any) (any, error) {
	filename := strings.TrimSpace(stringArg(args, "filename", ""))
	if !validUploadFilename(filename) {
		return nil, fmt.Errorf("invalid upload filename")
	}
	path := filepath.Join(s.cfg.Upload.Dir, filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to delete upload: %w", err)
	}
	if err := s.db.DeleteUpload(filename); err != nil {
		return nil, fmt.Errorf("failed to delete upload metadata: %w", err)
	}
	return map[string]any{"deleted": true, "filename": filename}, nil
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_links",
			"title":       "List links",
			"description": "List Glimmer short links. Optional filters: query and created_by.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"query":      map[string]any{"type": "string", "description": "Case-insensitive search over slug, URL, creator, and short URL."},
				"created_by": map[string]any{"type": "string", "enum": []string{"admin", "anonymous"}},
			}},
		},
		{
			"name":        "get_link",
			"title":       "Get link",
			"description": "Get a short link by id or slug.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"id":   map[string]any{"type": "integer"},
				"slug": map[string]any{"type": "string"},
			}},
		},
		{"name": "create_link", "title": "Create link", "description": "Create a short link. Omit slug to generate one.", "inputSchema": linkWriteSchema(true)},
		{"name": "update_link", "title": "Update link", "description": "Patch a short link by id. Omitted fields are left unchanged.", "inputSchema": linkWriteSchema(false)},
		{
			"name":        "delete_link",
			"title":       "Delete link",
			"description": "Delete a short link by id.",
			"inputSchema": map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{
				"id": map[string]any{"type": "integer"},
			}},
		},
		{
			"name":        "list_pastes",
			"title":       "List pastes",
			"description": "List Glimmer pastes. Content is omitted unless include_content is true.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"query":           map[string]any{"type": "string", "description": "Case-insensitive search over paste name, title, format, and optionally content."},
				"include_content": map[string]any{"type": "boolean"},
			}},
		},
		{
			"name":        "get_paste",
			"title":       "Get paste",
			"description": "Get a paste by id or name, including content and token.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"id":   map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"},
			}},
		},
		{"name": "create_paste", "title": "Create paste", "description": "Create a paste. Use generate_token or token for protected pastes.", "inputSchema": pasteWriteSchema(true)},
		{"name": "update_paste", "title": "Update paste", "description": "Patch a paste by id. Omitted fields are left unchanged. Set token to null to remove it.", "inputSchema": pasteWriteSchema(false)},
		{
			"name":        "delete_paste",
			"title":       "Delete paste",
			"description": "Delete a paste by id.",
			"inputSchema": map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{
				"id": map[string]any{"type": "integer"},
			}},
		},
		{
			"name":        "list_uploads",
			"title":       "List uploads",
			"description": "List uploaded files and image metadata known to Glimmer.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Case-insensitive search over stored filename, original filename, and extension."},
			}},
		},
		{
			"name":        "delete_upload",
			"title":       "Delete upload",
			"description": "Delete an uploaded file by its stored filename.",
			"inputSchema": map[string]any{"type": "object", "required": []string{"filename"}, "properties": map[string]any{
				"filename": map[string]any{"type": "string", "description": "Stored filename such as 32 hex characters plus extension."},
			}},
		},
	}
}

func linkWriteSchema(create bool) map[string]any {
	required := []string{}
	if create {
		required = append(required, "url")
	} else {
		required = append(required, "id")
	}
	return map[string]any{
		"type":     "object",
		"required": required,
		"properties": map[string]any{
			"id":         map[string]any{"type": "integer"},
			"url":        map[string]any{"type": "string", "description": "HTTP or HTTPS URL."},
			"slug":       map[string]any{"type": "string", "description": "Optional custom slug. Letters, numbers, hyphens, and underscores only."},
			"created_by": map[string]any{"type": "string", "enum": []string{"admin", "anonymous"}, "description": "Defaults to admin."},
		},
	}
}

func pasteWriteSchema(create bool) map[string]any {
	required := []string{}
	if create {
		required = append(required, "name", "content")
	} else {
		required = append(required, "id")
	}
	return map[string]any{
		"type":     "object",
		"required": required,
		"properties": map[string]any{
			"id":             map[string]any{"type": "integer"},
			"name":           map[string]any{"type": "string", "description": "Letters, numbers, hyphens, and underscores only."},
			"title":          map[string]any{"type": "string"},
			"content":        map[string]any{"type": "string", "description": "Max 512 KB."},
			"format":         map[string]any{"type": "string", "enum": []string{"markdown", "text"}},
			"token":          map[string]any{"type": []string{"string", "null"}, "description": "Access token. Set null on update to remove."},
			"generate_token": map[string]any{"type": "boolean"},
			"hidden":         map[string]any{"type": "boolean", "description": "Return 404 instead of a token prompt when protected."},
		},
	}
}

type mcpLink struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	ShortURL  string `json:"short_url"`
	CreatedBy string `json:"created_by"`
	Clicks    int64  `json:"clicks"`
	CreatedAt string `json:"created_at,omitempty"`
}

type mcpPaste struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	Format    string `json:"format"`
	Token     string `json:"token,omitempty"`
	Hidden    bool   `json:"hidden"`
	PublicURL string `json:"public_url"`
	TokenURL  string `json:"token_url,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type mcpUpload struct {
	Filename     string `json:"filename"`
	OriginalName string `json:"original_name,omitempty"`
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	SizeHuman    string `json:"size_human"`
	Ext          string `json:"ext"`
	IsImage      bool   `json:"is_image"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func (s *Server) toMCPLink(link db.Link) mcpLink {
	return mcpLink{
		ID:        link.ID,
		Slug:      link.Slug,
		URL:       link.URL,
		ShortURL:  joinBaseURL(s.cfg.Server.BaseURL, "/"+link.Slug),
		CreatedBy: link.CreatedBy,
		Clicks:    link.Clicks,
		CreatedAt: formatMCPTime(link.CreatedAt),
	}
}

func (s *Server) toMCPPaste(paste db.Paste, includeContent bool) mcpPaste {
	response := mcpPaste{
		ID:        paste.ID,
		Name:      paste.Name,
		Title:     paste.Title,
		Format:    paste.Format,
		Token:     paste.Token,
		Hidden:    paste.Hidden,
		PublicURL: joinBaseURL(s.cfg.Server.BaseURL, "/bin/"+paste.Name),
		CreatedAt: formatMCPTime(paste.CreatedAt),
		UpdatedAt: formatMCPTime(paste.UpdatedAt),
	}
	if includeContent {
		response.Content = paste.Content
	}
	if paste.Token != "" {
		response.TokenURL = joinBaseURL(s.cfg.Server.BaseURL, "/bin/"+paste.Name+"/"+paste.Token)
	}
	return response
}

func (s *Server) generateUniqueSlug() (string, error) {
	for range 10 {
		candidate := slug.Generate(s.cfg.Slugs.Length)
		exists, err := s.db.SlugExists(candidate)
		if err != nil {
			return "", fmt.Errorf("failed to check slug: %w", err)
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique slug")
}

func validateRedirectURL(rawURL *string) error {
	value := strings.TrimSpace(*rawURL)
	if value == "" {
		return fmt.Errorf("url is required")
	}
	if len(value) > 2048 {
		return fmt.Errorf("url is too long (max 2048 characters)")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid URL")
	}
	*rawURL = value
	return nil
}

func validatePasteWrite(name, content string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !validPasteName(name) {
		return fmt.Errorf("name must be alphanumeric with hyphens/underscores only")
	}
	if len(content) > 512*1024 {
		return fmt.Errorf("paste content too large (max 512 KB)")
	}
	return nil
}

func normalizePasteFormat(format string) string {
	if format == "text" {
		return "text"
	}
	return "markdown"
}

func notFoundOrWrapped(kind string, err error) error {
	if err == sql.ErrNoRows {
		return fmt.Errorf("%s not found", kind)
	}
	return fmt.Errorf("failed to get %s: %w", kind, err)
}

func mcpLinkMatches(link mcpLink, query string) bool {
	return strings.Contains(strings.ToLower(strings.Join([]string{
		link.Slug,
		link.URL,
		link.ShortURL,
		link.CreatedBy,
	}, " ")), query)
}

func mcpPasteMatches(paste mcpPaste, query string) bool {
	return strings.Contains(strings.ToLower(strings.Join([]string{
		paste.Name,
		paste.Title,
		paste.Format,
		paste.Content,
	}, " ")), query)
}

func writeMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeMCPJSON(w, http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeMCPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeMCPJSON(w, http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}})
}

func writeMCPHTTPError(w http.ResponseWriter, id json.RawMessage, status int, code int, message string) {
	writeMCPJSON(w, status, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}})
}

func writeMCPJSON(w http.ResponseWriter, status int, response mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	if boolValue, ok := value.(bool); ok {
		return boolValue
	}
	if stringValue, ok := value.(string); ok {
		parsed, err := strconv.ParseBool(stringValue)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func stringArg(args map[string]any, key string, fallback string) string {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	if stringValue, ok := value.(string); ok {
		return stringValue
	}
	return fallback
}

func int64Arg(args map[string]any, key string) (int64, bool) {
	value, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func formatMCPTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func joinBaseURL(baseURL, path string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		return path
	}
	return trimmed + path
}
