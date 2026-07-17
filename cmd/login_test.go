package cmd

import (
	"os"
	"testing"
)

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
