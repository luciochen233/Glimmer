package server

import (
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"glimmer/internal/config"
	"glimmer/internal/db"
)

type Server struct {
	cfg      *config.Config
	db       *db.DB
	sessions *sessionStore
	limiter  *rateLimiter
}

func New(cfg *config.Config, database *db.DB) *Server {
	ttl := time.Duration(cfg.Admin.SessionHours) * time.Hour
	return &Server{
		cfg:      cfg,
		db:       database,
		sessions: newSessionStore(ttl),
		limiter:  newRateLimiter(1 * time.Second),
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// lowercasePath normalises the URL path to lowercase so that QR-code
// generated uppercase URLs (e.g. /BIN/TEST/TOKEN) resolve correctly.
// Static and admin paths are excluded to avoid breaking file serving.
func lowercasePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lower := strings.ToLower(r.URL.Path)
		if lower != r.URL.Path && !strings.HasPrefix(r.URL.Path, "/static/") && !strings.HasPrefix(lower, "/admin") {
			r.URL.Path = lower
			http.Redirect(w, r, r.URL.String(), http.StatusMovedPermanently)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Start() error {
	initTemplates()

	mux := http.NewServeMux()

	// Static files
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Public routes
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /shorten", s.handleShorten)

	// Admin auth
	mux.HandleFunc("GET /admin/login", s.handleLoginPage)
	mux.HandleFunc("POST /admin/login", s.handleLogin)
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

	// Pastebin public routes (before catch-all)
	mux.HandleFunc("GET /bin/{name}", s.handleBinView)
	mux.HandleFunc("GET /bin/{name}/{token}", s.handleBinView)

	// Redirect catch-all (must be last)
	mux.HandleFunc("GET /{slug}", s.handleRedirect)

	addr := fmt.Sprintf(":%d", s.cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  s.cfg.Server.ReadTimeoutDuration(),
		WriteTimeout: s.cfg.Server.WriteTimeoutDuration(),
	}

	log.Printf("Starting server on %s (base URL: %s)", addr, s.cfg.Server.BaseURL)
	srv.Handler = lowercasePath(securityHeaders(mux))
	return srv.ListenAndServe()
}
