// Package config resolves plane-cli configuration with the precedence
// flags > environment > config file (~/.config/plane-cli/config.toml).
//
// The API key is never accepted via argv; it comes from PLANE_API_KEY,
// PLANE_API_KEY_FILE / --api-key-file (systemd LoadCredential-friendly),
// or the config file. Within one precedence tier a key file wins over an
// inline key.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// File is the on-disk TOML shape.
type File struct {
	BaseURL    string `toml:"base_url"`
	Workspace  string `toml:"workspace"`
	Project    string `toml:"project"`
	APIKey     string `toml:"api_key"`
	APIKeyFile string `toml:"api_key_file"`

	// ProjectOverrides applies extra settings when the selected project
	// (after flag/env resolution) matches a key, case-insensitively.
	ProjectOverrides map[string]Override `toml:"project_overrides"`
}

type Override struct {
	BaseURL    string `toml:"base_url"`
	Workspace  string `toml:"workspace"`
	APIKey     string `toml:"api_key"`
	APIKeyFile string `toml:"api_key_file"`
}

// Flags carries the values of global flags that participate in config
// resolution. Empty string means "not set".
type Flags struct {
	BaseURL    string
	Workspace  string
	Project    string
	APIKeyFile string
	ConfigPath string
}

// Config is the fully resolved configuration.
type Config struct {
	BaseURL   string
	Workspace string
	Project   string // project name, identifier, or UUID; resolved lazily per command

	apiKey string
}

// APIKey returns the resolved key. Deliberately not a struct field so that
// %+v / JSON dumps of Config never contain key material.
func (c *Config) APIKey() string { return c.apiKey }

// ConfigError is a resolution/validation failure; maps to exit code 2.
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

func DefaultPath(getenv func(string) string) string {
	if p := getenv("PLANE_CONFIG"); p != "" {
		return p
	}
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "plane-cli", "config.toml")
}

// Load resolves configuration. getenv is injectable for tests.
func Load(fl Flags, getenv func(string) string) (*Config, error) {
	var f File
	path := fl.ConfigPath
	if path == "" {
		path = DefaultPath(getenv)
	}
	if path != "" {
		if _, err := toml.DecodeFile(path, &f); err != nil && !os.IsNotExist(err) {
			return nil, &ConfigError{Msg: fmt.Sprintf("config file %s: %v", path, err)}
		}
	}

	pick := func(flag, env, file string) string {
		if flag != "" {
			return flag
		}
		if env != "" {
			return env
		}
		return file
	}

	cfg := &Config{}
	cfg.Project = pick(fl.Project, getenv("PLANE_PROJECT"), f.Project)

	// Apply a per-project override as an extra "file" layer.
	ov := Override{}
	if cfg.Project != "" {
		for name, o := range f.ProjectOverrides {
			if strings.EqualFold(name, cfg.Project) {
				ov = o
				break
			}
		}
	}
	fileBase := firstNonEmpty(ov.BaseURL, f.BaseURL)
	fileWS := firstNonEmpty(ov.Workspace, f.Workspace)
	fileKey := firstNonEmpty(ov.APIKey, f.APIKey)
	fileKeyFile := firstNonEmpty(ov.APIKeyFile, f.APIKeyFile)

	cfg.BaseURL = strings.TrimRight(pick(fl.BaseURL, getenv("PLANE_BASE_URL"), fileBase), "/")
	cfg.Workspace = pick(fl.Workspace, getenv("PLANE_WORKSPACE"), fileWS)

	key, err := resolveKey(fl.APIKeyFile, getenv, fileKey, fileKeyFile)
	if err != nil {
		return nil, err
	}
	cfg.apiKey = key
	return cfg, nil
}

func resolveKey(flagKeyFile string, getenv func(string) string, fileKey, fileKeyFile string) (string, error) {
	// flags > env > config file; within a tier, key file > inline key.
	if flagKeyFile != "" {
		return readKeyFile(flagKeyFile)
	}
	if p := getenv("PLANE_API_KEY_FILE"); p != "" {
		return readKeyFile(p)
	}
	if k := getenv("PLANE_API_KEY"); k != "" {
		return strings.TrimSpace(k), nil
	}
	if fileKeyFile != "" {
		return readKeyFile(fileKeyFile)
	}
	if fileKey != "" {
		return strings.TrimSpace(fileKey), nil
	}
	return "", nil
}

func readKeyFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", &ConfigError{Msg: fmt.Sprintf("api key file: %v", err)}
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", &ConfigError{Msg: fmt.Sprintf("api key file %s is empty", path)}
	}
	return key, nil
}

// Validate checks the parts of the configuration a command declared it needs.
func (c *Config) Validate(needWorkspace bool) error {
	if c.BaseURL == "" {
		return &ConfigError{Msg: "base URL not configured (set --base-url, PLANE_BASE_URL, or base_url in config.toml)"}
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return &ConfigError{Msg: fmt.Sprintf("base URL %q is not a valid http(s) URL", c.BaseURL)}
	}
	if c.apiKey == "" {
		return &ConfigError{Msg: "API key not configured (set PLANE_API_KEY, PLANE_API_KEY_FILE/--api-key-file, or api_key_file in config.toml)"}
	}
	if needWorkspace && c.Workspace == "" {
		return &ConfigError{Msg: "workspace not configured (set --workspace, PLANE_WORKSPACE, or workspace in config.toml)"}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
