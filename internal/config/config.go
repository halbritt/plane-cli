// Package config resolves plane-cli configuration with the precedence
// flags > environment > config file (~/.config/plane-cli/config.toml).
//
// The config file may define named connection profiles ([instance.<name>]
// stanzas) for talking to more than one Plane deployment; --instance /
// PLANE_INSTANCE selects one, default_instance applies otherwise, and a
// project override can route a project to an instance.
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
	"sort"
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

	// DefaultInstance names the [instance.*] stanza used when neither
	// --instance nor PLANE_INSTANCE selects one.
	DefaultInstance string `toml:"default_instance"`

	// Instances are named connection profiles ([instance.<name>]); the
	// selected one supplies base_url/workspace/key ahead of the top-level
	// file values.
	Instances map[string]Instance `toml:"instance"`

	// ProjectOverrides applies extra settings when the selected project
	// (after flag/env resolution) matches a key, case-insensitively.
	ProjectOverrides map[string]Override `toml:"project_overrides"`
}

type Instance struct {
	BaseURL    string `toml:"base_url"`
	Workspace  string `toml:"workspace"`
	APIKey     string `toml:"api_key"`
	APIKeyFile string `toml:"api_key_file"`
}

type Override struct {
	// Instance routes the project to a named [instance.*] stanza. Ignored
	// when --instance/PLANE_INSTANCE picked one explicitly.
	Instance string `toml:"instance"`

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
	Instance   string
	APIKeyFile string
	ConfigPath string
}

// Config is the fully resolved configuration.
type Config struct {
	BaseURL   string
	Workspace string
	Project   string // project name, identifier, or UUID; resolved lazily per command
	Instance  string // canonical name of the selected [instance.*] stanza, if any

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

	// Per-project override; may route to a named instance.
	ov := Override{}
	if cfg.Project != "" {
		for name, o := range f.ProjectOverrides {
			if strings.EqualFold(name, cfg.Project) {
				ov = o
				break
			}
		}
	}

	// Select the instance: explicit flag/env beats a project override's
	// routing, which beats default_instance. An override that routes to a
	// different instance than an explicit selection is ignored entirely —
	// its remaining fields were written for the instance it names.
	instName := firstNonEmpty(fl.Instance, getenv("PLANE_INSTANCE"))
	explicit := instName != ""
	if !explicit {
		instName = firstNonEmpty(ov.Instance, f.DefaultInstance)
	}
	if explicit && ov.Instance != "" && !strings.EqualFold(ov.Instance, instName) {
		ov = Override{}
	}
	inst := Instance{}
	if instName != "" {
		found := false
		for name, i := range f.Instances {
			if strings.EqualFold(name, instName) {
				inst, found = i, true
				cfg.Instance = name
				break
			}
		}
		if !found {
			return nil, &ConfigError{Msg: fmt.Sprintf(
				"instance %q not defined in config file (available: %s)",
				instName, strings.Join(instanceNames(f.Instances), ", "))}
		}
	}

	// File tier layering, strongest first: project override > instance >
	// top-level keys.
	fileBase := firstNonEmpty(ov.BaseURL, inst.BaseURL, f.BaseURL)
	fileWS := firstNonEmpty(ov.Workspace, inst.Workspace, f.Workspace)

	cfg.BaseURL = strings.TrimRight(pick(fl.BaseURL, getenv("PLANE_BASE_URL"), fileBase), "/")
	cfg.Workspace = pick(fl.Workspace, getenv("PLANE_WORKSPACE"), fileWS)

	key, err := resolveKey(fl.APIKeyFile, getenv, []keySource{
		{ov.APIKey, ov.APIKeyFile},
		{inst.APIKey, inst.APIKeyFile},
		{f.APIKey, f.APIKeyFile},
	})
	if err != nil {
		return nil, err
	}
	cfg.apiKey = key
	return cfg, nil
}

func instanceNames(m map[string]Instance) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"none"}
	}
	return names
}

// keySource is one file-tier layer's key material.
type keySource struct{ key, file string }

func resolveKey(flagKeyFile string, getenv func(string) string, fileLayers []keySource) (string, error) {
	// flags > env > config file layers; within a layer, key file > inline key.
	if flagKeyFile != "" {
		return readKeyFile(flagKeyFile)
	}
	if p := getenv("PLANE_API_KEY_FILE"); p != "" {
		return readKeyFile(p)
	}
	if k := getenv("PLANE_API_KEY"); k != "" {
		return strings.TrimSpace(k), nil
	}
	for _, l := range fileLayers {
		if l.file != "" {
			return readKeyFile(l.file)
		}
		if l.key != "" {
			return strings.TrimSpace(l.key), nil
		}
	}
	return "", nil
}

func readKeyFile(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
