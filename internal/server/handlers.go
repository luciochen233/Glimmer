package server

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"glimmer/internal/slug"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"
)

type pageData struct {
	BaseURL     string
	Error       string
	Success     string
	ShortURL    string
	OriginalURL string
	QRSVG       template.HTML
	Links       any
	AdminLinks  any
	AnonLinks   any
	Link        any
	TopLinks    any
	Paste       any
	Pastes      any
	PasteBody   template.HTML
	LoggedIn    bool
	CSRFToken   string
}

var mdRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Table),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}

func makeQRSVG(text string, size int) template.HTML {
	code, err := qr.Encode(strings.ToUpper(text), qr.L)
	if err != nil {
		return ""
	}
	n := code.Size
	pad := 2
	total := n + pad*2
	var paths strings.Builder
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			if code.Black(c, r) {
				fmt.Fprintf(&paths, "M%d,%dh1v1h-1z", c+pad, r+pad)
			}
		}
	}
	svg := fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges"><rect width="100%%" height="100%%" fill="#fff"/><path d="%s" fill="#000"/></svg>`,
		total, total, size, size, paths.String(),
	)
	return template.HTML(svg)
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	fullURL := s.baseURL(r) + "/" + slug
	svg := makeQRSVG(fullURL, 200)
	if svg == "" {
		http.Error(w, "QR generation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write([]byte(svg))
}

func (s *Server) baseURL(r *http.Request) string {
	host := r.Host
	if host == "" {
		return s.cfg.Server.BaseURL
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	return proto + "://" + host
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "index.html", pageData{BaseURL: s.baseURL(r), LoggedIn: s.isLoggedIn(r), CSRFToken: csrfToken(w, r)})
}

func (s *Server) handleShorten(w http.ResponseWriter, r *http.Request) {
	if !s.isLoggedIn(r) {
		ip := clientIP(r)
		if !s.limiter.Allow(ip) {
			s.renderIndex(w, r, "Too many requests. Please wait a moment.", "", "")
			return
		}
	}

	rawURL := strings.TrimSpace(r.FormValue("url"))
	customSlug := strings.TrimSpace(r.FormValue("slug"))

	if rawURL == "" {
		s.renderIndex(w, r, "URL is required", "", "")
		return
	}
	if len(rawURL) > 2048 {
		s.renderIndex(w, r, "URL is too long (max 2048 characters)", "", "")
		return
	}

	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		s.renderIndex(w, r, "Invalid URL", "", "")
		return
	}

	isAdmin := s.isLoggedIn(r)

	// Only admin can use custom slugs
	if customSlug != "" && !isAdmin {
		customSlug = ""
	}

	var finalSlug string

	if customSlug != "" {
		customSlug = strings.ToLower(customSlug)
		if err := slug.Validate(customSlug); err != nil {
			s.renderIndex(w, r, err.Error(), "", "")
			return
		}
		exists, err := s.db.SlugExists(customSlug)
		if err != nil {
			s.renderIndex(w, r, "Database error", "", "")
			return
		}
		if exists {
			s.renderIndex(w, r, fmt.Sprintf("Slug %q is already taken", customSlug), "", "")
			return
		}
		finalSlug = customSlug
	} else {
		// Return existing link if this URL was shortened before
		if existing, err := s.db.GetByURL(rawURL); err == nil {
			shortURL := s.baseURL(r) + "/" + existing.Slug
			s.renderIndex(w, r, "", shortURL, rawURL)
			return
		}

		for range 10 {
			candidate := slug.Generate(s.cfg.Slugs.Length)
			exists, err := s.db.SlugExists(candidate)
			if err != nil {
				s.renderIndex(w, r, "Database error", "", "")
				return
			}
			if !exists {
				finalSlug = candidate
				break
			}
		}
		if finalSlug == "" {
			s.renderIndex(w, r, "Could not generate a unique slug. Try again.", "", "")
			return
		}
	}

	createdBy := "anonymous"
	if isAdmin {
		createdBy = "admin"
	}

	if _, err := s.db.Create(finalSlug, rawURL, createdBy); err != nil {
		s.renderIndex(w, r, "Failed to create link", "", "")
		return
	}

	shortURL := s.baseURL(r) + "/" + finalSlug
	s.renderIndex(w, r, "", shortURL, rawURL)
}

func (s *Server) renderIndex(w http.ResponseWriter, r *http.Request, errMsg, shortURL, originalURL string) {
	var qrsvg template.HTML
	if shortURL != "" {
		qrsvg = makeQRSVG(shortURL, 180)
	}
	renderTemplate(w, "index.html", pageData{
		BaseURL:     s.baseURL(r),
		Error:       errMsg,
		ShortURL:    shortURL,
		OriginalURL: originalURL,
		QRSVG:       qrsvg,
		LoggedIn:    s.isLoggedIn(r),
		CSRFToken:   csrfToken(w, r),
	})
}

func (s *Server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	sl := strings.ToLower(r.PathValue("slug"))
	if sl == "" {
		http.NotFound(w, r)
		return
	}

	link, err := s.db.GetBySlug(sl)
	if err == sql.ErrNoRows {
		w.WriteHeader(http.StatusNotFound)
		renderTemplate(w, "404.html", pageData{BaseURL: s.baseURL(r)})
		return
	}
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Re-validate scheme before redirecting (defense in depth)
	if parsed, err := url.Parse(link.URL); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(w, "Invalid redirect target", http.StatusBadGateway)
		return
	}

	s.db.IncrementClicks(sl)
	http.Redirect(w, r, link.URL, http.StatusMovedPermanently)
}

// Admin handlers

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "admin_login.html", pageData{BaseURL: s.baseURL(r), CSRFToken: csrfToken(w, r)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username != s.cfg.Admin.Username {
		renderTemplate(w, "admin_login.html", pageData{Error: "Invalid credentials", CSRFToken: csrfToken(w, r)})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(s.cfg.Admin.PasswordHash), []byte(password)); err != nil {
		renderTemplate(w, "admin_login.html", pageData{Error: "Invalid credentials", CSRFToken: csrfToken(w, r)})
		return
	}

	token, err := s.sessions.Create()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	secure := r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   s.cfg.Admin.SessionHours * 3600,
	})

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session"); err == nil {
		s.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) pageData {
	return pageData{
		BaseURL:   s.baseURL(r),
		LoggedIn:  true,
		CSRFToken: csrfToken(w, r),
	}
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	adminLinks, err := s.db.ListByCreator("admin")
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	anonLinks, err := s.db.ListByCreator("anonymous")
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	topLinks, _ := s.db.TopLinks("admin", 20)

	d := s.adminPage(w, r)
	d.AdminLinks = adminLinks
	d.AnonLinks = anonLinks
	d.TopLinks = topLinks
	renderTemplate(w, "admin.html", d)
}

func (s *Server) handleAdminEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	link, err := s.db.GetByID(id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	d := s.adminPage(w, r)
	d.Link = link
	renderTemplate(w, "admin_edit.html", d)
}

func (s *Server) handleAdminEditSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	newSlug := strings.TrimSpace(r.FormValue("slug"))
	newURL := strings.TrimSpace(r.FormValue("url"))

	renderEditErr := func(msg string) {
		link, _ := s.db.GetByID(id)
		d := s.adminPage(w, r)
		d.Link = link
		d.Error = msg
		renderTemplate(w, "admin_edit.html", d)
	}

	if newSlug == "" || newURL == "" {
		renderEditErr("Slug and URL are required")
		return
	}

	if err := slug.Validate(newSlug); err != nil {
		renderEditErr(err.Error())
		return
	}

	parsed, err := url.ParseRequestURI(newURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		renderEditErr("Invalid URL")
		return
	}

	if err := s.db.Update(id, newSlug, newURL); err != nil {
		renderEditErr("Failed to update: slug may already be taken")
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	s.db.Delete(id)

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) isLoggedIn(r *http.Request) bool {
	cookie, err := r.Cookie("session")
	if err != nil {
		return false
	}
	return s.sessions.Valid(cookie.Value)
}

func formatTime(t time.Time) string {
	return t.Format("2006-01-02 15:04")
}

// ---- Paste handlers ----

func (s *Server) handleBinView(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	providedToken := r.PathValue("token")

	paste, err := s.db.GetPasteByName(name)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Token check (constant-time to prevent timing attacks)
	if paste.Token != "" && subtle.ConstantTimeCompare([]byte(paste.Token), []byte(providedToken)) != 1 {
		http.Error(w, "Access denied: invalid or missing token.", http.StatusForbidden)
		return
	}

	// Raw view
	if r.URL.Query().Get("raw") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(paste.Content))
		return
	}

	var body template.HTML
	if paste.Format == "markdown" {
		body = renderMarkdown(paste.Content)
	} else {
		body = template.HTML("<pre><code>" + template.HTMLEscapeString(paste.Content) + "</code></pre>")
	}

	renderTemplate(w, "bin_view.html", pageData{
		BaseURL:   s.baseURL(r),
		Paste:     paste,
		PasteBody: body,
		LoggedIn:  s.isLoggedIn(r),
	})
}

func (s *Server) handleAdminBin(w http.ResponseWriter, r *http.Request) {
	pastes, err := s.db.ListPastes()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	d := s.adminPage(w, r)
	d.Pastes = pastes
	renderTemplate(w, "admin_bin.html", d)
}

func (s *Server) handleAdminBinNew(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "admin_bin_edit.html", s.adminPage(w, r))
}

func (s *Server) handleAdminBinCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	title := strings.TrimSpace(r.FormValue("title"))
	content := r.FormValue("content")
	format := r.FormValue("format")
	if format != "text" {
		format = "markdown"
	}
	enableToken := r.FormValue("enable_token") == "1"

	newErrPage := func(msg string) {
		d := s.adminPage(w, r)
		d.Error = msg
		renderTemplate(w, "admin_bin_edit.html", d)
	}

	if len(content) > 512*1024 {
		newErrPage("Paste content too large (max 512 KB)")
		return
	}
	if name == "" {
		newErrPage("Name is required")
		return
	}
	if !validPasteName(name) {
		newErrPage("Name must be alphanumeric with hyphens/underscores only")
		return
	}
	exists, _ := s.db.PasteNameExists(name)
	if exists {
		newErrPage(fmt.Sprintf("Name %q is already taken", name))
		return
	}

	token := ""
	if enableToken {
		token = slug.Generate(12)
	}

	if _, err := s.db.CreatePaste(name, title, content, format, token); err != nil {
		newErrPage("Failed to create paste")
		return
	}
	http.Redirect(w, r, "/admin/bin", http.StatusSeeOther)
}

func (s *Server) handleAdminBinEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	paste, err := s.db.GetPasteByID(id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	d := s.adminPage(w, r)
	d.Paste = paste
	renderTemplate(w, "admin_bin_edit.html", d)
}

func (s *Server) handleAdminBinSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	paste, err := s.db.GetPasteByID(id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	title := strings.TrimSpace(r.FormValue("title"))
	content := r.FormValue("content")
	format := r.FormValue("format")
	if format != "text" {
		format = "markdown"
	}
	tokenAction := r.FormValue("token_action") // "keep", "disable", "regenerate"

	editErrPage := func(msg string) {
		d := s.adminPage(w, r)
		d.Paste = paste
		d.Error = msg
		renderTemplate(w, "admin_bin_edit.html", d)
	}

	if len(content) > 512*1024 {
		editErrPage("Paste content too large (max 512 KB)")
		return
	}
	if name == "" || !validPasteName(name) {
		editErrPage("Invalid name")
		return
	}

	token := paste.Token
	switch tokenAction {
	case "disable":
		token = ""
	case "regenerate":
		token = slug.Generate(12)
	}

	if err := s.db.UpdatePaste(id, name, title, content, format, token); err != nil {
		editErrPage("Failed to save: name may already be taken")
		return
	}
	http.Redirect(w, r, "/admin/bin", http.StatusSeeOther)
}

func (s *Server) handleAdminBinDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.db.DeletePaste(id)
	http.Redirect(w, r, "/admin/bin", http.StatusSeeOther)
}

var validPasteNameRe = strings.NewReplacer() // placeholder — use inline check below
func validPasteName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
