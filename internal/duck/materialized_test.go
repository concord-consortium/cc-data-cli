package duck

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
)

// plantParquet writes a Parquet for one view and records it in the manifest. The
// select runs against the dataset's raw views on a scratch connection, so the
// Parquet carries the view's own column set and types.
func plantParquet(t *testing.T, d *dataset.Dataset, view, selectSQL string, inputs []string) {
	t.Helper()
	if err := os.MkdirAll(d.Path(dataset.MaterializedDir), 0o700); err != nil {
		t.Fatal(err)
	}
	rel := filepath.ToSlash(filepath.Join(dataset.MaterializedDir, view+".parquet"))

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	canon, err := canonicalize(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	vs := viewSet{canonDir: canon, m: mustManifest(t, d)}
	ctx := context.Background()
	for _, st := range vs.statements() {
		if _, err := db.ExecContext(ctx, st.primary); err != nil {
			if _, ferr := db.ExecContext(ctx, st.fallback); ferr != nil {
				t.Fatalf("registering %s: %v", st.name, err)
			}
		}
	}
	copySQL := fmt.Sprintf("COPY (%s) TO %s (FORMAT parquet, COMPRESSION zstd)", selectSQL, sqlStr(d.Path(rel)))
	if _, err := db.ExecContext(ctx, copySQL); err != nil {
		t.Fatalf("planting %s: %v", rel, err)
	}

	m := mustManifest(t, d)
	m.Materialized[view] = dataset.Materialized{File: rel, Inputs: dataset.FingerprintInputs(d.Dir, inputs)}
	if err := writeManifestUnderLock(d, m); err != nil {
		t.Fatal(err)
	}
}

func writeManifestUnderLock(d *dataset.Dataset, m *dataset.Manifest) error {
	ok, err := d.Lock().TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("dataset lock busy")
	}
	defer d.Lock().Unlock()
	return d.WriteManifest(m)
}

func mustManifest(t *testing.T, d *dataset.Dataset) *dataset.Manifest {
	t.Helper()
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// answersFixture is a two-record answers store, returned with the data root so a
// caller can rename it.
func answersFixture(t *testing.T, name string) (*dataset.Dataset, string, string) {
	t.Helper()
	root := t.TempDir()
	d, err := dataset.Create(root, dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: name}, "")
	if err != nil {
		t.Fatal(err)
	}
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi"), answerRec("s", "e2", "q2", "yo")})
	return d, root, mustManifest(t, d).Stores["answers"].File
}

// plantAnswersSentinel materializes the answers view with one extra row the raw
// store does not have, so a query that returns it can only have read the Parquet.
func plantAnswersSentinel(t *testing.T, d *dataset.Dataset, storeFile string) {
	t.Helper()
	sel := `SELECT * FROM "answers" UNION ALL (SELECT * REPLACE ('SENTINEL' AS question_id) FROM "answers" LIMIT 1)`
	plantParquet(t, d, "answers", sel, []string{storeFile})
}

func sentinelCount(t *testing.T, e *Engine, view string) int {
	t.Helper()
	return queryInt(t, e, fmt.Sprintf("SELECT count(*) FROM %s WHERE question_id = 'SENTINEL'", view))
}

func TestMaterializedViewIsPreferredWhileFresh(t *testing.T) {
	d, _, storeFile := answersFixture(t, "ds")
	plantAnswersSentinel(t, d, storeFile)

	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	if n := sentinelCount(t, e, "answers"); n != 1 {
		t.Fatalf("sentinel rows = %d, want 1: the fresh Parquet should be the source", n)
	}
}

func TestMaterializedViewIsIgnoredWhenStale(t *testing.T) {
	d, _, storeFile := answersFixture(t, "ds")
	plantAnswersSentinel(t, d, storeFile)
	// A second run rewrites the store, moving the recorded fingerprint.
	buildStore(t, d, 585, [][]byte{answerRec("s", "e3", "q3", "new")})

	var warn bytes.Buffer
	e, err := Open(context.Background(), []DatasetSpec{{DS: d}}, nil, &warn)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if n := sentinelCount(t, e, "answers"); n != 0 {
		t.Fatalf("sentinel rows = %d, want 0: a stale Parquet must not be read", n)
	}
	if warn.Len() != 0 {
		t.Fatalf("going stale must be silent, got %q", warn.String())
	}
}

func TestMaterializedViewFallsBackWhenUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, path string)
	}{
		{"deleted", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not a parquet file"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated", func(t *testing.T, path string) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body[:len(body)/2], 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, storeFile := answersFixture(t, "ds")
			plantAnswersSentinel(t, d, storeFile)
			path := d.Path(filepath.Join(dataset.MaterializedDir, "answers.parquet"))
			// The fingerprint covers the inputs, not the Parquet, so the entry
			// stays fresh and the damage has to be caught at registration.
			tc.damage(t, path)

			var warn bytes.Buffer
			e, err := Open(context.Background(), []DatasetSpec{{DS: d}}, nil, &warn)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if n := queryInt(t, e, "SELECT count(*) FROM answers"); n != 2 {
				t.Fatalf("answers count = %d, want the 2 raw rows", n)
			}
			if !bytes.Contains(warn.Bytes(), []byte("is unreadable")) {
				t.Fatalf("want an unreadable-materialized warning, got %q", warn.String())
			}
		})
	}
}

// TestMaterializedViewSurvivesRename pins the property whose absence disqualified
// a persistent DuckDB: the manifest records dataset-relative paths, so the folder
// moves with the dataset.
func TestMaterializedViewSurvivesRename(t *testing.T) {
	d, root, storeFile := answersFixture(t, "ds")
	plantAnswersSentinel(t, d, storeFile)

	renamed, err := d.Rename(root, "moved")
	if err != nil {
		t.Fatal(err)
	}
	e := openEngine(t, []DatasetSpec{{DS: renamed}}, nil)
	if n := sentinelCount(t, e, "answers"); n != 1 {
		t.Fatalf("sentinel rows after rename = %d, want 1", n)
	}
}

func TestMaterializedViewResolvesUnderItsSchema(t *testing.T) {
	alpha, _, storeFile := answersFixture(t, "alpha")
	plantAnswersSentinel(t, alpha, storeFile)
	beta, _, _ := answersFixture(t, "beta")

	e := openEngine(t, []DatasetSpec{{DS: alpha}, {DS: beta}}, nil)
	if n := sentinelCount(t, e, `"alpha"."answers"`); n != 1 {
		t.Fatalf("alpha sentinel rows = %d, want 1", n)
	}
	if n := sentinelCount(t, e, `"beta"."answers"`); n != 0 {
		t.Fatalf("beta sentinel rows = %d, want 0: only alpha is materialized", n)
	}
}

func TestMaterializableViews(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi")})
	addReportCSV(t, d, 584, "answers", "student_id,res_1_q1_answer\nPrompt,What?\nCorrect answer,42\n1,hello\n")
	addDimensionCSV(t, d, dimFixture{run: 700, slug: "student-id-mapping", fetchedAt: time.Unix(1, 0).UTC(),
		csv: mappingCSV(mappingRow(1, 30, endpointAAA))})

	got := MaterializableViews(mustManifest(t, d))
	sort.Strings(got)
	want := []string{"answers", "report_prompts", "reports", "run_membership", "student_id_mapping"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MaterializableViews = %v, want %v", got, want)
	}
}

// TestMaterializableViewsExcludesEmptyDeclarations covers the other half of the
// predicate: over an empty manifest every builder returns its stand-in, which
// scans nothing, so nothing is materializable.
func TestMaterializableViewsExcludesEmptyDeclarations(t *testing.T) {
	if got := MaterializableViews(&dataset.Manifest{}); len(got) != 0 {
		t.Fatalf("MaterializableViews over an empty manifest = %v, want none", got)
	}
}
