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
	Database DatabaseConfig `toml:"database"`
	Slugs    SlugConfig     `toml:"slugs"`
}

type ServerConfig struct {
	Port         int    `toml:"port"`
	BaseURL      string `toml:"base_url"`
	ReadTimeout  string `toml:"read_timeout"`
	WriteTimeout string `toml:"write_timeout"`
}

type AdminConfig struct {
	Username     string `toml:"username"`
	PasswordHash string `toml:"password_hash"`
	SessionHours int    `toml:"session_hours"`
}

type DatabaseConfig struct {
	Path string `toml:"path"`
}

type SlugConfig struct {
	Length int `toml:"length"`
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

func Load(path string) (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:         8080,
			BaseURL:      "http://localhost:8080",
			ReadTimeout:  "5s",
			WriteTimeout: "10s",
		},
		Admin: AdminConfig{
			Username:     "admin",
			SessionHours: 24,
		},
		Database: DatabaseConfig{
			Path: "./data/urls.db",
		},
		Slugs: SlugConfig{
			Length: 3,
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
		return nil, fmt.Errorf("admin.password_hash is required in config")
	}

	return cfg, nil
}
