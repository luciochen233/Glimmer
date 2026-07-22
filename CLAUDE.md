# Glimmer — Claude Code Guide

## Project Overview

**Glimmer** is a single-binary Go URL shortener + pastebin with image and file upload support. It runs on a Raspberry Pi Zero (512 MB RAM, ARMv5) and compiles to a fully self-contained binary — no CGo, no external runtime, no system SQLite.

Live at [luci.ooo](https://luci.ooo).

---

## Build Commands

```bash
make build          # Windows → glimmer.exe
make build-linux    # Linux x86-64 → glimmer
make build-arm      # RPi Zero (ARMv5) → glimmer-arm
make run            # Build + run on Windows
make clean          # Remove all compiled binaries
make hash           # Interactively hash a password for config.toml
```

Always run `go build ./...` (or `make build`) after any Go changes to confirm the project compiles before finishing a task.

---

## Project Structure

```
glimmer/
├── main.go                          # Entry point; --hash-password flag
├── config.example.toml              # Tracked config template — copy to config.toml
├── config.toml                      # Runtime config (gitignored, loaded at startup)
├── go.mod / go.sum
├── Makefile
├── data/
│   ├── urls.db                      # SQLite DB (auto-created)
│   └── uploads/                     # Uploaded images & files (auto-created)
└── internal/
    ├── config/config.go             # TOML config structs + loader
    ├── db/db.go                     # DB open/migrate; Link + Paste + Upload CRUD
    ├── slug/slug.go                 # Base-36 slug generation + validation
    └── server/
        ├── server.go                # Mux, middleware wiring, server start
        ├── handlers.go              # All HTTP handlers + rendering helpers
        ├── middleware.go            # Auth, CSRF, sessions, rate limiter
        ├── templates.go             # embed.FS for templates + static; initTemplates()
        ├── static/
        │   ├── css/
        │   │   └── style.css        # Custom Solis-style design system (amber/cream, Outfit font)
        │   ├── favicon.ico
        │   ├── favicon.png
        │   ├── js/                  # Per-page scripts (CSP blocks inline JS)
        │   │   ├── base.js          # QR modal, data-confirm forms, glimmerCopy
        │   │   └── *.js             # One file per template that needs behaviour
        │   └── upload.js            # Shared client-side upload helper (window.GlimmerUpload)
        └── templates/
            ├── base.html            # Layout wrapper: sidebar (logged in) or public header
            ├── index.html           # Public: URL shortener form
            ├── admin_login.html     # Login (light, centered card)
            ├── admin.html           # Dashboard: stats, tiles, links tables
            ├── admin_edit.html      # Edit a single link
            ├── admin_bin.html       # Pastes list (cards + preview dialog)
            ├── admin_bin_edit.html  # Create / edit paste (uses /static/upload.js)
            ├── admin_uploads.html   # Image + file upload management
            ├── bin_view.html        # Public paste viewer
            ├── bin_token.html       # Token prompt for protected pastes
            └── 404.html
```

---

## Architecture

### Single package per layer
- `internal/config` — pure config loading, no HTTP
- `internal/db` — pure database, no HTTP
- `internal/slug` — pure slug logic, no HTTP
- `internal/server` — everything HTTP: mux, handlers, middleware, templates

### All handlers on `*Server`
```go
type Server struct {
    cfg      *config.Config
    db       *db.DB
    sessions *sessionStore
    limiter  *rateLimiter
}
```

### pageData is the single template context struct
All templates receive a `pageData` struct. Add new fields there when templates need new data — never pass raw types directly.

### Templates are embedded, not on disk
Templates and static files are embedded into the binary via `//go:embed` in `templates.go`. Changes to templates are compiled in; they are **not** reloaded at runtime. Always rebuild after template changes.

### No JS frameworks — and no inline JS
All JavaScript is vanilla ES5-style (`var`, not `const`/`let`). No build step, no npm, no bundler.

The CSP is `script-src 'self'`, so **inline `<script>` blocks and `on*=` attributes in templates are blocked by the browser**. All behaviour lives in files under `static/js/` (one per template, plus `base.js` for shared chrome: the QR modal, `glimmerCopy`, and `form[data-confirm]` submit confirmation). To pass template data to JS, put it in `data-*` attributes and read it from the script — never interpolate `{{...}}` into JS. Shared upload logic lives in `static/upload.js` as `window.GlimmerUpload`.

### CSS framework
**No CSS framework.** Glimmer uses a custom design system in `static/css/style.css` inspired by Solis's "Sunlight & Clarity" theme — warm amber (`#d97706`) on cream (`#fafaf9`) with Outfit font, sidebar + main-area layout, soft shadows with amber tint, cubic-bezier spring animations on toasts/modals. The CSS is served from `/static/css/style.css` so the app works fully offline. The Outfit font itself is loaded from Google Fonts (one external request); replace with a vendored woff2 if you need a true air-gapped build. Do not reintroduce CDN `<link>`/`<script>`/image references — everything must be same-origin (enforced by the `Content-Security-Policy` header in `securityHeaders`).

### Layout
Admin pages share a `base.html` layout that renders a 260px fixed sidebar (when `.LoggedIn`) or a sticky public header (otherwise). Each page template defines its own `{{define "content"}}...{{end}}` block; `base.html` calls `{{template "content" .}}` to inject it. The active sidebar item is driven by the `ActiveNav` field in `pageData` (`"links"`, `"pastes"`, `"uploads"`, or `"shorten"`). Use `s.adminPage(w, r, "links")` to get a pre-populated `pageData` for an admin route.

### Theme
Single light theme only — no `data-theme` switching. The design tokens are documented in the `:root` of `static/css/style.css`.

---

## Key Patterns

### Adding a new admin route
1. Write the handler method on `*Server` in `handlers.go`
2. Register the route in `server.go` `Start()`, wrapped with middleware:
   - Read routes: `s.requireAuth(s.handleFoo)`
   - Write routes: `s.requireAuth(s.requireCSRF(s.handleFoo))`
3. Add the route to the URL routes table in `README.md`

### CSRF
All state-changing POSTs must include `<input type="hidden" name="csrf_token" value="{{$.CSRFToken}}">` in the form. Handlers must be wrapped with `requireCSRF`. For multipart form handlers (file uploads), verify CSRF manually inside the handler *after* calling `r.ParseMultipartForm` — do not use the `requireCSRF` wrapper as it will parse with Go's default 32 MB limit before `MaxBytesReader` takes effect.

### File uploads
Two endpoints, both admin-only and CSRF-protected:
- `POST /admin/upload` — images only (PNG/JPEG/GIF/WebP), up to `upload.max_size_mb` from config (default 50 MB). MIME type validated by magic bytes via `http.DetectContentType`.
- `POST /admin/upload-file` — any file type, hard-coded 5 MB cap. Validates only that the extension is `[a-z0-9]{1,10}`.

Shared upload rules:
- Max size enforced via `http.MaxBytesReader(w, r.Body, maxBytes)` at handler start
- Filenames written to disk are always `[32 hex chars].[ext]` — validated by `validUploadRe` regexp before any file operation. Original filenames are stored in the `uploads` table (filename → original_name) and used for `Content-Disposition` and the admin listing.
- Per-request timeouts extended to 120s via `http.NewResponseController` — global timeouts stay at 5s/10s
- `sanitizeDisplayFilename()` strips control chars and markdown-significant chars (`[ ] ( ) \ \` * < > " '`) before the original name is embedded in JSON responses or stored.

### Serving uploads (`/uploads/{filename}`)
`handleUploadsServe` validates the filename against `validUploadRe`, then:
- **Images** (`.png`/`.jpg`/`.gif`/`.webp`) — served inline by `http.ServeFile` so they can embed in public pastes.
- **Everything else** — forced to download with `Content-Type: application/octet-stream` + `Content-Disposition: attachment` (with both ASCII `filename=` and RFC 5987 `filename*=UTF-8''…`). This prevents same-origin XSS from uploaded HTML/SVG/JS being rendered by the browser.

### Image resize
Only PNG (`.png`) and JPEG (`.jpg`) are resizable. Resize uses a nearest-neighbour scale implemented in stdlib (`image`, `image/color`, `image/jpeg`, `image/png`) — no external image library. GIF and WebP are served and deletable but not resizable.

### Admin tabs
Admin pages share a sidebar rendered by `base.html`. To highlight the current section, set `ActiveNav` on the `pageData`:
```go
d := s.adminPage(w, r, "pastes")  // highlights the Pastes nav item
renderTemplate(w, "admin_bin.html", d)
```
Valid values are `"links"`, `"pastes"`, `"uploads"`, `"shorten"`. New admin pages should follow the same `base.html` + `{{define "content"}}` pattern and live under `templates/`.

### Database migrations
Migrations run automatically in `db.Open()`. New columns are added with `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`. Never drop columns — the schema must remain backwards compatible with existing `data/urls.db` files on deployed instances.

### Paste name / token validation
Both paste names and access tokens must pass `validPasteName()`: alphanumeric + hyphens + underscores, 1–64 chars.

### lowercasePath middleware
Redirects uppercase paths to lowercase (for QR-code URL compatibility). **Excludes** `/static/`, `/admin`, and `/uploads/` — these prefixes must stay in the exclusion list if new case-sensitive routes are added.

---

## Configuration

`config.example.toml` is the tracked template. Copy it to `config.toml` (gitignored) and fill in real values — `config.toml` is what the server loads at startup. Never edit the example with live secrets.

```toml
[server]
port           = 8888
base_url       = "http://localhost:8888"
read_timeout   = "5s"
write_timeout  = "10s"
trust_proxy    = false          # only honour X-Forwarded-For behind a trusted proxy
max_concurrent = 64             # in-flight request cap (503 beyond it); lower on RPi Zero

[admin]
username      = "admin"
password_hash = "$2a$10$..."   # REQUIRED — server refuses to start if empty; generate with: ./glimmer --hash-password

[database]
path = "./data/urls.db"

[slugs]
length = 3

[upload]
dir         = "./data/uploads"
max_size_mb = 50
```

All fields have safe defaults in `config.Load()` — the only required field is `admin.password_hash`. If `config.toml` is absent, `config.Load()` returns an error; copy `config.example.toml` to `config.toml` first.

---

## Dependencies

| Package | Purpose |
|---|---|
| `github.com/BurntSushi/toml` | Config parsing |
| `golang.org/x/crypto/bcrypt` | Password hashing |
| `modernc.org/sqlite` | Pure-Go SQLite (no CGo) |
| `github.com/yuin/goldmark` | Markdown rendering (GFM + tables) |
| `rsc.io/qr` | QR code generation (SVG) |

Everything else in `go.sum` is a transitive dependency. **Do not add new external dependencies** without strong justification — the project is intentionally minimal for RPi Zero deployment.

---

## Deployment Target

The primary deployment target is a **Raspberry Pi Zero** (ARMv5, 512 MB RAM). Keep this in mind:
- Avoid memory-heavy operations (e.g. loading many large images into RAM at once)
- Image resize uses nearest-neighbour (fast, low memory) rather than bicubic
- The binary embeds all templates and static assets — no deployment of separate files needed beyond the binary and `config.toml`
- The `data/` directory (SQLite DB + uploads) lives alongside the binary and must persist across restarts
