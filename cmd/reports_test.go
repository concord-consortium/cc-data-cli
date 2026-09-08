package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

// TestResolvePortal covers what reports list and reports jobs add over
// auth.ResolvePortalTarget: the default_portal fallback layered on top of it.
// The accepted portal spellings themselves are config.TestPortalMatrix's to pin,
// so only one alias case appears here, as a check that expansion is reached at
// all. There is no --server on these commands; the server comes from the stored
// credential.
func TestResolvePortal(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		flagVal string
		want    string
	}{
		{"the flag is alias-expanded", &config.Config{}, "staging", config.StagingPortal},
		{"flag wins over default_portal", &config.Config{DefaultPortal: config.ProductionPortal}, "staging", config.StagingPortal},
		{"empty flag falls back to default_portal", &config.Config{DefaultPortal: config.ProductionPortal}, "", config.ProductionPortal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolvePortal(c.cfg, c.flagVal)
			if err != nil {
				t.Fatal(err)
			}
			if got.Host() != c.want {
				t.Fatalf("resolvePortal(%q) = %q, want %q", c.flagVal, got.Host(), c.want)
			}
		})
	}
}

func TestResolvePortalRequiresAPortal(t *testing.T) {
	if _, err := resolvePortal(&config.Config{}, ""); err == nil {
		t.Fatal("no flag and no default_portal should be a usage error")
	}
}

func TestFilterOptionsRequiresADimension(t *testing.T) {
	stdout, stderr, code := runArgs(t, "reports", "filter-options", "--portal", "prod")

	if code == output.ExitSuccess {
		t.Fatal("a missing --dimension should not succeed")
	}
	if !strings.Contains(stdout+stderr, "--dimension is required") {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}
}

func TestRenderFilterOptionsTable(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	total := 9
	renderFilterOptionsTable([]api.FilterOption{
		{ID: "3", Label: ""},
		{ID: "2", Label: "Adams (a)"},
	}, &total)

	got := out.String()
	for _, want := range []string{"ID", "LABEL", "Adams (a)", "2 shown of 9 total"} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
	// A coalesced empty label still gets its own row, so the id stays selectable.
	if !strings.Contains(got, "3") {
		t.Fatalf("an empty label dropped its row:\n%s", got)
	}
}

func TestRenderFilterOptionsTableWithoutACount(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	renderFilterOptionsTable([]api.FilterOption{{ID: "2", Label: "Adams (a)"}}, nil)

	if strings.Contains(out.String(), "total") {
		t.Fatalf("a missing count must not be rendered as a total:\n%s", out.String())
	}
}
