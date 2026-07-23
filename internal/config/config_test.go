package config

import (
	"os"
	"path/filepath"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrecedenceFlagsOverEnvOverFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.toml", `
base_url = "https://file.example"
workspace = "file-ws"
project = "file-proj"
api_key = "file-key"
`)
	env := map[string]string{
		"PLANE_BASE_URL":  "https://env.example",
		"PLANE_WORKSPACE": "env-ws",
		"PLANE_API_KEY":   "env-key",
	}

	cfg, err := Load(Flags{BaseURL: "https://flag.example", ConfigPath: cfgPath}, envMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://flag.example" {
		t.Errorf("BaseURL = %s, want flag value", cfg.BaseURL)
	}
	if cfg.Workspace != "env-ws" {
		t.Errorf("Workspace = %s, want env value", cfg.Workspace)
	}
	if cfg.Project != "file-proj" {
		t.Errorf("Project = %s, want file value", cfg.Project)
	}
	if cfg.APIKey() != "env-key" {
		t.Errorf("APIKey = %s, want env value over file", cfg.APIKey())
	}
}

func TestKeyFileWinsWithinEnvTierAndStripsWhitespace(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeFile(t, dir, "key", "  secret-from-file\n\n")
	env := map[string]string{
		"PLANE_API_KEY":      "inline-key",
		"PLANE_API_KEY_FILE": keyPath,
	}
	cfg, err := Load(Flags{ConfigPath: filepath.Join(dir, "absent.toml")}, envMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey() != "secret-from-file" {
		t.Errorf("APIKey = %q, want trimmed file content winning over inline env", cfg.APIKey())
	}
}

func TestEmptyKeyFileIsConfigError(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeFile(t, dir, "key", "\n")
	_, err := Load(Flags{APIKeyFile: keyPath, ConfigPath: filepath.Join(dir, "absent.toml")}, envMap(nil))
	if _, ok := err.(*ConfigError); !ok {
		t.Fatalf("err = %v, want ConfigError", err)
	}
}

func TestProjectOverridesApply(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.toml", `
base_url = "https://main.example"
workspace = "main-ws"
api_key = "main-key"

[project_overrides."Side Project"]
workspace = "side-ws"
api_key = "side-key"
`)
	cfg, err := Load(Flags{Project: "side project", ConfigPath: cfgPath}, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace != "side-ws" || cfg.APIKey() != "side-key" {
		t.Errorf("override not applied: ws=%s key=%s", cfg.Workspace, cfg.APIKey())
	}
	if cfg.BaseURL != "https://main.example" {
		t.Errorf("BaseURL = %s, want fallback to top-level", cfg.BaseURL)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.toml")

	cfg, _ := Load(Flags{ConfigPath: absent}, envMap(map[string]string{
		"PLANE_API_KEY": "k",
	}))
	if err := cfg.Validate(true); err == nil {
		t.Error("want error for missing base URL")
	}

	cfg, _ = Load(Flags{ConfigPath: absent}, envMap(map[string]string{
		"PLANE_BASE_URL": "not a url", "PLANE_API_KEY": "k",
	}))
	if err := cfg.Validate(true); err == nil {
		t.Error("want error for invalid base URL")
	}

	cfg, _ = Load(Flags{ConfigPath: absent}, envMap(map[string]string{
		"PLANE_BASE_URL": "https://x.example", "PLANE_API_KEY": "k",
	}))
	if err := cfg.Validate(true); err == nil {
		t.Error("want error for missing workspace")
	}
	if err := cfg.Validate(false); err != nil {
		t.Errorf("workspace should be optional here: %v", err)
	}

	cfg, _ = Load(Flags{ConfigPath: absent}, envMap(map[string]string{
		"PLANE_BASE_URL": "https://x.example", "PLANE_WORKSPACE": "ws",
	}))
	if err := cfg.Validate(true); err == nil {
		t.Error("want error for missing API key")
	}
}

func TestBaseURLTrailingSlashTrimmed(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(Flags{BaseURL: "https://x.example/", ConfigPath: filepath.Join(dir, "absent.toml")},
		envMap(map[string]string{"PLANE_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://x.example" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
}
