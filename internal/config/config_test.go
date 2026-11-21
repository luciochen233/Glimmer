package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadString(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[admin]\npassword_hash = \"x\"\n"+body), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestAPIKeysAreValidated(t *testing.T) {
	good := "[[api.keys]]\nname = \"solis\"\nkey = \"0123456789abcdef01234567\"\n"
	cfg, err := loadString(t, good)
	if err != nil || len(cfg.API.Keys) != 1 || cfg.API.Keys[0].Name != "solis" {
		t.Fatalf("valid key rejected: %v %+v", err, cfg)
	}
	for name, body := range map[string]string{
		"short key":      "[[api.keys]]\nname = \"solis\"\nkey = \"short\"\n",
		"bad name":       "[[api.keys]]\nname = \"So Lis\"\nkey = \"0123456789abcdef01234567\"\n",
		"missing name":   "[[api.keys]]\nkey = \"0123456789abcdef01234567\"\n",
		"duplicate name": good + good,
		"same as mcp":    "[mcp]\napi_key = \"0123456789abcdef01234567\"\n" + good,
	} {
		if _, err := loadString(t, body); err == nil || !strings.Contains(err.Error(), "api.keys") {
			t.Errorf("%s: expected an api.keys error, got %v", name, err)
		}
	}
}
