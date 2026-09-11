package duck

import (
	"bytes"
	"context"
	"errors"
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
	// A log run puts the logs view in the set, which is the one carrying JSON,
	// TIMESTAMP and LIST columns derived per query rather than read from the CSV.
	addLogCSV(t, d, logFixture{run: 900, csv: clueCSV()})
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
	if !reflect.DeepEqual(res.Written(), views) {
		t.Fatalf("written = %v, want every materializable view %v", res.Written(), views)
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
	// Named individually because these are computed per query, so their types are
	// what the round trip could lose.
	logTypes := columnTypes(t, after, `"logs"`)
	for col, want := range map[string]string{
		"parameters_json": "JSON", "extras_json": "JSON",
		"event_time": "TIMESTAMP", "received_time": "TIMESTAMP",
	} {
		if logTypes[col] != want {
			t.Errorf("materialized logs.%s is %q, want %q", col, logTypes[col], want)
		}
	}
}

// TestMaterializedParquetCarriesItsProvenance covers the footer metadata. Nothing
// in cc-data reads it back; it is there so a Parquet found in a backup or a
// copied folder can say what it is.
func TestMaterializedParquetCarriesItsProvenance(t *testing.T) {
	d := fullFixture(t)
	materialize(t, d, MaterializeOptions{})

	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	path := d.Path(filepath.Join(dataset.MaterializedDir, "answers.parquet"))
	rows, err := e.Query(context.Background(),
		fmt.Sprintf("SELECT key, value FROM parquet_kv_metadata(%s)", sqlStr(path)))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kv := map[string]string{}
	for rows.Next() {
		var k, v []byte
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		kv[string(k)] = string(v)
	}
	if kv["view"] != "answers" {
		t.Errorf("footer view = %q, want answers", kv["view"])
	}
	if _, err := time.Parse(time.RFC3339, kv["built_at"]); err != nil {
		t.Errorf("footer built_at %q is not a timestamp: %v", kv["built_at"], err)
	}
}

func TestMaterializeSkipsFreshViewsUnlessForced(t *testing.T) {
	d := fullFixture(t)
	first, _ := materialize(t, d, MaterializeOptions{})

	second, _ := materialize(t, d, MaterializeOptions{})
	if len(second.Written()) != 0 {
		t.Fatalf("second run wrote %v, want nothing: every view is fresh", second.Written())
	}
	if !reflect.DeepEqual(second.Fresh(), first.Written()) {
		t.Fatalf("skipped = %v, want the views the first run wrote %v", second.Fresh(), first.Written())
	}

	forced, _ := materialize(t, d, MaterializeOptions{Force: true})
	if !reflect.DeepEqual(forced.Written(), first.Written()) {
		t.Fatalf("--force wrote %v, want %v", forced.Written(), first.Written())
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
		reason, ok := res.Refused()[view]
		if !ok {
			t.Fatalf("%s should be refused; refused = %v", view, res.Refused())
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
	if !containsAll(res.Written(), "answers", "run_membership") {
		t.Fatalf("clean views should still be written, got %v", res.Written())
	}
	if !res.Refusals() {
		t.Fatal("a refusal must be reportable, so the command can exit non-zero")
	}

	// --force is not a second way past the refusal; that is --allow-partial's job.
	forced, _ := materialize(t, d, MaterializeOptions{Force: true})
	if _, ok := forced.Refused()["reports"]; !ok {
		t.Fatalf("--force must not override a missing input, refused = %v", forced.Refused())
	}

	partial, warn := materialize(t, d, MaterializeOptions{AllowPartial: true})
	if len(partial.Refused()) != 0 {
		t.Fatalf("--allow-partial should clear the refusals, got %v", partial.Refused())
	}
	if !containsAll(partial.Written(), "reports", "student_id_mapping") {
		t.Fatalf("--allow-partial should write the short views, got %v", partial.Written())
	}
	if !strings.Contains(warn, "building reports from 2 of 3 declared inputs") {
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
	reason, ok := res.Refused()["answers"]
	if !ok {
		t.Fatalf("a degraded view must be refused with --allow-partial given; refused = %v", res.Refused())
	}
	if !strings.Contains(reason, "typed-empty") {
		t.Fatalf("reason should say the view degraded: %s", reason)
	}
	for _, w := range res.Written() {
		if w == "answers" {
			t.Fatal("a degraded view must never be published")
		}
	}
}

// TestMaterializeRefusesAViewItCannotCopy holds the per-view rule for a failing
// copy: the other views are still written and the run still reports a refusal.
// An unwritable target is the reachable shape of this, standing in for a disk
// filling partway through a run.
func TestMaterializeRefusesAViewItCannotCopy(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores the directory permission this test relies on")
	}
	d := fullFixture(t)
	// A directory where answers.parquet's temp file has to be created, with no
	// write permission, so exactly one view's copy fails.
	blocked := d.Path(filepath.Join(dataset.MaterializedDir))
	if err := os.MkdirAll(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(blocked, 0o700) })

	res, err := Materialize(context.Background(), d, MaterializeOptions{}, io.Discard)
	if err != nil {
		t.Fatalf("one unwritable view must not fail the run: %v", err)
	}
	if len(res.Refused()) == 0 {
		t.Fatalf("want the unwritable views refused, got %+v", res)
	}
	if len(res.Written()) != 0 {
		t.Fatalf("nothing can be written into an unwritable folder, got %v", res.Written())
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
	reason, ok := res.Refused()["answers"]
	if !ok {
		t.Fatalf("a short store copy must be refused; refused = %v", res.Refused())
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
	// Two incomplete store downloads for one run: run_membership maps by type, so
	// both reach it and the run must still be named once.
	m.Downloads = append(m.Downloads,
		dataset.Download{Type: "answers", RunID: 901, Complete: false},
		dataset.Download{Type: "history", RunID: 901, Complete: false})
	if err := writeManifestUnderLock(d, m); err != nil {
		t.Fatal(err)
	}

	res, warn := materialize(t, d, MaterializeOptions{})
	if !strings.Contains(warn, "run 901") {
		t.Fatalf("an incomplete download must be named: %q", warn)
	}
	if strings.Contains(warn, "901, 901") {
		t.Fatalf("a run reached through two downloads must be named once: %q", warn)
	}
	if len(res.Refused()) != 0 {
		t.Fatalf("an incomplete download must block nothing, refused = %v", res.Refused())
	}
	if !containsAll(res.Written(), "answers", "run_membership") {
		t.Fatalf("the store views should still be written, got %v", res.Written())
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
	if containsString(res.Written(), "answers") {
		t.Fatal("a copy whose store moved under it must not be recorded as written")
	}
	// The distinction the status exists for: a discarded view is not materialized
	// and running again picks it up, where a fresh one needs nothing.
	if containsString(res.Fresh(), "answers") {
		t.Fatal("a discarded copy must not be reported as already fresh; nothing about it is current")
	}
	if !containsString(res.Discarded(), "answers") {
		t.Fatalf("the discarded view should carry the discarded status, got %+v", res.Views)
	}
	for _, o := range res.Views {
		if o.View == "answers" && o.Reason == "" {
			t.Fatal("a discarded view must say why, or the caller has nothing to render")
		}
	}
	if _, ok := mustManifest(t, d).Materialized["answers"]; ok {
		t.Fatal("a discarded copy must leave no manifest entry")
	}
	if f := tempFiles(t, d); len(f) > 0 {
		t.Fatalf("a discarded copy must leave no temp file: %v", f)
	}
}

// TestMaterializeReportsEveryViewExactlyOnce is the invariant the outcome list
// exists to make structural: a view cannot land in two statuses or in none.
func TestMaterializeReportsEveryViewExactlyOnce(t *testing.T) {
	d := fullFixture(t)
	// One view refused and one already fresh, so the run spans several statuses.
	materialize(t, d, MaterializeOptions{})
	if err := os.Remove(d.Path("report_700.csv")); err != nil {
		t.Fatal(err)
	}
	res, _ := materialize(t, d, MaterializeOptions{})

	want := MaterializableViews(mustManifest(t, d))
	seen := map[string]int{}
	for _, o := range res.Views {
		seen[o.View]++
		if o.Status == "" {
			t.Fatalf("view %s has no status", o.View)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("outcomes cover %d views, want the %d materializable ones: %+v", len(seen), len(want), res.Views)
	}
	for _, view := range want {
		if seen[view] != 1 {
			t.Fatalf("view %s appears %d times, want exactly once: %+v", view, seen[view], res.Views)
		}
	}
	// And the order is the view order, so output is stable between runs.
	var got []string
	for _, o := range res.Views {
		got = append(got, o.View)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcome order = %v, want view order %v", got, want)
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

// TestReindexDropsTheEntriesAndKeepsTheFiles pins three things at once: reindex
// drops the manifest entries, leaves the Parquet files on disk, and dataset show
// then names them as unreferenced. The middle one is what would catch a later
// tidy-up making reindex delete the folder.
func TestReindexDropsTheEntriesAndKeepsTheFiles(t *testing.T) {
	d := fullFixture(t)
	res, _ := materialize(t, d, MaterializeOptions{})
	if len(res.Written()) == 0 {
		t.Fatal("fixture materialized nothing")
	}

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	if got := mustManifest(t, d).Materialized; len(got) != 0 {
		t.Fatalf("reindex must drop the materialization entries, got %v", got)
	}
	for _, view := range res.Written() {
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
	if named != len(res.Written()) {
		t.Fatalf("want every leftover Parquet named, got %d of %d: %v", named, len(res.Written()), s.Warnings)
	}
}

// TestMaterializeSweepsATempFromADeadRun covers the state a hard interrupt
// leaves: nothing installs a signal handler, so a killed run's deferred cleanup
// never runs and its temp file survives. The next run clears it, which it can do
// unconditionally because it holds the materialize guard and the kernel drops
// that guard when its holder dies.
func TestMaterializeSweepsATempFromADeadRun(t *testing.T) {
	d := fullFixture(t)
	if err := os.MkdirAll(d.Path(dataset.MaterializedDir), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dataset.MaterializedDir, "answers"+tempParquetInfix+"99")
	if err := os.WriteFile(d.Path(stale), []byte("half a parquet"), 0o600); err != nil {
		t.Fatal(err)
	}

	materialize(t, d, MaterializeOptions{})

	if _, err := os.Stat(d.Path(stale)); !os.IsNotExist(err) {
		t.Fatalf("a dead run's temp file should be swept, stat err = %v", err)
	}
	if f := tempFiles(t, d); len(f) > 0 {
		t.Fatalf("no temp file should survive: %v", f)
	}
}

// TestMaterializeRefusesASecondRun is what makes the sweep safe: while one run
// holds the guard, a second cannot be inside the window where its temp files
// exist, so there is never a live owner for the sweep to rob.
func TestMaterializeRefusesASecondRun(t *testing.T) {
	d := fullFixture(t)
	release, err := d.LockMaterialize()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = Materialize(context.Background(), d, MaterializeOptions{}, io.Discard)
	if !errors.Is(err, dataset.ErrBusy) {
		t.Fatalf("a second run should report the dataset busy, got %v", err)
	}
}

// TestMaterializeHoldsItsGuardForTheWholeRun pins the property the sweep rests
// on. The check runs in the window where the temp files exist, so a run that
// took the guard and released it straight away fails here: at that point a
// second run could start and sweep away this run's work.
func TestMaterializeHoldsItsGuardForTheWholeRun(t *testing.T) {
	d := fullFixture(t)
	var heldWhileTempsExist bool
	orig := testHookBeforeRepoint
	testHookBeforeRepoint = func() {
		testHookBeforeRepoint = orig
		if f := tempFiles(t, d); len(f) == 0 {
			t.Error("the hook must run while temp files exist, or this asserts nothing")
		}
		release, err := d.LockMaterialize()
		heldWhileTempsExist = err != nil
		if err == nil {
			release()
		}
	}
	t.Cleanup(func() { testHookBeforeRepoint = orig })

	materialize(t, d, MaterializeOptions{})
	if !heldWhileTempsExist {
		t.Fatal("the guard must stay held for the whole run; released early, a second run could sweep this run's temp files")
	}
}

// TestMaterializeReleasesItsGuard keeps the guard from leaking across runs, which
// would make every later run on the dataset fail as busy.
func TestMaterializeReleasesItsGuard(t *testing.T) {
	d := fullFixture(t)
	materialize(t, d, MaterializeOptions{})

	release, err := d.LockMaterialize()
	if err != nil {
		t.Fatalf("the guard should be free once a run finishes: %v", err)
	}
	release()
}
