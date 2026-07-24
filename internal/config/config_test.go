package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateServerURL(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		want    string
	}{
		{"https://report-server.concord.org", false, "https://report-server.concord.org"},
		{"https://report-server.concordqa.org", false, "https://report-server.concordqa.org"},
		{"https://concord.org", false, "https://concord.org"},
		{"https://concordqa.org", false, "https://concordqa.org"},
		{"http://localhost:4000", false, "http://localhost:4000"},
		{"https://localhost:4000", false, "https://localhost:4000"},
		{"http://127.0.0.1:4000", false, "http://127.0.0.1:4000"},
		// A bracketed IPv6 loopback is loopback with or without a port.
		{"http://[::1]:4000", false, "http://[::1]:4000"},
		{"http://[::1]", false, "http://[::1]"},
		// Only a matched pair of brackets is unwrapped, so a stray one cannot
		// be trimmed into a host that reads as loopback.
		{"http://localhost]", true, ""},
		{"https://evil-concord.org", true, ""},
		{"https://concord.org.evil.com", true, ""},
		{"https://notconcord.org", true, ""},
		{"http://report-server.concord.org", true, ""}, // http not allowed off loopback
		{"ftp://report-server.concord.org", true, ""},
		{"https://", true, ""},
	}
	for _, c := range cases {
		got, err := ValidateServerURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("ValidateServerURL(%q) should error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ValidateServerURL(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ValidateServerURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDataRootPrecedence(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = defaultHomeDir })

	// Default: ~/cc-data.
	c := &Config{}
	got, err := c.DataRootDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, "cc-data") {
		t.Fatalf("default data root = %q", got)
	}

	// config data_root wins over default.
	c.DataRoot = "/some/where"
	got, _ = c.DataRootDir()
	if got != "/some/where" {
		t.Fatalf("config data root = %q", got)
	}

	// env wins over config.
	t.Setenv("CC_DATA_ROOT", "/env/root")
	got, _ = c.DataRootDir()
	if got != "/env/root" {
		t.Fatalf("env data root = %q", got)
	}
}

func TestConfigRoundTripAndHomeExpansion(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = defaultHomeDir })

	dir, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".config", "cc-data") {
		t.Fatalf("ConfigDir = %q", dir)
	}

	c := &Config{DefaultPortal: "learn.concord.org", ServerURL: "https://report-server.concordqa.org"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultPortal != "learn.concord.org" || loaded.Version != CurrentVersion {
		t.Fatalf("round trip lost data: %+v", loaded)
	}
	if loaded.ServerOrigin() != "https://report-server.concordqa.org" {
		t.Fatalf("server origin = %q", loaded.ServerOrigin())
	}
}

// TestSaveRejectsAliasDefaultPortal keeps Save from writing a config Load would
// refuse, which would leave every command failing on a file the CLI wrote
// itself.
func TestSaveRejectsAliasDefaultPortal(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = defaultHomeDir })

	c := &Config{DefaultPortal: "staging"}
	err := c.Save()
	if err == nil {
		t.Fatal("saving an alias default_portal should error")
	}
	if !strings.Contains(err.Error(), "environment alias") {
		t.Fatalf("error should explain the alias: %v", err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a rejected Save should not have written config.json")
	}
}

// TestLoadRejectsAliasDefaultPortal pins the refusal to treat an environment
// alias as a portal host. default_portal is also the literal portal of a bare
// dataset ref and the on-disk folder identity, so accepting "staging" here would
// store a credential under a host by that name and build the auth URL
// "https://staging".
func TestLoadRejectsAliasDefaultPortal(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = defaultHomeDir })

	dir := filepath.Join(home, ".config", "cc-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"staging", "prod", "dev", "STAGING"} {
		body := []byte(`{"version":1,"default_portal":"` + alias + `"}`)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load()
		if err == nil {
			t.Errorf("default_portal %q should be rejected as an alias", alias)
			continue
		}
		if !strings.Contains(err.Error(), "environment alias") {
			t.Errorf("default_portal %q error should explain the alias: %v", alias, err)
		}
		// This refusal fails every command, so the error has to name the file
		// the user has to edit to recover.
		path, perr := Path()
		if perr != nil {
			t.Fatal(perr)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("default_portal %q error should name %s: %v", alias, path, err)
		}
	}

	// A full hostname is still accepted.
	body := []byte(`{"version":1,"default_portal":"` + StagingPortal + `"}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("a full hostname default_portal should load: %v", err)
	}
	if c.DefaultPortal != StagingPortal {
		t.Fatalf("default_portal = %q", c.DefaultPortal)
	}
}

// TestLoadNormalizesDefaultPortal pins that the value is canonicalized once, at
// load. Left raw, "https://learn.concord.org" and "learn.concord.org" name the
// same portal but two different dataset folders, and the auto-named
// "dataset create" path filed data under the URL-shaped one, where dataset list
// could not see it.
func TestLoadNormalizesDefaultPortal(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = defaultHomeDir })

	dir := filepath.Join(home, ".config", "cc-data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://learn.concord.org", "https://learn.concord.org/", "Learn.Concord.Org"} {
		body := []byte(`{"version":1,"default_portal":"` + raw + `"}`)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load()
		if err != nil {
			t.Errorf("default_portal %q should load: %v", raw, err)
			continue
		}
		if c.DefaultPortal != ProductionPortal {
			t.Errorf("default_portal %q loaded as %q, want %q", raw, c.DefaultPortal, ProductionPortal)
		}
		p, err := c.DefaultPortalValue()
		if err != nil || p.Host() != ProductionPortal {
			t.Errorf("DefaultPortalValue for %q = (%q, %v)", raw, p.Host(), err)
		}
	}

	// A value that cannot be a folder is refused outright, naming the file.
	body := []byte(`{"version":1,"default_portal":"../../escaped"}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("a traversal default_portal should be rejected")
	}
}

func TestLoadAbsentReturnsDefault(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = defaultHomeDir })

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != CurrentVersion || c.ServerOrigin() != DefaultServerURL {
		t.Fatalf("absent config default wrong: %+v", c)
	}
}
