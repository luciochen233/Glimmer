package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"glimmer/internal/config"
	"glimmer/internal/db"
	"glimmer/internal/httpguard"
)

type Server struct {
	cfg           *config.Config
	db            *db.DB
	sessions      *sessionStore
	limiter       *rateLimiter
	authLimiter   *rateLimiter
	pwGate        *httpguard.PasswordGate
	reqlog        *requestLog
	globalLimiter *globalRateLimiter
	// tokenLimiter throttles wrong paste-token guesses per IP. Separate from
	// authLimiter so a paste viewer cannot influence the login throttle.
	tokenLimiter *rateLimiter
	// thumbMu serialises thumbnail generation: a full image decode can cost
	// ~100 MB, so concurrent requests for uncached thumbnails must not stack.
	thumbMu sync.Mutex
}

func New(cfg *config.Config, database *db.DB) *Server {
	ttl := time.Duration(cfg.Admin.SessionHours) * time.Hour
	return &Server{
		cfg:         cfg,
		db:          database,
		sessions:    newSessionStore(database, ttl),
		limiter:     newRateLimiter(1 * time.Second),
		authLimiter: newRateLimiter(1 * time.Second),
		// At most NumCPU concurrent bcrypt operations, so password checks
		// cannot peg the CPU on small hardware.
		pwGate:        httpguard.NewPasswordGate(runtime.NumCPU()),
		reqlog:        newRequestLog(100),
		globalLimiter: newGlobalRateLimiter(10, 10), // 10 shortens/sec globally, burst 10; admin bypasses
		tokenLimiter:  newRateLimiter(1 * time.Second),
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SAMEORIGIN (not DENY) so the admin Pastes page can preview a paste in
		// an in-page iframe; cross-site framing is still blocked.
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// All assets are served from the same origin (CSS and JS are embedded),
		// so the app works fully offline and we can lock the origin down.
		// img-src allows data: and https: so uploaded/remote images and
		// favicons still render, and blob: so the client-side image compressor
		// (canvas) can load a selected file before upload. script-src is
		// 'self' only — all JS lives in /static/js/ files; do not add inline
		// <script> blocks or on*= attributes to templates, they will be
		// blocked. style-src keeps 'unsafe-inline' for the style="" attributes
		// throughout the templates (style injection is far lower risk than
		// script injection). object-src/base-uri are pinned to blunt
		// injection; frame-ancestors 'self' permits the same-origin paste
		// preview iframe while blocking cross-site clickjacking.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"img-src 'self' data: https: blob:; "+
				"style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; "+
				"object-src 'none'; "+
				"base-uri 'none'; "+
				"form-action 'self'; "+
				"frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

// lowercasePath normalises the URL path to lowercase so that QR-code
// generated uppercase URLs (e.g. /BIN/TEST/TOKEN) resolve correctly. Static,
// admin and upload paths are excluded to avoid breaking file serving; keep
// these prefixes in the list if new case-sensitive routes are added.
func lowercasePath(next http.Handler) http.Handler {
	return httpguard.LowercasePath(next, "/static/", "/admin", "/uploads/")
}

// noDirFS serves files but never directory listings.
type noDirFS = httpguard.NoDirFS

// isUploadRoute matches the two upload endpoints exactly: they set their own
// (larger) body cap. A prefix match would also exempt /admin/uploads/*
// (delete, resize) from the ordinary cap.
func isUploadRoute(r *http.Request) bool {
	return r.URL.Path == "/admin/upload" || r.URL.Path == "/admin/upload-file"
}

func (s *Server) Start() error {
	initTemplates()
	s.backfillPasteSummaries()

	// Ensure upload directory exists
	if err := os.MkdirAll(s.cfg.Upload.Dir, 0755); err != nil {
		return fmt.Errorf("creating upload directory: %w", err)
	}

	mux := http.NewServeMux()

	// Static files
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(noDirFS{FS: http.FS(staticSub)})))

	// Uploaded files (public — appear in public pastes). Non-image files are
	// served as attachments to prevent same-origin XSS via HTML/SVG/JS uploads.
	// The more specific /uploads/thumb/ route wins for thumbnails.
	mux.HandleFunc("GET /uploads/thumb/{filename}", s.handleThumb)
	mux.HandleFunc("GET /uploads/", s.handleUploadsServe)

	// Public routes
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /shorten", s.handleShorten)

	// Admin auth
	mux.HandleFunc("GET /admin/login", s.handleLoginPage)
	mux.HandleFunc("POST /admin/login", s.requireCSRF(s.handleLogin))
	mux.HandleFunc("POST /admin/logout", s.requireAuth(s.requireCSRF(s.handleLogout)))

	// Admin routes (protected)
	mux.HandleFunc("GET /admin", s.requireAuth(s.handleAdmin))
	mux.HandleFunc("GET /admin/edit/{id}", s.requireAuth(s.handleAdminEdit))
	mux.HandleFunc("POST /admin/edit/{id}", s.requireAuth(s.requireCSRF(s.handleAdminEditSave)))
	mux.HandleFunc("POST /admin/delete/{id}", s.requireAuth(s.requireCSRF(s.handleAdminDelete)))
	mux.HandleFunc("GET /admin/qr/{slug}", s.requireAuth(s.handleQR))

	// Pastebin admin routes (protected)
	mux.HandleFunc("GET /admin/bin", s.requireAuth(s.handleAdminBin))
	mux.HandleFunc("GET /admin/bin/new", s.requireAuth(s.handleAdminBinNew))
	mux.HandleFunc("POST /admin/bin/new", s.requireAuth(s.requireCSRF(s.handleAdminBinCreate)))
	mux.HandleFunc("GET /admin/bin/edit/{id}", s.requireAuth(s.handleAdminBinEdit))
	mux.HandleFunc("POST /admin/bin/edit/{id}", s.requireAuth(s.requireCSRF(s.handleAdminBinSave)))
	mux.HandleFunc("POST /admin/bin/delete/{id}", s.requireAuth(s.requireCSRF(s.handleAdminBinDelete)))
	mux.HandleFunc("GET /admin/bin/qr/{name}", s.requireAuth(s.handleBinQR))
	mux.HandleFunc("POST /admin/upload", s.requireAuth(s.handleUpload))
	mux.HandleFunc("POST /admin/upload-file", s.requireAuth(s.handleFileUpload))
	mux.HandleFunc("GET /admin/uploads", s.requireAuth(s.handleAdminUploads))
	mux.HandleFunc("POST /admin/uploads/delete/{filename}", s.requireAuth(s.requireCSRF(s.handleAdminUploadDelete)))
	mux.HandleFunc("POST /admin/uploads/resize/{filename}", s.requireAuth(s.handleAdminUploadResize))
	mux.HandleFunc("GET /admin/log", s.requireAuth(s.handleAdminLog))

	if s.cfg.MCP.Enabled {
		mux.HandleFunc("POST /mcp", s.handleMCP)
		// REST surface for programmatic short-link creation. Shares the
		// same API key as /mcp. Only registered when the API key is set so
		// a misconfigured server never exposes the endpoint unauthenticated.
		// The route is intentionally not documented in any public-facing
		// template; visitors who don't know it exists get a 404.
		if s.cfg.MCP.APIKey != "" {
			mux.HandleFunc("POST /api/links", s.apiCreateLink)
		}
	}

	// Pastebin public routes (before catch-all)
	mux.HandleFunc("GET /bin/{name}", s.handleBinView)
	mux.HandleFunc("GET /bin/{name}/{token}", s.handleBinView)

	// Redirect catch-all (must be last)
	mux.HandleFunc("GET /{slug}", s.handleRedirect)

	// Empty host = all interfaces. Behind a reverse proxy set server.host to
	// 127.0.0.1 so the app (and its trust in X-Forwarded-For) is only
	// reachable through the proxy.
	addr := net.JoinHostPort(s.cfg.Server.Host, strconv.Itoa(s.cfg.Server.Port))

	// Middleware chain (outermost first):
	//   limitConcurrency — cap in-flight requests (memory/CPU guard)
	//   limitBody        — cap body size for non-upload POSTs
	//   lowercasePath    — normalise case for QR URLs
	//   securityHeaders  — CSP + hardening headers
	//   logRequests      — in-RAM request log
	//   crossOriginGuard — reject cross-origin state-changing browser requests
	const maxBodyBytes = 1 << 20 // 1 MB for ordinary requests
	handler := httpguard.LimitConcurrency(s.cfg.Server.MaxConcurrent,
		httpguard.LimitBody(maxBodyBytes, isUploadRoute,
			lowercasePath(securityHeaders(
				s.logRequests(
					crossOriginGuard(s.cfg.Server.BaseURL, mux))))))

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       s.cfg.Server.ReadTimeoutDuration(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      s.cfg.Server.WriteTimeoutDuration(),
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16, // 64 KB
	}

	log.Printf("Starting server on %s (base URL: %s)", addr, s.cfg.Server.BaseURL)

	// Graceful shutdown: on SIGINT/SIGTERM stop accepting connections, let
	// in-flight requests drain (bounded), then return so deferred cleanup
	// (e.g. closing the SQLite DB) runs instead of being killed mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	}
}
