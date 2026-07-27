package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
)

// TestResolvePortalTarget spot-checks that expansion is reached at all; the
// accepted spellings themselves are config.TestPortalMatrix's to pin.
func TestResolvePortalTarget(t *testing.T) {
	setupCredHome(t)

	cases := []struct{ in, want, wantOrigin string }{
		{"staging", config.StagingPortal, "https://" + config.StagingPortal},
		{config.ProductionPortal, config.ProductionPortal, "https://" + config.ProductionPortal},
		{"https://learn.concord.org/", config.ProductionPortal, "https://" + config.ProductionPortal},
		{"localhost:3000", config.DevPortal, "http://" + config.DevPortal},
		{"https://localhost:3001", "localhost:3001", "https://localhost:3001"},
	}
	for _, c := range cases {
		got, origin, err := ResolvePortalTarget(c.in)
		if err != nil {
			t.Errorf("ResolvePortalTarget(%q) error: %v", c.in, err)
			continue
		}
		if got.Host() != c.want || origin != c.wantOrigin {
			t.Errorf("ResolvePortalTarget(%q) = (%q, %q), want (%q, %q)", c.in, got.Host(), origin, c.want, c.wantOrigin)
		}
	}

	// With nothing stored under it, a single-label host is a typo'd alias.
	if _, _, err := ResolvePortalTarget("stagng"); err == nil {
		t.Error("a misspelled alias with no stored credential should be rejected")
	}
}

// TestResolvePortalTargetReachesLegacyCredential covers the exception this
// resolver makes to the portal shape check. An older build stored credentials
// under single-label hosts; auth status still lists them, so refusing them would
// leave one visible with no way to revoke it, its runs impossible to list, and no
// way to refresh the token once it expires.
func TestResolvePortalTargetReachesLegacyCredential(t *testing.T) {
	setupCredHome(t)

	legacy := config.AdoptStoredPortal("myportal")
	var store creds.Store
	if err := store.Save(legacy, "ccd_abc", config.DefaultServerURL); err != nil {
		t.Fatal(err)
	}
	got, origin, err := ResolvePortalTarget("myportal")
	if err != nil {
		t.Fatalf("a stored single-label portal should be reachable: %v", err)
	}
	if got != legacy {
		t.Errorf("ResolvePortalTarget = %q, want %q", got.Host(), "myportal")
	}
	if origin != "https://myportal" {
		t.Errorf("origin = %q, want %q", origin, "https://myportal")
	}

	// The exception is only for what is actually stored: a near-miss of the
	// stored host is still a typo.
	if _, _, err := ResolvePortalTarget("myportl"); err == nil {
		t.Error("an unstored single-label portal should still be rejected")
	}
}

// TestResolvePortalArgReportsUnreadableStore keeps a broken credential store
// from reading as a misspelled portal. The shape error alone would send the user
// to fix their spelling when the real problem is that the listing failed.
func TestResolvePortalTargetReportsUnreadableStore(t *testing.T) {
	setupCredHome(t)

	dir, err := config.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err = ResolvePortalTarget("myportal")
	if err == nil {
		t.Fatal("an unreadable store should not resolve a single-label portal")
	}
	if !strings.Contains(err.Error(), "could not read stored credentials") {
		t.Errorf("error should name the listing failure: %v", err)
	}
}
