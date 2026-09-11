package duck

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
)

// fullFixture holds a store, a log CSV and a mapping CSV, so the materializable
// set spans store-backed, union and dimension views.
func fullFixture(t *testing.T) *dataset.Dataset {
	t.Helper()
	root := t.TempDir()
	d, err := dataset.Create(root, dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "ds"}, "")
	if err != nil {
		t.Fatal(err)
	}
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi"), answerRec("s", "e2", "q2", "yo")})
	addReportCSV(t, d, 584, "answers", "student_id,res_1_q1_answer\nPrompt,What?\nCorrect answer,42\n1,hello\n2,world\n")
	addDimensionCSV(t, d, dimFixture{run: 700, slug: "student-id-mapping", fetchedAt: time.Unix(1, 0).UTC(),
		csv: mappingCSV(mappingRow(1, 30, endpointAAA))})
	return d
}

func materialize(t *testing.T, d *dataset.Dataset, opts MaterializeOptions) (MaterializeResult, string) {
	t.Helper()
	var warn bytes.Buffer
	res, err := Materialize(context.Background(), d, opts, &warn)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	return res, warn.String()
}

func columnTypes(t *testing.T, e *Engine, view string) map[string]string {
	t.Helper()
	rows, err := e.Query(context.Background(),
		fmt.Sprintf("SELECT column_name, column_type FROM (DESCRIBE SELECT * FROM %s)", view))
	if err != nil {
		t.Fatalf("describe %s: %v", view, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatal(err)
		}
		out[name] = typ
	}
	return out
}

func viewRows(t *testing.T, e *Engine, view string) []string {
	t.Helper()
	rows, err := e.Query(context.Background(), fmt.Sprintf("SELECT * FROM %s ORDER BY ALL", view))
	if err != nil {
		t.Fatalf("select %s: %v", view, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%v", cells))
	}
	return out
}

func tempFiles(t *testing.T, d *dataset.Dataset) []string {
	t.Helper()
	entries, err := os.ReadDir(d.Path(dataset.MaterializedDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestMaterializeReturnsIdenticalResults iterates the materializable views rather
// than a hand-written list, so a view added later cannot go uncompared.
func TestMaterializeReturnsIdenticalResults(t *testing.T) {
	d := fullFixture(t)
	views := MaterializableViews(mustManifest(t, d))
	if len(views) == 0 {
		t.Fatal("fixture materializes nothing, so the comparison below asserts nothing")
	}

	raw := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	before := map[string][]string{}
	beforeTypes := map[string]map[string]string{}
	for _, v := range views {
		before[v] = viewRows(t, raw, sqlIdent(v))
		beforeTypes[v] = columnTypes(t, raw, sqlIdent(v))
	}

	res, _ := materialize(t, d, MaterializeOptions{})
	sort.Strings(views)
	if !reflect.DeepEqual(res.Written, views) {
		t.Fatalf("written = %v, want every materializable view %v", res.Written, views)
	}

	after := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, v := range views {
		if got := viewRows(t, after, sqlIdent(v)); !reflect.DeepEqual(got, before[v]) {
			t.Fatalf("%s rows differ after materializing:\n raw = %v\n mat = %v", v, before[v], got)
		}
		if got := columnTypes(t, after, sqlIdent(v)); !reflect.DeepEqual(got, beforeTypes[v]) {
			t.Fatalf("%s column types differ after materializing:\n raw = %v\n mat = %v", v, beforeTypes[v], got)
		}
	}
	if f := tempFiles(t, d); len(f) > 0 {
		t.Fatalf("temp files survived a clean run: %v", f)
	}
}

func TestMaterializeSkipsFreshViewsUnlessForced(t *testing.T) {
	d := fullFixture(t)
	first, _ := materialize(t, d, MaterializeOptions{})

	second, _ := materialize(t, d, MaterializeOptions{})
	if len(second.Written) != 0 {
		t.Fatalf("second run wrote %v, want nothing: every view is fresh", second.Written)
	}
	if !reflect.DeepEqual(second.Skipped, first.Written) {
		t.Fatalf("skipped = %v, want the views the first run wrote %v", second.Skipped, first.Written)
	}

	forced, _ := materialize(t, d, MaterializeOptions{Force: true})
	if !reflect.DeepEqual(forced.Written, first.Written) {
		t.Fatalf("--force wrote %v, want %v", forced.Written, first.Written)
	}
}

func TestMaterializeRefusesAMissingInputPerView(t *testing.T) {
	d := fullFixture(t)
	if err := os.Remove(d.Path("report_700.csv")); err != nil {
		t.Fatal(err)
	}

	res, _ := materialize(t, d, MaterializeOptions{})
	// reports and student_id_mapping both read the deleted CSV.
	for _, view := range []string{"reports", "student_id_mapping"} {
		reason, ok := res.Refused[view]
		if !ok {
			t.Fatalf("%s should be refused; refused = %v", view, res.Refused)
		}
		if !strings.Contains(reason, "report_700.csv") {
			t.Fatalf("%s reason does not name the missing file: %s", view, reason)
		}
		if !strings.Contains(reason, "re-fetch run 700") || !strings.Contains(reason, "dataset reindex") {
			t.Fatalf("%s reason must name both remedies: %s", view, reason)
		}
		if !strings.Contains(reason, "--allow-partial") {
			t.Fatalf("%s reason must name the override: %s", view, reason)
		}
	}
	if !containsAll(res.Written, "answers", "run_membership") {
		t.Fatalf("clean views should still be written, got %v", res.Written)
	}
	if !res.Refusals() {
		t.Fatal("a refusal must be reportable, so the command can exit non-zero")
	}

	partial, warn := materialize(t, d, MaterializeOptions{AllowPartial: true})
	if len(partial.Refused) != 0 {
		t.Fatalf("--allow-partial should clear the refusals, got %v", partial.Refused)
	}
	if !containsAll(partial.Written, "reports", "student_id_mapping") {
		t.Fatalf("--allow-partial should write the short views, got %v", partial.Written)
	}
	if !strings.Contains(warn, "building reports from 1 of 2 declared inputs") {
		t.Fatalf("--allow-partial must report the shortfall as a count, got %q", warn)
	}
	if f := tempFiles(t, d); len(f) > 0 {
		t.Fatalf("temp files survived: %v", f)
	}
}

// TestMaterializeRefusesADegradedViewEvenWithAllowPartial covers total loss: the
// view registered from its typed-empty fallback, so there is nothing to publish.
func TestMaterializeRefusesADegradedViewEvenWithAllowPartial(t *testing.T) {
	d := fullFixture(t)
	storeFile := mustManifest(t, d).Stores["answers"].File
	if err := os.Remove(d.Path(storeFile)); err != nil {
		t.Fatal(err)
	}

	res, _ := materialize(t, d, MaterializeOptions{AllowPartial: true})
	reason, ok := res.Refused["answers"]
	if !ok {
		t.Fatalf("a degraded view must be refused with --allow-partial given; refused = %v", res.Refused)
	}
	if !strings.Contains(reason, "typed-empty") {
		t.Fatalf("reason should say the view degraded: %s", reason)
	}
	for _, w := range res.Written {
		if w == "answers" {
			t.Fatal("a degraded view must never be published")
		}
	}
}

func TestMaterializeRefusesAStoreViewShortOfItsCount(t *testing.T) {
	d := fullFixture(t)
	m := mustManifest(t, d)
	st := m.Stores["answers"]
	st.Count = 99
	m.Stores["answers"] = st
	if err := writeManifestUnderLock(d, m); err != nil {
		t.Fatal(err)
	}

	res, _ := materialize(t, d, MaterializeOptions{})
	reason, ok := res.Refused["answers"]
	if !ok {
		t.Fatalf("a short store copy must be refused; refused = %v", res.Refused)
	}
	if !strings.Contains(reason, "99") {
		t.Fatalf("reason should name the manifest's count: %s", reason)
	}
	if _, err := os.Stat(d.Path(filepath.Join(dataset.MaterializedDir, "answers.parquet"))); !os.IsNotExist(err) {
		t.Fatal("a refused copy must not be left on disk under its final name")
	}
}

func TestMaterializeWarnsOnIncompleteDownloadWithoutBlocking(t *testing.T) {
	d := fullFixture(t)
	m := mustManifest(t, d)
	m.Downloads = append(m.Downloads, dataset.Download{Type: "answers", RunID: 901, Complete: false})
	if err := writeManifestUnderLock(d, m); err != nil {
		t.Fatal(err)
	}

	res, warn := materialize(t, d, MaterializeOptions{})
	if !strings.Contains(warn, "run 901") {
		t.Fatalf("an incomplete download must be named: %q", warn)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("an incomplete download must block nothing, refused = %v", res.Refused)
	}
	if !containsAll(res.Written, "answers", "run_membership") {
		t.Fatalf("the store views should still be written, got %v", res.Written)
	}
}

// TestMaterializeDiscardsACopyWhoseInputsMoved drives the window between the copy
// and the repoint, which is the one place a Parquet can describe a state that is
// already gone.
func TestMaterializeDiscardsACopyWhoseInputsMoved(t *testing.T) {
	d := fullFixture(t)
	res, err := materializeWithRace(t, d, func() {
		buildStore(t, d, 585, [][]byte{answerRec("s", "e9", "q9", "later")})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Written {
		if w == "answers" {
			t.Fatal("a copy whose store moved under it must not be recorded as fresh")
		}
	}
	if !containsString(res.Skipped, "answers") {
		t.Fatalf("the discarded view should be reported as skipped, got %v", res.Skipped)
	}
	if _, ok := mustManifest(t, d).Materialized["answers"]; ok {
		t.Fatal("a discarded copy must leave no manifest entry")
	}
	if f := tempFiles(t, d); len(f) > 0 {
		t.Fatalf("a discarded copy must leave no temp file: %v", f)
	}
}

// materializeWithRace runs between() in the window after every copy is made and
// before the manifest is repointed, which is when the mutation locks are free.
func materializeWithRace(t *testing.T, d *dataset.Dataset, between func()) (MaterializeResult, error) {
	t.Helper()
	orig := testHookBeforeRepoint
	testHookBeforeRepoint = func() {
		testHookBeforeRepoint = orig
		between()
	}
	t.Cleanup(func() { testHookBeforeRepoint = orig })
	return Materialize(context.Background(), d, MaterializeOptions{}, io.Discard)
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

func containsAll(all []string, want ...string) bool {
	for _, w := range want {
		if !containsString(all, w) {
			return false
		}
	}
	return true
}

func TestStaleMaterializedViewsTracksTheInputs(t *testing.T) {
	d := fullFixture(t)
	materialize(t, d, MaterializeOptions{})

	stale, err := StaleMaterializedViews(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("nothing should be stale right after materializing, got %v", stale)
	}

	buildStore(t, d, 585, [][]byte{answerRec("s", "e9", "q9", "later")})
	stale, err = StaleMaterializedViews(d)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(stale, "answers", "run_membership") {
		t.Fatalf("a get that moved the store should make its views stale, got %v", stale)
	}
}

func TestAnnotateShowJSONAddsTheStaleWarning(t *testing.T) {
	d := fullFixture(t)
	materialize(t, d, MaterializeOptions{})
	buildStore(t, d, 585, [][]byte{answerRec("s", "e9", "q9", "later")})

	s, err := d.BuildShowJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range s.Warnings {
		if strings.HasPrefix(w, "STALE_MATERIALIZED") {
			t.Fatal("the raw show contract cannot decide staleness; only the view set can")
		}
	}
	if err := AnnotateShowJSON(d, s); err != nil {
		t.Fatal(err)
	}
	var named []string
	for _, w := range s.Warnings {
		if strings.HasPrefix(w, "STALE_MATERIALIZED") {
			named = append(named, w)
		}
	}
	if len(named) == 0 {
		t.Fatalf("want a stale warning, got %v", s.Warnings)
	}
	if !strings.Contains(named[0], "dataset materialize") {
		t.Fatalf("the warning must name the refresh command: %s", named[0])
	}
	if !sort.StringsAreSorted(s.Warnings) {
		t.Fatalf("the decorator must re-sort: %v", s.Warnings)
	}
}

// TestReindexDropsTheEntriesAndKeepsTheFiles covers all three halves of the
// reindex requirement at once: the manifest entries go, the Parquet files stay,
// and dataset show then names them as unreferenced.
func TestReindexDropsTheEntriesAndKeepsTheFiles(t *testing.T) {
	d := fullFixture(t)
	res, _ := materialize(t, d, MaterializeOptions{})
	if len(res.Written) == 0 {
		t.Fatal("fixture materialized nothing")
	}

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	if got := mustManifest(t, d).Materialized; len(got) != 0 {
		t.Fatalf("reindex must drop the materialization entries, got %v", got)
	}
	for _, view := range res.Written {
		if _, err := os.Stat(d.Path(filepath.Join(dataset.MaterializedDir, view+".parquet"))); err != nil {
			t.Fatalf("reindex must leave %s.parquet on disk for external readers: %v", view, err)
		}
	}

	s, err := d.BuildShowJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	var named int
	for _, w := range s.Warnings {
		if strings.HasPrefix(w, "UNREFERENCED_MATERIALIZED") {
			named++
		}
	}
	if named != len(res.Written) {
		t.Fatalf("want every leftover Parquet named, got %d of %d: %v", named, len(res.Written), s.Warnings)
	}
}
