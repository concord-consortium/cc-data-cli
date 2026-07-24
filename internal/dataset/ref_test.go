package dataset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
)

// portal builds a default-portal fixture; the zero Portal means "none configured".
func portal(host string) config.Portal {
	if host == "" {
		return config.Portal{}
	}
	return config.MustPortal(host)
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		raw     string
		def     string
		portal  string
		name    string
		wantErr bool
	}{
		{"learn.concord.org/wildfire", "", "learn.concord.org", "wildfire", false},
		{"https://learn.concord.org/wildfire", "", "learn.concord.org", "wildfire", false},
		{"wildfire", "learn.concord.org", "learn.concord.org", "wildfire", false},
		{"wildfire", "", "", "", true}, // no default portal
		{"localhost:8080/ds", "", "localhost:8080", "ds", false},
		{"", "x", "", "", true},
		{"portal/", "", "", "", true}, // empty name
		// The portal half can no longer carry a traversal into Ref.Dir.
		{"../wildfire", "", "", "", true},
		{"..%2fwildfire", "", "", "", true},
	}
	for _, c := range cases {
		ref, err := ParseRef(c.raw, portal(c.def))
		if c.wantErr {
			if err == nil {
				t.Fatalf("ParseRef(%q,%q) should error", c.raw, c.def)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseRef(%q,%q) error: %v", c.raw, c.def, err)
		}
		if ref.Portal.Host() != c.portal || ref.Name != c.name {
			t.Fatalf("ParseRef(%q) = %+v, want %s/%s", c.raw, ref, c.portal, c.name)
		}
	}
}

// TestParseRefRejectsEnvironmentAliases pins that a ref refuses an environment
// alias rather than expanding it or taking it literally. Expanding would give one
// folder two names; taking it literally files the data under a portal that can
// never hold a credential, which reads downstream as a NOT_AUTHENTICATED that
// "cc-data login staging" appears to fix and does not.
func TestParseRefRejectsEnvironmentAliases(t *testing.T) {
	for _, alias := range []string{"staging", "prod", "dev", "production", "STAGING"} {
		_, err := ParseRef(alias+"/foo", config.Portal{})
		if err == nil {
			t.Errorf("ParseRef(%q/foo) should be refused as an alias", alias)
			continue
		}
		if !strings.Contains(err.Error(), "environment alias") {
			t.Errorf("ParseRef(%q/foo) error should explain the alias: %v", alias, err)
		}
	}
	// A full hostname is of course still fine.
	ref, err := ParseRef(config.StagingPortal+"/foo", config.Portal{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Portal.Host() != config.StagingPortal {
		t.Errorf("ref portal = %q", ref.Portal.Host())
	}
}

// TestParseRefStaysInsideTheDataRoot is the property the traversal rows above
// protect: Ref.Dir feeds MkdirAll and, via dataset delete, os.RemoveAll.
func TestParseRefStaysInsideTheDataRoot(t *testing.T) {
	root := filepath.Join("/data", "root")
	for _, raw := range []string{"../wildfire", "../../wildfire", "./wildfire", "..%2f..%2fwildfire"} {
		ref, err := ParseRef(raw, config.Portal{})
		if err != nil {
			continue // refused outright, which is the preferred outcome
		}
		dir := filepath.Clean(ref.Dir(root))
		if !strings.HasPrefix(dir, filepath.Clean(root)+string(filepath.Separator)) {
			t.Errorf("ParseRef(%q).Dir escapes the data root: %s", raw, dir)
		}
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"a", "wildfire", "2026-07-16_wildfire", "a_b", "a-b", strings.Repeat("a", 63)}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Fatalf("ValidateName(%q) should pass: %v", n, err)
		}
	}
	invalid := []string{
		"",
		"-bad",                                                       // leading hyphen
		"_bad",                                                       // leading underscore
		"Bad",                                                        // uppercase
		"a b",                                                        // space
		strings.Repeat("a", 64),                                      // too long
		"main", "temp", "system", "information_schema", "pg_catalog", // reserved
	}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Fatalf("ValidateName(%q) should fail", n)
		}
	}
}

func TestRefDirEncoding(t *testing.T) {
	ref := Ref{Portal: config.MustPortal("localhost:8080"), Name: "ds"}
	dir := ref.Dir("/root")
	if !strings.Contains(dir, "localhost_8080") {
		t.Fatalf("dir should encode the port: %s", dir)
	}
}

// TestParseRefForExistingReachesRefusedFolder covers the exception the inspect
// and remove commands make. dataset list builds its rows from folder names and
// never parses them, so a folder written under a portal the parser now refuses
// would otherwise be visible and removable only with rm -rf.
func TestParseRefForExistingReachesRefusedFolder(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}

	// A folder that could only have been created by an earlier build.
	stranded := Ref{Portal: config.AdoptStoredPortal("staging"), Name: "wildfire"}
	if err := os.MkdirAll(stranded.Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stranded.Dir(root), "manifest.json"), []byte(`{"name":"wildfire"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The ordinary parser still refuses it, so nothing new can be created there.
	if _, err := ParseRefForConfig(cfg, "staging/wildfire"); err == nil {
		t.Error("ParseRefForConfig should still refuse an alias portal")
	}
	// The existing-dataset parser reaches it, so it can be shown and deleted.
	ref, err := ParseRefForExisting(cfg, root, "staging/wildfire")
	if err != nil {
		t.Fatalf("a stranded dataset should be reachable: %v", err)
	}
	if ref.Portal.Host() != "staging" || ref.Name != "wildfire" {
		t.Errorf("ParseRefForExisting = %+v", ref)
	}

	// The exception is only for what is actually on disk.
	if _, err := ParseRefForExisting(cfg, root, "staging/absent"); err == nil {
		t.Error("an alias portal with no folder should still be refused")
	}
	// And never for a syntax refusal: that is the traversal guard, not a policy.
	for _, raw := range []string{"../wildfire", "../../wildfire"} {
		if _, err := ParseRefForExisting(cfg, root, raw); err == nil {
			t.Errorf("ParseRefForExisting(%q) must stay refused", raw)
		}
	}
}

// TestPortalFolderRoundTripsExactly pins the property that lets the folder name
// serve as the only on-disk record of a dataset's portal: for every host the
// parser accepts, encoding it to a folder and decoding it back returns the same
// host. config.hostSyntax excludes underscores and IPv6 literals precisely so
// this holds; without that, a dataset under "host_3000" would be reported by
// dataset list as belonging to "host:3000", a different portal.
func TestPortalFolderRoundTripsExactly(t *testing.T) {
	hosts := []string{
		"learn.concord.org",
		"learn.portal.staging.concord.org",
		"localhost",
		"localhost:3000",
		"127.0.0.1:3000",
		"portal.example.org:8443",
		"my-portal.concord.org",
	}
	for _, host := range hosts {
		p, err := config.ParsePortalIdentity(host)
		if err != nil {
			t.Errorf("ParsePortalIdentity(%q) should be accepted: %v", host, err)
			continue
		}
		if back := decodePortalFolder(p.Folder()); back != host {
			t.Errorf("%q -> folder %q -> %q, want the original host", host, p.Folder(), back)
		}
	}

	// The hosts that could not round-trip are refused outright, so the property
	// above has no exceptions to carve out.
	for _, host := range []string{"host_3000", "a_b.concord.org", "[::1]", "[::1]:3000"} {
		if _, err := config.ParsePortalIdentity(host); err == nil {
			t.Errorf("ParsePortalIdentity(%q) should be refused: it cannot round-trip", host)
		}
	}
}
