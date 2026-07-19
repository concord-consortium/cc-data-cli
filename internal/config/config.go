package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

// CurrentVersion is the config schema version this binary writes.
const CurrentVersion = 1

// DefaultServerURL is the built-in production report-server origin.
const DefaultServerURL = "https://report-server.concord.org"

// Config is the on-disk ~/.config/cc-data/config.json.
type Config struct {
	Version       int    `json:"version"`
	DefaultPortal string `json:"default_portal,omitempty"`
	DataRoot      string `json:"data_root,omitempty"`
	ServerURL     string `json:"server_url,omitempty"`
}

// homeDir is a seam so tests can pin the home directory.
var defaultHomeDir = os.UserHomeDir
var homeDir = defaultHomeDir

// ConfigDir returns ~/.config/cc-data (using UserHomeDir on every platform).
func ConfigDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cc-data"), nil
}

// Path returns the config file path.
func Path() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config, returning a default (never nil) config when absent.
// The stored server_url is validated so a hand-edited config cannot bypass the
// origin allowlist.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := fsutil.ReadReplaceTarget(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Version: CurrentVersion}, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config.json is not valid JSON: %w", err)
	}
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.ServerURL != "" {
		origin, err := ValidateServerURL(c.ServerURL)
		if err != nil {
			return nil, fmt.Errorf("config.json server_url invalid: %w", err)
		}
		// Keep the canonical path-stripped origin so ServerOrigin() never
		// returns a raw value that still carries an /api path.
		c.ServerURL = origin
	}
	return &c, nil
}

// Save writes the config atomically at 0600.
func (c *Config) Save() error {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.ServerURL != "" {
		// Canonicalize on write so Save can never persist a value Load would
		// reject, and so the stored origin has any /api path stripped.
		origin, err := ValidateServerURL(c.ServerURL)
		if err != nil {
			return fmt.Errorf("server_url invalid: %w", err)
		}
		c.ServerURL = origin
	}
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := fsutil.EnsureDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic0600(path, data)
}

// ServerOrigin returns the configured server origin, or the built-in default.
func (c *Config) ServerOrigin() string {
	if c.ServerURL != "" {
		return c.ServerURL
	}
	return DefaultServerURL
}

// DataRootDir resolves the dataset root: CC_DATA_ROOT env, then config data_root,
// then ~/cc-data.
func (c *Config) DataRootDir() (string, error) {
	if env := os.Getenv("CC_DATA_ROOT"); env != "" {
		return env, nil
	}
	if c.DataRoot != "" {
		return c.DataRoot, nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "cc-data"), nil
}

// ValidateServerURL enforces the server-origin allowlist and returns the
// canonical origin. The host must be concord.org / concordqa.org or a subdomain
// of either (dot-boundary suffix match), or a loopback host; http is accepted
// only for loopback, all other hosts require https.
func ValidateServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid server URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server URL %q must use http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", raw)
	}
	host, _ := splitHostPort(u.Host)
	host = strings.ToLower(host)

	if isLoopback(host) {
		return u.Scheme + "://" + u.Host, nil
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("server URL %q must use https (http is accepted only for loopback)", raw)
	}
	if !isAllowedServerHost(host) {
		return "", fmt.Errorf("server host %q is not allowed: must be concord.org, concordqa.org, a subdomain of either, or loopback", host)
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func isAllowedServerHost(host string) bool {
	for _, base := range []string{"concord.org", "concordqa.org"} {
		if host == base || strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return false
}
