# Glimmer

A lightweight, single-binary URL shortener and pastebin built in Go. Hosted on [luci.ooo](https://luci.ooo). Designed to run on a Raspberry Pi Zero (512MB RAM, single-core ARM) with minimal dependencies and no external runtime requirements.

---

## Features

### URL Shortener
- **Public:** Anyone can shorten a URL — no account required
- **Auto-scheme:** Submitting `google.com` automatically becomes `https://google.com`
- **Deduplication:** Submitting the same URL a second time returns the existing short link
- **QR codes:** Server-side SVG QR codes generated for every short link (crisp, no CDN)
- **Click tracking:** Every redirect increments the click counter
- **IP rate limiting:** Anonymous users limited to 1 short link per second; admin is never limited
- **Short slugs:** 3-character base-36 auto-generated (`a-z`, `0-9`); up to 64 characters for custom (admin only)

### Admin Dashboard
- Login protected with bcrypt-hashed password; centered dark-theme login card
- **Dark UI** throughout all admin pages (login, dashboard, edit forms, pastes)
- **Stats bar:** live counts of My Links, Anonymous Links, and Quick Links at a glance
- **My Links** card: links the admin created, with edit/delete/QR and click counts
- **Anonymous Links** card: links created by unauthenticated users, also manageable
- **Quick Links tile grid:** Top 20 most-clicked admin links displayed as tiles with favicon, destination hostname, and click count; one click to open
- **Pastes list:** format and token-status badges; empty states with guidance
- Create links with custom slugs (blocked for anonymous users)
- Edit slug and destination URL

### Pastebin
- **Admin:** Create, edit, delete pastes
- **Public:** Read-only access; optionally protected by a 12-character access token
- **Formats:** Markdown (GitHub-Flavored, rendered server-side) and plain text
- **Raw view:** Append `?raw=1` to any paste URL for `text/plain` output
- **Copy button:** One-click clipboard copy in the paste viewer
- **Image embedding:** Paste an image from clipboard or use the Upload Image button in the paste editor — image is uploaded and a markdown link is inserted at the cursor automatically

### Image Uploads
- **Admin-only upload:** Images up to 50 MB via file picker or clipboard paste in the paste editor
- **Supported formats:** PNG, JPEG, GIF, WebP (validated by magic bytes, not file extension)
- **Uploads management tab:** View all uploaded images with thumbnails, file size, and dimensions
- **Resize:** Downscale PNG or JPEG images in-place by setting a max dimension — re-encodes JPEG at 85% quality; no additional dependencies
- **Delete:** Remove individual images from the admin uploads tab
- **Copy markdown:** One-click copy of the `![](/uploads/...)` link for use in any paste
- **Public serving:** Uploaded images are served at `/uploads/{filename}` and render in public paste views
- **Per-upload timeout:** Upload requests get a 120-second read/write deadline via `http.NewResponseController`; all other routes keep the 5s/10s defaults

### Security
- CSRF protection on every state-changing POST (double-submit cookie pattern)
- `crypto/subtle.ConstantTimeCompare` for CSRF and paste token comparisons
- Session cookie: `HttpOnly`, `SameSite=Lax`, conditionally `Secure` when behind HTTPS proxy
- Security headers on all responses: `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy`
- Input length limits: URLs 2048 chars, paste content 512 KB
- URL scheme re-validated before every redirect (defense in depth)
- In-memory sessions with hourly cleanup goroutine

---

## Project Layout

```
glimmer/
├── main.go                         # Entry point; flag parsing; --hash-password helper
├── config.toml                     # Runtime configuration (port, admin creds, db path)
├── go.mod / go.sum
├── Makefile
├── deploy/
│   ├── install.sh                  # Install script (auto-detects systemd vs SysV)
│   ├── glimmer.service            # systemd unit file (Ubuntu 15.04+)
│   └── glimmer.init               # SysV init script (Ubuntu 14.04 and older)
├── data/
│   ├── urls.db                     # SQLite database (auto-created on first run)
│   └── uploads/                    # Uploaded images (auto-created on first run)
└── internal/
    ├── config/
    │   └── config.go               # TOML config loader; Config struct (incl. UploadConfig)
    ├── db/
    │   └── db.go                   # SQLite open/migrate; Link & Paste CRUD
    ├── slug/
    │   └── slug.go                 # Base-36 slug generation and validation
    └── server/
        ├── server.go               # HTTP mux registration; security headers middleware
        ├── handlers.go             # All request handlers; template rendering helpers
        ├── middleware.go           # requireAuth, requireCSRF, rateLimiter, sessionStore, clientIP
        ├── static/
        │   └── style.css           # Custom styles on top of Pico CSS (no JS)
        └── templates/
            ├── index.html          # Public home: shorten form + QR result
            ├── admin_login.html    # Login form (dark, centered card)
            ├── admin.html          # Admin dashboard: stats bar, tiles, links tables
            ├── admin_edit.html     # Edit a single link
            ├── admin_bin.html      # Pastes list with format/token badges
            ├── admin_bin_edit.html # Create / edit a paste (with image upload)
            ├── admin_uploads.html  # Upload management: list, resize, delete
            ├── bin_view.html       # Public paste viewer (GitHub Gist style)
            └── 404.html            # Not found page
```

---

## Dependencies

| Package | Purpose |
|---|---|
| `github.com/BurntSushi/toml` | TOML config parsing |
| `golang.org/x/crypto/bcrypt` | Admin password hashing |
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGo required) |
| `github.com/yuin/goldmark` | Markdown rendering (GFM + tables) |
| `rsc.io/qr` | Server-side QR code generation (SVG) |

All other entries in `go.sum` are transitive dependencies of the above.

---

## Getting Started

### Prerequisites

- Go 1.22 or later
- No CGo, no external C libraries, no system SQLite needed

> **Windows note:** If Go is not in your bash `PATH`, prefix every command:
> ```bash
> export PATH="/c/Program Files/Go/bin:$PATH"
> ```

### 1. Clone and install dependencies

```bash
git clone <repo-url>
cd url
go mod download
```

### 2. Configure

Edit `config.toml`:

```toml
[server]
port       = 8888
base_url   = "http://localhost:8888"   # Override by Host header at runtime
read_timeout  = "5s"
write_timeout = "10s"

[admin]
username      = "admin"
password_hash = "$2a$10$..."           # See step 3 below
session_hours = 24

[database]
path = "./data/urls.db"

[slugs]
length = 3   # Auto-generated slug length (characters)

[upload]
dir         = "./data/uploads"   # Where uploaded images are stored (auto-created)
max_size_mb = 50                 # Max image upload size in MB
```

### 3. Set your admin password

```bash
./glimmer --hash-password "your-secure-password"
# or via make:
make hash
```

Copy the printed bcrypt hash into `config.toml` under `admin.password_hash`.

### 4. Build and run

```bash
# Windows
make build
./glimmer.exe

# Linux / macOS
make build-linux
./glimmer

# Raspberry Pi Zero (ARMv5)
make build-arm
# Copy glimmer-arm to the Pi, then run it there
```

The server starts on `http://localhost:8888` (or whatever port you set).

---

## Makefile Targets

| Target | Description |
|---|---|
| `make build` | Build `glimmer.exe` for Windows (stripped binary) |
| `make build-linux` | Build `glimmer` for Linux x86-64 |
| `make build-arm` | Cross-compile `glimmer-arm` for Raspberry Pi Zero (ARMv5) |
| `make run` | Build then immediately run on Windows |
| `make clean` | Remove all compiled binaries |
| `make hash` | Interactively hash a password for `config.toml` |

---

## Deploying to Raspberry Pi Zero

```bash
# On your dev machine — cross-compile for ARMv5 (Pi Zero)
make build-arm

# Copy binary, config, and deploy scripts to the Pi
scp glimmer-arm    pi@raspberrypi.local:~/glimmer
scp config.toml     pi@raspberrypi.local:~/config.toml
scp -r deploy/      pi@raspberrypi.local:~/deploy/
```

The binary is fully self-contained — no Go runtime, no system SQLite, no shared libraries required.

### Install as a system service

The `deploy/` directory contains an install script and service files compatible with Ubuntu/Debian (both modern systemd and older SysV init systems):

```bash
# On the Pi (or any Ubuntu/Debian host):
chmod +x glimmer
sudo ./deploy/install.sh
```

The script will:
- Create a dedicated `glimmer` system user
- Install the binary to `/opt/glimmer/`
- Install the config to `/etc/glimmer/config.toml`
- Create `/var/lib/glimmer/` for the database
- Register and enable the service (systemd or SysV, auto-detected)

**After install**, set your admin password hash in the config:

```bash
/opt/glimmer/glimmer -hash-password "your-secure-password"
# Paste the output into /etc/glimmer/config.toml → admin.password_hash
sudo systemctl start glimmer
```

**Service management:**

```bash
sudo systemctl status glimmer
sudo systemctl restart glimmer
sudo journalctl -u glimmer -f      # live logs
```

---

## Behind a Reverse Proxy — nginx

The app reads `Host` and `X-Forwarded-Proto` headers automatically to build correct short URLs and set `Secure` cookies. No `base_url` change is needed in `config.toml` when running behind a proxy.

### 1. Install nginx

```bash
sudo apt update
sudo apt install -y nginx
```

### 2. Create a site config

```bash
sudo nano /etc/nginx/sites-available/glimmer
```

Paste the following, replacing `short.example.com` with your domain:

```nginx
server {
    listen 80;
    server_name short.example.com;

    location / {
        proxy_pass         http://127.0.0.1:8888;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;

        proxy_read_timeout  120s;   # Allow time for large image uploads (up to 50 MB)
        proxy_send_timeout  120s;
    }
}
```

```bash
sudo ln -s /etc/nginx/sites-available/glimmer /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

At this point the site is reachable over HTTP. Proceed to step 3 to add HTTPS.

### 3. Add HTTPS with Let's Encrypt (certbot)

```bash
sudo apt install -y certbot python3-certbot-nginx
sudo certbot --nginx -d short.example.com
```

Certbot will automatically update the nginx config to add SSL and redirect HTTP → HTTPS. Certificates auto-renew via a systemd timer or cron job that certbot installs.

> **No public domain?** If running on a local network (e.g. Raspberry Pi at home), skip certbot and use a self-signed certificate instead:
> ```bash
> sudo openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
>   -keyout /etc/ssl/private/glimmer.key \
>   -out /etc/ssl/certs/glimmer.crt \
>   -subj "/CN=glimmer"
> ```
> Then reference them manually in the nginx config (`ssl_certificate`, `ssl_certificate_key`).

### 4. Recommended production nginx config

After certbot runs, your config will look roughly like this. The additions below tighten security headers and enable compression:

```nginx
server {
    listen 80;
    server_name short.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name short.example.com;

    # TLS — managed by certbot
    ssl_certificate     /etc/letsencrypt/live/short.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/short.example.com/privkey.pem;
    include             /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam         /etc/letsencrypt/ssl-dhparams.pem;

    # Compression
    gzip on;
    gzip_types text/css text/html application/javascript;
    gzip_min_length 1024;

    # Security headers
    add_header Strict-Transport-Security "max-age=31536000" always;
    add_header X-Content-Type-Options    "nosniff"          always;
    add_header X-Frame-Options           "DENY"             always;
    add_header Referrer-Policy           "strict-origin"    always;

    location / {
        proxy_pass         http://127.0.0.1:8888;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;

        proxy_read_timeout  120s;   # Allow time for large image uploads (up to 50 MB)
        proxy_send_timeout  120s;
        proxy_buffering     off;
    }
}
```

```bash
sudo nginx -t && sudo systemctl reload nginx
```

### 5. Update config.toml base_url (optional)

`base_url` is only used as a fallback when no `Host` header is present (e.g. direct `curl` on localhost). When nginx is in front, the `Host` header is always forwarded, so the generated short links will automatically use your domain. If you want the fallback to also be correct:

```toml
[server]
base_url = "https://short.example.com"
```

Then restart the service: `sudo systemctl restart glimmer`

### Verify everything works

```bash
# Confirm redirect (should return 301 to https)
curl -I http://short.example.com/

# Confirm HTTPS proxy is working
curl -I https://short.example.com/

# Check glimmer is running and nginx can reach it
sudo systemctl status glimmer
sudo systemctl status nginx
```

### Cloudflare note

If proxying through Cloudflare, `X-Forwarded-For` is safe to trust for rate limiting since Cloudflare controls the header. For direct-to-internet deployments without a trusted proxy, clients could spoof `X-Forwarded-For` to bypass IP rate limits — see "Possible Future Improvements".

---

## URL Routes

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/` | Public | Home / shorten form |
| `POST` | `/shorten` | Public | Create short link |
| `GET` | `/{slug}` | Public | Redirect to destination |
| `GET` | `/bin/{name}` | Public | View paste |
| `GET` | `/bin/{name}/{token}` | Public | View token-protected paste |
| `GET` | `/uploads/{filename}` | Public | Serve uploaded image |
| `GET` | `/admin/login` | Public | Login form |
| `POST` | `/admin/login` | Public | Submit login |
| `POST` | `/admin/logout` | Admin | Logout (CSRF protected) |
| `GET` | `/admin` | Admin | Dashboard: links + tiles |
| `GET` | `/admin/edit/{id}` | Admin | Edit link form |
| `POST` | `/admin/edit/{id}` | Admin | Save link edit (CSRF) |
| `POST` | `/admin/delete/{id}` | Admin | Delete link (CSRF) |
| `GET` | `/admin/qr/{slug}` | Admin | QR code SVG for a short link |
| `GET` | `/admin/bin` | Admin | Pastes list |
| `GET` | `/admin/bin/new` | Admin | New paste form |
| `POST` | `/admin/bin/new` | Admin | Create paste (CSRF) |
| `GET` | `/admin/bin/edit/{id}` | Admin | Edit paste form |
| `POST` | `/admin/bin/edit/{id}` | Admin | Save paste edit (CSRF) |
| `POST` | `/admin/bin/delete/{id}` | Admin | Delete paste (CSRF) |
| `GET` | `/admin/uploads` | Admin | Uploads management list |
| `POST` | `/admin/upload` | Admin | Upload an image (returns JSON) |
| `POST` | `/admin/uploads/delete/{filename}` | Admin | Delete an uploaded image (CSRF) |
| `POST` | `/admin/uploads/resize/{filename}` | Admin | Resize a PNG/JPEG in-place (returns JSON) |
| `GET` | `/static/*` | Public | CSS / static assets |

---

## Database Schema

SQLite, WAL mode, single writer, auto-migrated on startup.

```sql
CREATE TABLE links (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT NOT NULL UNIQUE,
    url        TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT 'anonymous',  -- 'admin' or 'anonymous'
    clicks     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE pastes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,   -- URL slug, e.g. "my-script"
    title      TEXT NOT NULL DEFAULT '',
    content    TEXT NOT NULL DEFAULT '',
    format     TEXT NOT NULL DEFAULT 'markdown',  -- 'markdown' | 'text'
    token      TEXT,                   -- NULL = public; non-null = token required
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

To reset the database (e.g. during development):

```bash
rm -f data/urls.db
# Restart the server — it will recreate the DB automatically
```

---

## Configuration Reference

```toml
[server]
port          = 8888          # TCP port to listen on
base_url      = "http://localhost:8888"  # Fallback base URL (overridden by Host header)
read_timeout  = "5s"          # HTTP read timeout (uploads extend their own deadline to 120s)
write_timeout = "10s"         # HTTP write timeout (uploads extend their own deadline to 120s)

[admin]
username      = "admin"       # Admin login username
password_hash = "$2a$10$..."  # bcrypt hash — generate with: ./glimmer --hash-password
session_hours = 24            # How long admin sessions last

[database]
path = "./data/urls.db"       # Path to SQLite file (directory auto-created)

[slugs]
length = 3                    # Length of auto-generated slugs (recommended: 3–6)

[upload]
dir          = "./data/uploads"  # Directory for uploaded images (auto-created)
max_size_mb  = 50                # Max upload size in MB
```

---

## Security Notes

- **Change the default password.** The default hash in `config.toml` is for the password `admin`. Run `./glimmer --hash-password` immediately.
- **CSRF protection** uses the double-submit cookie pattern. Every admin POST requires a `csrf_token` form field matching the `csrf` cookie value, compared with constant-time equality.
- **Sessions** are stored in memory. They are lost when the server restarts (acceptable for single-user use). Session IDs are 32 bytes of `crypto/rand`.
- **Paste tokens** are 12-character random strings. Token comparison uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks.
- **Rate limiting** is 1 short link per second per IP for anonymous users. The rate limiter table is lazily cleaned up at 10,000 entries.

---

## Development Tips

### Rebuild and restart quickly (Windows)

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
pkill -f glimmer.exe; sleep 1
go build -o glimmer.exe ./ && ./glimmer.exe &
```

### Check logs

```bash
# If started with output redirected:
tail -f /tmp/glimmer.log
```

### Useful one-liners

```bash
# Hash a new password
go run . --hash-password "mynewpassword"

# Check what's listening on 8888 (Windows PowerShell)
Get-NetTCPConnection -LocalPort 8888 | Select-Object OwningProcess

# Kill whatever is on port 8888 (Windows PowerShell)
Get-NetTCPConnection -LocalPort 8888 | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force }
```

---

## Possible Future Improvements

- [ ] Configurable trusted-proxy IP list (for safer `X-Forwarded-For` handling)
- [ ] Persistent sessions (write to SQLite) so restarts don't log out admin
- [ ] Optional expiry date per link
- [ ] Bulk delete / bulk import via CSV
- [ ] Syntax highlighting for code pastes
- [ ] Animated GIF resize support
- [ ] WebP encode support for resize (requires an external library)
- [ ] Upload storage quota display in the uploads tab
