package guidance

import (
	"reflect"
	"testing"
)

func TestParseCatalogReadsBothMarkupShapes(t *testing.T) {
	bullet := "## Views\n\n- `reports` — report CSV rows\n"
	table := "## Views\n\n| View | What it holds |\n|---|---|\n| `reports` | Report CSV rows |\n"
	for _, body := range []string{bullet, table} {
		got, err := ParseCatalog(body, "Views")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []string{"reports"}) {
			t.Fatalf("got %v", got)
		}
	}
}

func TestParseCatalogReadsEveryNameInAMultiNameEntry(t *testing.T) {
	bullet := "## Views\n\n- `answers`, `history` — the identity-keyed stores\n"
	row := "## Views\n\n| V | W |\n|---|---|\n| `run_membership`, `downloads` | Provenance |\n"
	if got, err := ParseCatalog(bullet, "Views"); err != nil || !reflect.DeepEqual(got, []string{"answers", "history"}) {
		t.Fatalf("bulleted multi-name entry: got %v, %v", got, err)
	}
	if got, err := ParseCatalog(row, "Views"); err != nil || !reflect.DeepEqual(got, []string{"run_membership", "downloads"}) {
		t.Fatalf("multi-name table row: got %v, %v", got, err)
	}
}

// An entry that opens with prose rather than a name is documentation, not a catalog
// entry. This is what keeps the per-run shapes out of the inventory the guard compares.
func TestParseCatalogIgnoresAnEntryThatDoesNotOpenWithAName(t *testing.T) {
	body := "## Views\n\n- `reports` — x\n- Per-run views: `report_<run>`, `answers_<run>`.\n"
	got, err := ParseCatalog(body, "Views")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"reports"}) {
		t.Fatalf("per-run shapes leaked into the catalog: %v", got)
	}
}

func TestParseCatalogTakesOnlyTheFirstMatchingSection(t *testing.T) {
	body := "## Views\n\n- `reports` — x\n\n## Per-run Views\n\n- `answers_584` — x\n"
	got, err := ParseCatalog(body, "Views")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"reports"}) {
		t.Fatalf("a later heading reopened the section: %v", got)
	}
}

func TestParseCatalogFailsOnAMissingSection(t *testing.T) {
	if _, err := ParseCatalog("## Something Else\n\n- `reports` — x\n", "Views"); err == nil {
		t.Fatal("a missing catalog section must be an error, not an empty result")
	}
}

func TestParseCatalogFailsOnAnEmptySection(t *testing.T) {
	if _, err := ParseCatalog("## Views\n\nprose, but no entries\n", "Views"); err == nil {
		t.Fatal("a section documenting no names must be an error")
	}
}

func TestMissingNamesTheAbsentEntries(t *testing.T) {
	if got := Missing([]string{"reports", "answers"}, []string{"reports"}); !reflect.DeepEqual(got, []string{"answers"}) {
		t.Fatalf("got %v", got)
	}
	if got := Missing([]string{"reports"}, []string{"reports"}); got != nil {
		t.Fatalf("nothing missing should report nothing, got %v", got)
	}
}
