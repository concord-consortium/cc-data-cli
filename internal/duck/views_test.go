package duck

import (
	"slices"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
)

func TestStaticViewNamesAreUnquotedAndUnique(t *testing.T) {
	got := StaticViewNames()
	if len(got) == 0 {
		t.Fatal("no static views")
	}
	seen := map[string]bool{}
	for _, name := range got {
		if strings.ContainsAny(name, `".`) {
			t.Errorf("view name is still SQL-quoted or schema-qualified: %q", name)
		}
		if seen[name] {
			t.Errorf("duplicate view name %q", name)
		}
		seen[name] = true
	}
}

// A report download expands into a per-run view only once it carries both files and
// columns, so a fixture missing either would prove nothing.
func TestStaticViewNamesExcludePerRunViews(t *testing.T) {
	populated := viewSet{m: &dataset.Manifest{Downloads: []dataset.Download{{
		Type:     "report",
		RunID:    584,
		Files:    []string{"report.csv"},
		Columns:  map[string]string{"run_id": "BIGINT"},
		Complete: true,
	}}}}
	var withRun []string
	for _, st := range populated.statements() {
		withRun = append(withRun, strings.Trim(st.name, `"`))
	}
	if !slices.Contains(withRun, "report_584") {
		t.Fatalf("fixture produced no per-run view, so the exclusion proves nothing: %v", withRun)
	}
	if slices.Contains(StaticViewNames(), "report_584") {
		t.Error("per-run view leaked into the static inventory")
	}
	if len(withRun) != len(StaticViewNames())+1 {
		t.Errorf("expected the per-run view to be the only difference, got %v", withRun)
	}
}

func TestIdentityColumnNamesAreTheMembershipKey(t *testing.T) {
	got := IdentityColumnNames()
	want := []string{"history_id", "question_id", "remote_endpoint", "source_key"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, name := range got {
		if strings.HasPrefix(name, "_") {
			t.Errorf("internal bookkeeping column %q is not an identity column", name)
		}
	}
}
