# Glimmer — Claude Code Guide

## Project Overview

**Glimmer** is a single-binary Go URL shortener + pastebin with image upload support. It runs on a Raspberry Pi Zero (512 MB RAM, ARMv5) and compiles to a fully self-contained binary — no CGo, no external runtime, no system SQLite.

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
├── config.toml                      # Runtime config (not embedded — loaded at startup)
├── go.mod / go.sum
├── Makefile
├── data/
│   ├── urls.db                      # SQLite DB (auto-created)
│   └── uploads/                     # Uploaded images (auto-created)
└── internal/
    ├── config/config.go             # TOML config structs + loader
    ├── db/db.go                     # DB open/migrate; Link + Paste CRUD
    ├── slug/slug.go                 # Base-36 slug generation + validation
    └── server/
        ├── server.go                # Mux, middleware wiring, server start
        ├── handlers.go              # All HTTP handlers + rendering helpers
        ├── middleware.go            # Auth, CSRF, sessions, rate limiter
        ├── templates.go             # embed.FS for templates + static; initTemplates()
        ├── static/style.css         # Custom styles on top of Pico CSS 2
        └── templates/
            ├── index.html           # Public: URL shortener form
            ├── admin_login.html     # Login (dark, centered)
            ├── admin.html           # Dashboard: stats, tiles, links tables
            ├── admin_edit.html      # Edit a single link
            ├── admin_bin.html       # Pastes list
            ├── admin_bin_edit.html  # Create / edit paste (with image upload JS)
            ├── admin_uploads.html   # Image upload management
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

### No JS frameworks
All JavaScript is vanilla ES5-style (`var`, not `const`/`let`) written inline in templates. No build step, no npm, no bundler.

### CSS framework
[Pico CSS 2](https://picocss.com/) loaded from CDN. Custom overrides in `static/style.css`. Admin pages use `data-theme="dark"`.

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
- Max size enforced via `http.MaxBytesReader(w, r.Body, maxBytes)` at handler start
- MIME type validated via `http.DetectContentType` (magic bytes), not the client-supplied `Content-Type`
- Filenames are always `[32 hex chars].[ext]` — validated by `validUploadRe` regexp before any file operation
- Per-request timeouts extended to 120s via `http.NewResponseController` — global timeouts stay at 5s/10s

### Image resize
Only PNG (`.png`) and JPEG (`.jpg`) are resizable. Resize uses a nearest-neighbour scale implemented in stdlib (`image`, `image/color`, `image/jpeg`, `image/png`) — no external image library. GIF and WebP are served and deletable but not resizable.

### Admin tabs
Every admin page (`admin.html`, `admin_bin.html`, `admin_bin_edit.html`, `admin_uploads.html`) must include the full three-tab nav. Add the `active` class to the correct tab:
```html
<div class="admin-tabs">
    <a href="/admin"         class="tab-link [active]">Links</a>
    <a href="/admin/bin"     class="tab-link [active]">Pastes</a>
    <a href="/admin/uploads" class="tab-link [active]">Uploads</a>
</div>
```

### Database migrations
Migrations run automatically in `db.Open()`. New columns are added with `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`. Never drop columns — the schema must remain backwards compatible with existing `data/urls.db` files on deployed instances.

### Paste name / token validation
Both paste names and access tokens must pass `validPasteName()`: alphanumeric + hyphens + underscores, 1–64 chars.

### lowercasePath middleware
Redirects uppercase paths to lowercase (for QR-code URL compatibility). **Excludes** `/static/`, `/admin`, and `/uploads/` — these prefixes must stay in the exclusion list if new case-sensitive routes are added.

---

## Configuration (`config.toml`)

```toml
[server]
port          = 8888
base_url      = "http://localhost:8888"
read_timeout  = "5s"
write_timeout = "10s"

[admin]
username      = "admin"
password_hash = "$2a$10$..."   # bcrypt; generate with: ./glimmer --hash-password
session_hours = 24

[database]
path = "./data/urls.db"

[slugs]
length = 3

[upload]
dir         = "./data/uploads"
max_size_mb = 50
```

All fields have safe defaults in `config.Load()` — the only required field is `admin.password_hash`.

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
