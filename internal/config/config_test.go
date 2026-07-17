package config

import (
	"path/filepath"
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
