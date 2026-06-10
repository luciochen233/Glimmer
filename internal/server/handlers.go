package server

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"glimmer/internal/db"
	"glimmer/internal/slug"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
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
	Uploads     any
	LoggedIn    bool
	ActiveNav   string // "links" | "pastes" | "uploads" | "shorten" — for sidebar highlight
	CSRFToken   string
	UploadMaxMB int64
}

type UploadInfo struct {
	Filename     string
	OriginalName string
	SizeHuman    string
	Size         int64
	URL          string
	Ext          string
	Width        int
	Height       int
	CanResize    bool
	IsImage      bool
	ModTime      time.Time
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
	// X-Forwarded-Proto is client-settable, so it is only honoured behind a
	// trusted proxy (same rule as X-Forwarded-For). Otherwise fall back to
	// the configured base URL's scheme.
	proto := "http"
	if s.isHTTPS(r) {
		proto = "https"
	}
	return proto + "://" + host
}

// isHTTPS reports whether the request should be treated as HTTPS: either the
// trusted proxy says so, or the operator configured an https base URL.
func (s *Server) isHTTPS(r *http.Request) bool {
	if s.cfg.Server.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return strings.HasPrefix(s.cfg.Server.BaseURL, "https://")
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "index.html", pageData{BaseURL: s.baseURL(r), LoggedIn: s.isLoggedIn(r), CSRFToken: s.csrfToken(w, r)})
}

func (s *Server) handleShorten(w http.ResponseWriter, r *http.Request) {
	if !s.isLoggedIn(r) {
		ip := clientIP(r, s.cfg.Server.TrustProxy)
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
		CSRFToken:   s.csrfToken(w, r),
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
	renderTemplate(w, "admin_login.html", pageData{BaseURL: s.baseURL(r), CSRFToken: s.csrfToken(w, r)})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Throttle login attempts per IP to slow brute force.
	if !s.authLimiter.Allow(clientIP(r, s.cfg.Server.TrustProxy)) {
		w.WriteHeader(http.StatusTooManyRequests)
		renderTemplate(w, "admin_login.html", pageData{Error: "Too many attempts. Please wait a moment.", CSRFToken: s.csrfToken(w, r)})
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	// Always run bcrypt (even when the username is wrong) so response timing
	// does not reveal whether the username is valid. The username is compared
	// in constant time. bcrypt runs through the concurrency gate.
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.Admin.Username)) == 1
	pwOK, available := s.checkPassword(s.cfg.Admin.PasswordHash, password)
	if !available {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		renderTemplate(w, "admin_login.html", pageData{Error: "Server busy. Please try again.", CSRFToken: s.csrfToken(w, r)})
		return
	}
	if !userOK || !pwOK {
		w.WriteHeader(http.StatusUnauthorized)
		renderTemplate(w, "admin_login.html", pageData{Error: "Invalid credentials", CSRFToken: s.csrfToken(w, r)})
		return
	}

	token, err := s.sessions.Create()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	secure := s.isHTTPS(r)
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

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request, nav string) pageData {
	return pageData{
		BaseURL:     s.baseURL(r),
		LoggedIn:    true,
		ActiveNav:   nav,
		CSRFToken:   s.csrfToken(w, r),
		UploadMaxMB: s.cfg.Upload.MaxSize,
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

	d := s.adminPage(w, r, "links")
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

	d := s.adminPage(w, r, "links")
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
		d := s.adminPage(w, r, "links")
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

func (s *Server) handleBinQR(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	paste, err := s.db.GetPasteByName(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fullURL := s.baseURL(r) + "/bin/" + paste.Name
	if r.URL.Query().Get("full") == "1" && paste.Token != "" {
		fullURL += "/" + paste.Token
	}
	svg := makeQRSVG(fullURL, 200)
	if svg == "" {
		http.Error(w, "QR generation failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write([]byte(svg))
}

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

	// Token check (constant-time, case-insensitive since QR codes uppercase URLs)
	if paste.Token != "" && subtle.ConstantTimeCompare([]byte(strings.ToLower(paste.Token)), []byte(strings.ToLower(providedToken))) != 1 {
		if paste.Hidden {
			w.WriteHeader(http.StatusNotFound)
			renderTemplate(w, "404.html", pageData{BaseURL: s.baseURL(r)})
			return
		}
		errMsg := ""
		if providedToken != "" {
			errMsg = "Invalid token. Please try again."
		}
		w.WriteHeader(http.StatusForbidden)
		renderTemplate(w, "bin_token.html", pageData{
			BaseURL: s.baseURL(r),
			Error:   errMsg,
		})
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

	// Embed view — used by admin paste preview iframe; strips the page chrome
	// (sidebar / public header / toolbar / card wrapper) so only the paste body renders.
	if r.URL.Query().Get("embed") == "1" {
		renderTemplate(w, "bin_view_embed.html", pageData{
			BaseURL:   s.baseURL(r),
			Paste:     paste,
			PasteBody: body,
		})
		return
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
	cards := make([]pasteCard, 0, len(pastes))
	for _, p := range pastes {
		cards = append(cards, pasteCard{
			Paste:    p,
			Summary:  pasteSummary(p.Content, 140),
			ThumbURL: thumbURLFor(firstPasteImage(p.Content)),
		})
	}
	d := s.adminPage(w, r, "pastes")
	d.Pastes = cards
	renderTemplate(w, "admin_bin.html", d)
}

func (s *Server) handleAdminBinNew(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "admin_bin_edit.html", s.adminPage(w, r, "pastes"))
}

func (s *Server) handleAdminBinCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	title := strings.TrimSpace(r.FormValue("title"))
	content := r.FormValue("content")
	format := r.FormValue("format")
	if format != "text" {
		format = "markdown"
	}
	enableTokenVal := r.FormValue("enable_token")
	hidden := r.FormValue("hidden") == "1"

	newErrPage := func(msg string) {
		d := s.adminPage(w, r, "pastes")
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
	switch enableTokenVal {
	case "1":
		token = slug.Generate(12)
	case "custom":
		token = strings.TrimSpace(r.FormValue("custom_token"))
		if token == "" {
			newErrPage("Custom token cannot be empty")
			return
		}
		if !validPasteName(token) {
			newErrPage("Custom token must be alphanumeric with hyphens/underscores (max 64 chars)")
			return
		}
	}

	if _, err := s.db.CreatePaste(name, title, content, format, token, hidden); err != nil {
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
	d := s.adminPage(w, r, "pastes")
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
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	title := strings.TrimSpace(r.FormValue("title"))
	content := r.FormValue("content")
	format := r.FormValue("format")
	if format != "text" {
		format = "markdown"
	}
	tokenAction := r.FormValue("token_action") // "keep", "disable", "regenerate", "custom"
	hidden := r.FormValue("hidden") == "1"

	editErrPage := func(msg string) {
		d := s.adminPage(w, r, "pastes")
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
	case "custom":
		token = strings.TrimSpace(r.FormValue("custom_token"))
		if token == "" {
			editErrPage("Custom token cannot be empty")
			return
		}
		if !validPasteName(token) {
			editErrPage("Custom token must be alphanumeric with hyphens/underscores (max 64 chars)")
			return
		}
	}

	if err := s.db.UpdatePaste(id, name, title, content, format, token, hidden); err != nil {
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

// ---- Upload helpers ----

var validUploadRe = regexp.MustCompile(`^[0-9a-f]{32}\.[a-z0-9]{1,10}$`)

func validUploadFilename(name string) bool {
	return validUploadRe.MatchString(name)
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".gif": true, ".webp": true,
}

func isImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

func formatBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func scaleImage(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return src
	}
	var newW, newH int
	if w >= h {
		newW = maxDim
		newH = int(math.Round(float64(h) * float64(maxDim) / float64(w)))
	} else {
		newH = maxDim
		newW = int(math.Round(float64(w) * float64(maxDim) / float64(h)))
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, newW, newH))
	scaleX := float64(w) / float64(newW)
	scaleY := float64(h) / float64(newH)
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			srcX := int(float64(x)*scaleX) + b.Min.X
			srcY := int(float64(y)*scaleY) + b.Min.Y
			r, g, bl, a := src.At(srcX, srcY).RGBA()
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(bl >> 8),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

// maxDecodePixels caps the pixel count of images we are willing to fully
// decode. A small compressed file can declare enormous dimensions (a
// decompression bomb): 24 MP decodes to ~96 MB of NRGBA, which is already a
// big bite on an RPi Zero — anything larger is rejected before decode.
const maxDecodePixels = 24 << 20

// decodeImageBounded decodes an image file, but reads only the header first
// and refuses files whose decoded size would exceed maxDecodePixels. The
// dimension product uses int64 so a malicious header cannot overflow 32-bit
// int on ARMv5.
func decodeImageBounded(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return nil, fmt.Errorf("image dimensions too large (%dx%d, max %d megapixels)", cfg.Width, cfg.Height, maxDecodePixels>>20)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	return src, err
}

// ---- Thumbnails ----

const thumbMaxDim = 512 // long-side size of generated thumbnails, in pixels

// thumbDir is the directory thumbnails are cached in (a subdir of uploads).
func (s *Server) thumbDir() string {
	return filepath.Join(s.cfg.Upload.Dir, "thumbs")
}

// thumbPath returns the on-disk path of the thumbnail for an upload filename.
// Thumbnails are always JPEG, named after the original's hash.
func (s *Server) thumbPath(filename string) string {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	return filepath.Join(s.thumbDir(), base+".jpg")
}

// generateThumbnail decodes an uploaded image, scales it so its long side is at
// most thumbMaxDim px, and writes a cached JPEG thumbnail. Best-effort: returns
// an error for formats it can't decode (e.g. WebP), leaving callers to fall
// back to the original file.
func (s *Server) generateThumbnail(filename string) error {
	if !isImageFile(filename) {
		return fmt.Errorf("not an image")
	}
	src, err := decodeImageBounded(filepath.Join(s.cfg.Upload.Dir, filename))
	if err != nil {
		return err
	}
	dst := scaleImage(src, thumbMaxDim)
	if err := os.MkdirAll(s.thumbDir(), 0755); err != nil {
		return err
	}
	tp := s.thumbPath(filename)
	tmp := tp + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, dst, &jpeg.Options{Quality: 82}); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	return os.Rename(tmp, tp)
}

// handleThumb serves a cached 512px (long side) JPEG thumbnail for an uploaded
// image, generating it on first request. Falls back to serving the original
// inline for images it can't decode (e.g. WebP).
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("filename")
	if !validUploadFilename(name) || !isImageFile(name) {
		http.NotFound(w, r)
		return
	}
	orig := filepath.Join(s.cfg.Upload.Dir, name)
	if _, err := os.Stat(orig); err != nil {
		http.NotFound(w, r)
		return
	}
	tp := s.thumbPath(name)
	if _, err := os.Stat(tp); err != nil {
		if err := s.generateThumbnail(name); err != nil {
			// Can't thumbnail this format — serve the original inline.
			http.ServeFile(w, r, orig)
			return
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, tp)
}

// ---- Paste card helpers (admin Pastes grid) ----

type pasteCard struct {
	db.Paste
	Summary  string
	ThumbURL string // thumbnail URL for the first image, or "" if none
}

var (
	pasteImgMDRe   = regexp.MustCompile(`!\[[^\]]*\]\(\s*(\S+?)\s*\)`)
	pasteImgHTMLRe = regexp.MustCompile(`(?i)<img[^>]+src=["']?([^"'>\s]+)`)
	fencedCodeRe   = regexp.MustCompile("(?s)```.*?```")
	mdLinkRe       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdStripRe      = regexp.MustCompile("[#>*_`~|<>\\-]+")
)

// firstPasteImage returns the URL of the first image referenced in paste
// content (markdown or HTML), or "" if there is none.
func firstPasteImage(content string) string {
	idxMD, idxHTML := -1, -1
	var urlMD, urlHTML string
	if m := pasteImgMDRe.FindStringSubmatchIndex(content); m != nil {
		idxMD, urlMD = m[0], content[m[2]:m[3]]
	}
	if m := pasteImgHTMLRe.FindStringSubmatchIndex(content); m != nil {
		idxHTML, urlHTML = m[0], content[m[2]:m[3]]
	}
	switch {
	case idxMD == -1 && idxHTML == -1:
		return ""
	case idxHTML == -1:
		return urlMD
	case idxMD == -1:
		return urlHTML
	case idxMD <= idxHTML:
		return urlMD
	default:
		return urlHTML
	}
}

// thumbURLFor maps a paste's first image URL to a card thumbnail URL. Local
// uploads use the on-the-fly thumbnail route; remote https images are used
// as-is (allowed by CSP img-src); anything else yields no thumbnail.
func thumbURLFor(rawURL string) string {
	if strings.HasPrefix(rawURL, "/uploads/") {
		name := strings.TrimPrefix(rawURL, "/uploads/")
		if validUploadFilename(name) && isImageFile(name) {
			return "/uploads/thumb/" + name
		}
		return ""
	}
	if strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}
	return ""
}

// pasteSummary produces a short plain-text excerpt from paste content by
// stripping code fences, images, link targets, and markdown punctuation.
func pasteSummary(content string, max int) string {
	t := fencedCodeRe.ReplaceAllString(content, " ")
	t = pasteImgMDRe.ReplaceAllString(t, " ")
	t = mdLinkRe.ReplaceAllString(t, "$1")
	t = mdStripRe.ReplaceAllString(t, " ")
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "…"
	}
	return t
}

// handleUploadsServe serves files from the upload directory. For non-image
// files it forces Content-Disposition: attachment with an octet-stream type so
// that arbitrary uploads (e.g. HTML, SVG, JS) cannot be rendered in-browser
// from the same origin as the admin panel.
func (s *Server) handleUploadsServe(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/uploads/")
	if !validUploadFilename(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.Upload.Dir, name)
	if !isImageFile(name) {
		download := name
		if orig, _ := s.db.GetUploadName(name); orig != "" {
			download = orig
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", contentDispositionAttachment(download))
	}
	http.ServeFile(w, r, path)
}

// contentDispositionAttachment builds a safe attachment header. The ASCII
// filename strips characters that would break header parsing; the RFC 5987
// filename* preserves any UTF-8 chars for clients that understand it.
func contentDispositionAttachment(name string) string {
	var ascii strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || r == '"' || r == '\\' {
			continue
		}
		if r > 127 {
			ascii.WriteByte('_')
			continue
		}
		ascii.WriteRune(r)
	}
	a := ascii.String()
	if a == "" {
		a = "download"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, a, url.PathEscape(name))
}

// ---- Upload management handlers ----

func (s *Server) handleAdminUploads(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.cfg.Upload.Dir)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "Failed to read uploads", http.StatusInternalServerError)
		return
	}

	names, _ := s.db.ListUploadNames()

	var uploads []UploadInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !validUploadFilename(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		ui := UploadInfo{
			Filename:     name,
			OriginalName: names[name],
			Size:         info.Size(),
			SizeHuman:    formatBytes(info.Size()),
			URL:          "/uploads/" + name,
			Ext:          strings.TrimPrefix(ext, "."),
			IsImage:      isImageFile(name),
			ModTime:      info.ModTime(),
		}
		if ui.IsImage {
			if f, err := os.Open(filepath.Join(s.cfg.Upload.Dir, name)); err == nil {
				if cfg, _, err := image.DecodeConfig(f); err == nil {
					ui.Width = cfg.Width
					ui.Height = cfg.Height
				}
				f.Close()
			}
		}
		if ext == ".png" || ext == ".jpg" {
			ui.CanResize = true
		}
		uploads = append(uploads, ui)
	}

	sort.Slice(uploads, func(i, j int) bool {
		return uploads[i].ModTime.After(uploads[j].ModTime)
	})

	d := s.adminPage(w, r, "uploads")
	d.Uploads = uploads
	renderTemplate(w, "admin_uploads.html", d)
}

func (s *Server) handleAdminUploadDelete(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if !validUploadFilename(filename) {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	os.Remove(filepath.Join(s.cfg.Upload.Dir, filename))
	os.Remove(s.thumbPath(filename))
	s.db.DeleteUpload(filename)
	http.Redirect(w, r, "/admin/uploads", http.StatusSeeOther)
}

func (s *Server) handleAdminUploadResize(w http.ResponseWriter, r *http.Request) {
	// The resize request arrives as multipart FormData, so CSRF is verified
	// here rather than via the requireCSRF wrapper (see CLAUDE.md). The body
	// is tiny — cap it before the form parse triggered by verifyCSRF.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !verifyCSRF(r) {
		jsonError(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	filename := r.PathValue("filename")
	if !validUploadFilename(filename) {
		jsonError(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".png" && ext != ".jpg" {
		jsonError(w, "Only PNG and JPEG can be resized", http.StatusBadRequest)
		return
	}
	maxDim, err := strconv.Atoi(r.FormValue("max_dim"))
	if err != nil || maxDim < 64 || maxDim > 8192 {
		jsonError(w, "max_dim must be between 64 and 8192", http.StatusBadRequest)
		return
	}

	path := filepath.Join(s.cfg.Upload.Dir, filename)
	if _, err := os.Stat(path); err != nil {
		jsonError(w, "File not found", http.StatusNotFound)
		return
	}
	src, err := decodeImageBounded(path)
	if err != nil {
		jsonError(w, "Failed to decode image: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	dst := scaleImage(src, maxDim)

	tmpPath := path + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		jsonError(w, "Failed to write file", http.StatusInternalServerError)
		return
	}
	var encErr error
	if ext == ".jpg" {
		encErr = jpeg.Encode(out, dst, &jpeg.Options{Quality: 85})
	} else {
		encErr = png.Encode(out, dst)
	}
	out.Close()
	if encErr != nil {
		os.Remove(tmpPath)
		jsonError(w, "Failed to encode image", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		jsonError(w, "Failed to save resized image", http.StatusInternalServerError)
		return
	}
	os.Remove(s.thumbPath(filename)) // invalidate cached thumbnail; regenerated on next view

	info, _ := os.Stat(path)
	var newSize int64
	if info != nil {
		newSize = info.Size()
	}
	b := dst.Bounds()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"size_human": formatBytes(newSize),
		"width":      b.Dx(),
		"height":     b.Dy(),
	})
}

// ---- Image upload handler ----

var allowedMIME = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	maxBytes := s.cfg.Upload.MaxSize * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	// Extend per-request deadlines for large uploads
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(120 * time.Second))
	rc.SetWriteDeadline(time.Now().Add(120 * time.Second))

	// Parse the multipart body explicitly BEFORE checking CSRF. verifyCSRF reads
	// a form value, which would otherwise trigger an implicit ParseMultipartForm
	// and surface an oversized body (tripping MaxBytesReader above) as a bogus
	// "Invalid CSRF token" 403. Parsing here lets us report the real cause (413).
	// A small maxMemory keeps large uploads spilling to temp files, not RAM (RPi Zero).
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		w.Header().Set("Content-Type", "application/json")
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("File too large (max %d MB)", s.cfg.Upload.MaxSize)})
		} else {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid upload"})
		}
		return
	}

	// Verify CSRF (form already parsed; no body re-read).
	if !verifyCSRF(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid CSRF token"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "No file provided"})
		return
	}
	defer file.Close()

	// Read first 512 bytes to detect MIME type
	head := make([]byte, 512)
	n, err := file.Read(head)
	if err != nil && err != io.EOF {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read file"})
		return
	}
	head = head[:n]

	mimeType := http.DetectContentType(head)
	ext, ok := allowedMIME[mimeType]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		json.NewEncoder(w).Encode(map[string]string{"error": "Only PNG, JPEG, GIF, and WebP images are allowed"})
		return
	}

	// Generate random filename
	randBytes := make([]byte, 16)
	rand.Read(randBytes)
	filename := hex.EncodeToString(randBytes) + ext

	// Write file to disk
	destPath := filepath.Join(s.cfg.Upload.Dir, filename)
	out, err := os.Create(destPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to save file"})
		return
	}
	defer out.Close()

	// Write the head bytes we already read, then copy the rest
	if _, err := out.Write(head); err != nil {
		os.Remove(destPath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to save file"})
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		os.Remove(destPath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to save file"})
		return
	}

	displayName := sanitizeDisplayFilename(header.Filename)
	s.db.RecordUpload(filename, displayName)
	_ = s.generateThumbnail(filename) // best-effort; lazily regenerated on first view otherwise

	imgURL := "/uploads/" + filename
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url":      imgURL,
		"filename": displayName,
		"markdown": "![](" + imgURL + ")",
	})
}

// ---- File upload handler (any file, 5 MB max) ----

var validExtRe = regexp.MustCompile(`^[a-z0-9]{1,10}$`)

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	const maxFileBytes = 5 * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxFileBytes+4096)

	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(120 * time.Second))
	rc.SetWriteDeadline(time.Now().Add(120 * time.Second))

	// Parse before CSRF so an oversized body is reported as 413, not a bogus 403.
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			jsonError(w, "File too large (max 5 MB)", http.StatusRequestEntityTooLarge)
		} else {
			jsonError(w, "Invalid upload", http.StatusBadRequest)
		}
		return
	}

	if !verifyCSRF(r) {
		jsonError(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "No file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), "."))
	if ext == "" || !validExtRe.MatchString(ext) {
		jsonError(w, "Invalid file extension", http.StatusBadRequest)
		return
	}

	randBytes := make([]byte, 16)
	rand.Read(randBytes)
	filename := hex.EncodeToString(randBytes) + "." + ext

	destPath := filepath.Join(s.cfg.Upload.Dir, filename)
	out, err := os.Create(destPath)
	if err != nil {
		jsonError(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	defer out.Close()

	written, err := io.Copy(out, file)
	if err != nil {
		os.Remove(destPath)
		jsonError(w, "Failed to save file", http.StatusInternalServerError)
		return
	}
	if written > maxFileBytes {
		os.Remove(destPath)
		jsonError(w, "File too large (max 5 MB)", http.StatusRequestEntityTooLarge)
		return
	}

	fileURL := "/uploads/" + filename
	displayName := sanitizeDisplayFilename(header.Filename)
	if displayName == "" {
		displayName = filename
	}
	s.db.RecordUpload(filename, displayName)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url":      fileURL,
		"filename": displayName,
		"markdown": "[" + displayName + "](" + fileURL + ")",
	})
}

// sanitizeDisplayFilename strips path components, control chars, and
// markdown-significant characters from a user-supplied filename so that it
// can be safely embedded in a markdown link.
func sanitizeDisplayFilename(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 {
			continue
		}
		switch r {
		case '[', ']', '(', ')', '\\', '`', '*', '<', '>', '"', '\'':
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String())
	if len(s) > 100 {
		s = s[:100]
	}
	return s
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
