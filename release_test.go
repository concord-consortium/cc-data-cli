package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestArtifactNamingAgrees asserts the goreleaser archive name template, the
// Homebrew formula template, and the formula render script all use the same
// cc-data_<version>_<os>_<arch> artifact naming, so a release cannot produce
// files the formula cannot find.
func TestArtifactNamingAgrees(t *testing.T) {
	base := readFile(t, ".goreleaser.base.yaml")
	if !strings.Contains(base, "cc-data_{{ .Version }}_{{ .Os }}_{{ .Arch }}") {
		t.Fatal("goreleaser name_template changed; update the formula naming to match")
	}

	formula := readFile(t, "packaging/cc-data.rb.tmpl")
	render := readFile(t, "scripts/render-formula.sh")

	for _, target := range []string{"darwin_arm64", "darwin_amd64", "linux_amd64"} {
		// The formula URL must reference the goreleaser artifact name.
		wantURL := "cc-data___VERSION___" + target + ".tar.gz"
		if !strings.Contains(formula, wantURL) {
			t.Fatalf("formula template missing artifact %q", wantURL)
		}
		// The render script must compute a sha for the same artifact.
		if !strings.Contains(render, target) {
			t.Fatalf("render-formula.sh does not handle target %q", target)
		}
	}

	// The render script's artifact pattern must match the goreleaser scheme.
	if !strings.Contains(render, `cc-data_${VERSION}_${1}.tar.gz`) {
		t.Fatal("render-formula.sh artifact pattern does not match the goreleaser name_template")
	}
}

func TestReleaseWorkflowTargets(t *testing.T) {
	rel := readFile(t, ".github/workflows/release.yml")
	// The signed macOS targets and the Linux amd64 target must all be built.
	for _, target := range []string{"darwin_arm64", "darwin_amd64", "linux_amd64"} {
		if !strings.Contains(rel, target) {
			t.Fatalf("release workflow missing target %q", target)
		}
	}
	// The tag must fail if a macOS artifact was left unsigned.
	if !regexp.MustCompile(`notarize-unsigned`).MatchString(rel) {
		t.Fatal("release workflow must fail the tag on an unsigned macOS artifact")
	}
}

// TestReleaseWorkflowImportsSigningCert guards the load-bearing cert-import step:
// the App Store Connect key only authenticates notarization, so codesign also needs
// the Developer ID cert + key imported into a keychain. Without this, every macOS
// build fails with "no identity found" even when all secrets are set.
func TestReleaseWorkflowImportsSigningCert(t *testing.T) {
	rel := readFile(t, ".github/workflows/release.yml")
	// Match the actual step inputs, not the bare secret names: those also appear in
	// the "Required secrets" header comment, so a comment alone would satisfy the
	// test even if the import step's env bindings were removed.
	for _, want := range []string{
		"MACOS_CERT_P12: ${{ secrets.MACOS_CERT_P12 }}",
		"MACOS_CERT_PASSWORD: ${{ secrets.MACOS_CERT_PASSWORD }}",
		"security import",
		"security create-keychain",
	} {
		if !strings.Contains(rel, want) {
			t.Fatalf("release workflow must import the Developer ID signing certificate; missing %q", want)
		}
	}
}

func TestCIMatrixCoversFiveTargets(t *testing.T) {
	ci := readFile(t, ".github/workflows/ci.yml")
	for _, runner := range []string{"ubuntu-24.04", "ubuntu-24.04-arm", "macos-15", "macos-15-intel", "windows-2022"} {
		if !strings.Contains(ci, runner) {
			t.Fatalf("CI matrix missing runner %q", runner)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
