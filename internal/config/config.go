package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Admin    AdminConfig    `toml:"admin"`
	MCP      MCPConfig      `toml:"mcp"`
	API      APIConfig      `toml:"api"`
	Database DatabaseConfig `toml:"database"`
	Slugs    SlugConfig     `toml:"slugs"`
	Upload   UploadConfig   `toml:"upload"`
}

type ServerConfig struct {
	// Host is the address to listen on. Empty (the default) listens on all
	// interfaces; set it to 127.0.0.1 when a reverse proxy on the same machine
	// is the only thing that should reach the app.
	Host         string `toml:"host"`
	Port         int    `toml:"port"`
	BaseURL      string `toml:"base_url"`
	ReadTimeout  string `toml:"read_timeout"`
	WriteTimeout string `toml:"write_timeout"`
	// TrustProxy controls whether X-Forwarded-For / X-Real-Ip headers are
	// honoured for client IP detection. Only enable when the server sits
	// behind a trusted reverse proxy that sets these headers; otherwise a
	// client can spoof them to bypass rate limiting.
	TrustProxy bool `toml:"trust_proxy"`
	// ProxyHops is how many trusted proxies sit in front of the server when
	// TrustProxy is on (default 1). The client IP is taken that many entries
	// from the right of X-Forwarded-For; entries further left are
	// client-supplied and spoofable. Use 2 for e.g. Cloudflare -> nginx -> app.
	ProxyHops int `toml:"proxy_hops"`
	// MaxConcurrent caps the number of in-flight requests. Excess requests
	// get 503 instead of piling up and exhausting RAM/CPU on small hardware.
	MaxConcurrent int `toml:"max_concurrent"`
}

type AdminConfig struct {
	Username     string `toml:"username"`
	PasswordHash string `toml:"password_hash"`
	SessionHours int    `toml:"session_hours"`
}

type MCPConfig struct {
	Enabled bool   `toml:"enabled"`
	APIKey  string `toml:"api_key"`
}

// APIConfig lists the applications allowed to use the REST link API
// (/api/links). Each gets its own key, so one can be revoked without touching
// the others, and can only see and change the links it created itself. The
// [mcp] api_key remains the administrator's key: it reaches every link and
// the MCP endpoint.
type APIConfig struct {
	Keys []APIKey `toml:"keys"`
}

type APIKey struct {
	Name string `toml:"name"` // short identifier, recorded as the creator "api:<name>"
	Key  string `toml:"key"`  // long random secret
}

type DatabaseConfig struct {
	Path string `toml:"path"`
}

type SlugConfig struct {
	Length int `toml:"length"`
}

type UploadConfig struct {
	Dir     string `toml:"dir"`
	MaxSize int64  `toml:"max_size_mb"`
}

func (c *ServerConfig) ReadTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.ReadTimeout)
	if err != nil {
		return 5 * time.Second
	}
	return d
}

func (c *ServerConfig) WriteTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.WriteTimeout)
	if err != nil {
		return 10 * time.Second
	}
	return d
}

func validAPIKeyName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:          8080,
			BaseURL:       "http://localhost:8080",
			ReadTimeout:   "5s",
			WriteTimeout:  "10s",
			TrustProxy:    false,
			ProxyHops:     1,
			MaxConcurrent: 64,
		},
		Admin: AdminConfig{
			Username:     "admin",
			SessionHours: 24,
		},
		Database: DatabaseConfig{
			Path: "./data/urls.db",
		},
		Slugs: SlugConfig{
			Length: 7,
		},
		Upload: UploadConfig{
			Dir:     "./data/uploads",
			MaxSize: 50,
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Admin.PasswordHash == "" {
		return nil, fmt.Errorf("admin.password_hash is required in config (generate one with: glimmer --hash-password \"your-password\")")
	}

	seen := map[string]bool{}
	for _, k := range cfg.API.Keys {
		if !validAPIKeyName(k.Name) {
			return nil, fmt.Errorf("api.keys: name %q must be 1-32 characters of a-z, 0-9, '-' or '_'", k.Name)
		}
		if seen[k.Name] {
			return nil, fmt.Errorf("api.keys: duplicate name %q", k.Name)
		}
		seen[k.Name] = true
		if len(k.Key) < 24 {
			return nil, fmt.Errorf("api.keys: key for %q is too short (use at least 24 random characters)", k.Name)
		}
		if k.Key == cfg.MCP.APIKey {
			return nil, fmt.Errorf("api.keys: key for %q must differ from mcp.api_key", k.Name)
		}
	}

	if cfg.Server.ProxyHops < 1 {
		cfg.Server.ProxyHops = 1
	}

	if cfg.Server.MaxConcurrent <= 0 {
		cfg.Server.MaxConcurrent = 64
	}

	return cfg, nil
}
