package cmd

import (
	"os"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
)

func TestResolveLoginPortalDefaults(t *testing.T) {
	// No flag, no default_portal -> production portal.
	got, err := resolveLoginPortal(&config.Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != config.ProductionPortal {
		t.Fatalf("empty flag + no default should use production %q, got %q", config.ProductionPortal, got)
	}

	// No flag, but a configured default_portal -> that portal.
	got, err = resolveLoginPortal(&config.Config{DefaultPortal: "learn.portal.staging.concord.org"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "learn.portal.staging.concord.org" {
		t.Fatalf("empty flag should use default_portal, got %q", got)
	}

	// Explicit flag wins over default_portal.
	got, err = resolveLoginPortal(&config.Config{DefaultPortal: "learn.portal.staging.concord.org"}, "learn.concord.org")
	if err != nil {
		t.Fatal(err)
	}
	if got != "learn.concord.org" {
		t.Fatalf("flag should win, got %q", got)
	}
}

func withStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })
	go func() {
		w.WriteString(content)
		w.Close()
	}()
}

func TestResolveTokenStdin(t *testing.T) {
	withStdin(t, "ccd_piped_token\n")
	got, err := resolveToken(nil, "-")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ccd_piped_token" {
		t.Fatalf("stdin token = %q", got)
	}
}

func TestResolveTokenDirect(t *testing.T) {
	got, err := resolveToken(nil, "ccd_direct")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ccd_direct" {
		t.Fatalf("direct token = %q", got)
	}
}

func TestResolveTokenEmptyMeansPKCE(t *testing.T) {
	got, err := resolveToken(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("empty flag should mean PKCE, got %q", got)
	}
}
