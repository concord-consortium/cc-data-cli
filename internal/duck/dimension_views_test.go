package duck

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
)

type dimFixture struct {
	run       int
	slug      string
	fetchedAt time.Time
	filter    string
	csv       string
}

const (
	endpointAAA = "https://portal/dataservice/external_activity_data/AAA"
	endpointBBB = "https://portal/dataservice/external_activity_data/BBB"
	// The form a learner with no secure_key produces. Every such learner carries this one
	// string, so it identifies no learner at all.
	endpointNone = "https://portal/dataservice/external_activity_data/"
)

func addDimensionCSV(t *testing.T, d *dataset.Dataset, f dimFixture) {
	t.Helper()
	name := fmt.Sprintf("report_%d.csv", f.run)
	if err := os.WriteFile(d.Path(name), []byte(f.csv), 0o600); err != nil {
		t.Fatal(err)
	}
	rowCount, cols, order, dialect, err := dataset.DetectCSV(d.Path(name), dataset.ReportTypePortal)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDownload(dataset.Download{
		Type: "report", RunID: f.run, ReportType: dataset.ReportTypePortal, Slug: f.slug,
		Filters: json.RawMessage(f.filter), Files: []string{name}, RowCount: &rowCount,
		Columns: cols, ColumnOrder: order, CSVDialect: &dialect, Complete: true,
		FetchedAt: f.fetchedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func at(day int) time.Time {
	return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC)
}

func mappingCSV(rows ...string) string {
	out := "learner_id,user_id,primary_user_id,student_id,class_id,offering_id,runnable_url,run_remote_endpoint\n"
	for _, r := range rows {
		out += r + "\n"
	}
	return out
}

func mappingRow(learner, class int, endpoint string) string {
	return fmt.Sprintf("%d,11,11,s%d,%d,70,https://act/1,%s", learner, learner, class, endpoint)
}

func openWithWarnings(t *testing.T, d *dataset.Dataset) (*Engine, string) {
	t.Helper()
	var warn bytes.Buffer
	e, err := Open(context.Background(), []DatasetSpec{{DS: d}}, nil, &warn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, warn.String()
}

func queryStrings(t *testing.T, e *Engine, sql string) []string {
	t.Helper()
	rows, err := e.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v *string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			out = append(out, "<NULL>")
			continue
		}
		out = append(out, *v)
	}
	return out
}

// queryString runs a query that must match exactly one row, so a dedupe that dropped the wrong
// row reports itself rather than panicking on an empty result.
func queryString(t *testing.T, e *Engine, sql string) string {
	t.Helper()
	got := queryStrings(t, e, sql)
	if len(got) != 1 {
		t.Fatalf("query %q matched %d rows, want 1", sql, len(got))
	}
	return got[0]
}

// Two runs overlapping on a learner collapse to one row, and the dedupe is by fetch time: run 100
// is the lower id and the later fetch, so an ordering written on run_id returns run 200's row.
func TestStudentIDMappingDeduplicatesByFetchTime(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 200, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
		mappingRow(902, 10, endpointBBB),
	)})
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(2), csv: mappingCSV(
		mappingRow(901, 99, endpointAAA),
	)})

	e, _ := openWithWarnings(t, d)

	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping"); n != 2 {
		t.Fatalf("two runs holding three rows for two learners produced %d view rows, want 2", n)
	}
	if got := queryString(t, e, "SELECT class_id::VARCHAR FROM student_id_mapping WHERE learner_id = 901"); got != "99" {
		t.Fatalf("learner 901 class_id = %q, want the later fetch's 99", got)
	}
	// A learner only the earlier run held is not dropped by the dedupe.
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping WHERE learner_id = 902"); n != 1 {
		t.Fatalf("a learner present in only the earlier run was dropped")
	}
	if got := queryString(t, e, "SELECT run_id::VARCHAR FROM student_id_mapping WHERE learner_id = 902"); got != "200" {
		t.Fatalf("learner 902 came from run %q, want 200", got)
	}
}

// The recency signal orders the view without joining it: a caller sees the CSV's own columns plus
// the run_id every union member carries.
func TestDimensionViewsDoNotExposeTheRecencySignal(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
	)})

	e, _ := openWithWarnings(t, d)
	if err := selectColumn(t, e, "fetched_at"); err == nil {
		t.Fatal("fetched_at is a recency signal, not a column of the view")
	}
	for _, col := range []string{"run_id", "learner_id", "run_remote_endpoint", "offering_id", "runnable_url"} {
		if err := selectColumn(t, e, col); err != nil {
			t.Errorf("the view lost %q: %v", col, err)
		}
	}
}

// degraded reports whether one named view fell back to its typed-empty form. The per-run views
// over the same missing files degrade too, so the check has to name the view it is about.
func degraded(warnings, view string) bool {
	return strings.Contains(warnings, fmt.Sprintf("view %s degraded", sqlIdent(view)))
}

// The rows have to be closed even when they are not read: an open result set holds the single
// connection, and the engine's Close then blocks on it.
func selectColumn(t *testing.T, e *Engine, col string) error {
	t.Helper()
	rows, err := e.Query(context.Background(), "SELECT "+col+" FROM student_id_mapping")
	if err != nil {
		return err
	}
	return rows.Close()
}

// The portal query groups by the report-learner row rather than by the learner id, so one run can
// emit a learner twice. The extra row is dropped and the closed ordering names which one survives.
func TestStudentIDMappingClosesTheOrderingWithinOneRun(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 77, endpointAAA),
		mappingRow(901, 22, endpointAAA),
	)})

	e, _ := openWithWarnings(t, d)
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping"); n != 1 {
		t.Fatalf("a repeated learner produced %d rows, want 1", n)
	}
	// Ascending on the remaining columns, so the lower class_id is the defined survivor rather
	// than whichever row the scan happened to reach first.
	if got := queryString(t, e, "SELECT class_id::VARCHAR FROM student_id_mapping"); got != "22" {
		t.Fatalf("surviving class_id = %q, want the ordering's named survivor 22", got)
	}
}

// The documented join is one row per learner. Against a fixture where every learner has a secure
// key that is true by construction, so the fixture carries two who have none.
func TestStudentIDMappingJoinsAnswersWithoutFanningOut(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 584, [][]byte{
		answerRec("s", endpointAAA, "q1", "hi"),
		answerRec("s", endpointNone, "q2", "yo"),
	})
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
		mappingRow(902, 10, endpointNone),
		mappingRow(903, 10, endpointNone),
	)})

	e, _ := openWithWarnings(t, d)

	const join = "SELECT count(*) FROM student_id_mapping m JOIN answers a ON m.run_remote_endpoint = a.remote_endpoint"
	if n := queryInt(t, e, join); n != 1 {
		t.Fatalf("the join produced %d rows; one answer was attributed to more than one learner", n)
	}
	if got := queryString(t, e, "SELECT m.learner_id::VARCHAR FROM student_id_mapping m JOIN answers a ON m.run_remote_endpoint = a.remote_endpoint"); got != "901" {
		t.Fatalf("the joined learner is %q, want the one with a real secure key", got)
	}
	// Only the join key is withheld: the learners themselves stay in the dimension.
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping"); n != 3 {
		t.Fatalf("the view lists %d learners, want all 3", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping WHERE run_remote_endpoint IS NULL AND class_id = 10"); n != 2 {
		t.Fatalf("the secure-key-less learners lost their other columns")
	}
}

func metadataCSV(rows ...string) string {
	out := "learner_id,user_id,primary_user_id,student_id,class_id,school_id,run_remote_endpoint," +
		"student_name,username,class,school,teacher_user_ids,teacher_names,teacher_emails," +
		"teacher_districts,teacher_states,permission_forms,last_run\n"
	for _, r := range rows {
		out += r + "\n"
	}
	return out
}

func metadataRow(learner int, endpoint, name string) string {
	return fmt.Sprintf("%d,11,11,s%d,10,5,%s,%s,u%d,Period 3,Elm,14,Ms A,a@x,D1,MA,none,2026-09-01",
		learner, learner, endpoint, name, learner)
}

func TestStudentMetadataJoinsMappingOnLearnerID(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
		mappingRow(902, 10, endpointBBB),
	)})
	addDimensionCSV(t, d, dimFixture{run: 101, slug: "student-metadata", fetchedAt: at(1), csv: metadataCSV(
		metadataRow(901, endpointAAA, "Ada"),
		metadataRow(902, endpointBBB, "Bea"),
	)})

	e, _ := openWithWarnings(t, d)
	const join = "SELECT count(*) FROM student_id_mapping m JOIN student_metadata s USING (learner_id)"
	if n := queryInt(t, e, join); n != 2 {
		t.Fatalf("the documented join produced %d rows, want one per learner", n)
	}
	if got := queryString(t, e, "SELECT s.student_name FROM student_id_mapping m JOIN student_metadata s USING (learner_id) WHERE m.offering_id = 70 AND learner_id = 901"); got != "Ada" {
		t.Fatalf("the join carries the wrong metadata row: %q", got)
	}
}

// Two runs fetched under different roles are union-compatible CSVs whose student_name means
// different things. Without the column the dedupe picks by recency across a distinction nothing
// in the view can see.
func TestStudentMetadataExposesHideNames(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{
		run: 100, slug: "student-metadata", fetchedAt: at(1), filter: `{"hide_names":false}`,
		csv: metadataCSV(metadataRow(901, endpointAAA, "Ada")),
	})
	addDimensionCSV(t, d, dimFixture{
		run: 101, slug: "student-metadata", fetchedAt: at(2), filter: `{"hide_names":true}`,
		csv: metadataCSV(metadataRow(902, endpointBBB, "s902")),
	})

	e, _ := openWithWarnings(t, d)
	if got := queryString(t, e, "SELECT hide_names::VARCHAR FROM student_metadata WHERE learner_id = 901"); got != "false" {
		t.Fatalf("learner 901 hide_names = %q, want false", got)
	}
	if got := queryString(t, e, "SELECT hide_names::VARCHAR FROM student_metadata WHERE learner_id = 902"); got != "true" {
		t.Fatalf("learner 902 hide_names = %q, want true", got)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM student_metadata WHERE hide_names = false"); n != 1 {
		t.Fatalf("the mixing is not filterable, got %d rows under a role", n)
	}
}

// Every download in every dataset that exists today predates the filter being recorded, so a
// query filtering WHERE hide_names = false must not silently exclude them without saying so.
func TestStudentMetadataHideNamesIsNullWithoutAFilter(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{
		run: 100, slug: "student-metadata", fetchedAt: at(1),
		csv: metadataCSV(metadataRow(901, endpointAAA, "Ada")),
	})

	e, _ := openWithWarnings(t, d)
	if got := queryString(t, e, "SELECT hide_names::VARCHAR FROM student_metadata"); got != "<NULL>" {
		t.Fatalf("hide_names = %q, want NULL for a download with no recorded filter", got)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM student_metadata WHERE hide_names IS NULL"); n != 1 {
		t.Fatal("the null is not observable")
	}
}

// This is every dataset before the first Portal download, and the shape StaticViewNames builds.
// Wrapping the stand-in in the dedupe reproduces the same binder error the missing-file case had.
func TestDimensionViewsAreEmptyWithNoSuchDownloads(t *testing.T) {
	d := newDS(t, "ds")
	addReportCSV(t, d, 584, dataset.ReportTypeAnswers, "student_id,x_answer\nPrompt,p\nCorrect answer,c\n1,a\n")

	e, warnings := openWithWarnings(t, d)
	for _, view := range []string{"student_id_mapping", "student_metadata"} {
		if n := queryInt(t, e, "SELECT count(*) FROM "+view); n != 0 {
			t.Errorf("%s has %d rows on a dataset with no such downloads", view, n)
		}
	}
	// The documented joins must be runnable before anything has been downloaded, which a
	// run_id-only stand-in cannot answer.
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping WHERE learner_id IS NOT NULL"); n != 0 {
		t.Errorf("the typed columns are missing from the stand-in, got %d", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping m JOIN answers a ON m.run_remote_endpoint = a.remote_endpoint"); n != 0 {
		t.Errorf("the documented join does not bind on a fresh dataset, got %d", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping m JOIN student_metadata s USING (learner_id)"); n != 0 {
		t.Errorf("the metadata join does not bind on a fresh dataset, got %d", n)
	}
	for _, view := range []string{"student_id_mapping", "student_metadata"} {
		if strings.Contains(warnings, view) {
			t.Errorf("a dataset with no Portal downloads warned about %s: %q", view, warnings)
		}
	}
}

// One missing CSV costs that run's rows and says so, rather than taking the view down.
func TestDimensionViewDegradesWhenOneCSVIsMissing(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
	)})
	addDimensionCSV(t, d, dimFixture{run: 200, slug: "student-id-mapping", fetchedAt: at(2), csv: mappingCSV(
		mappingRow(902, 10, endpointBBB),
	)})
	if err := os.Remove(d.Path("report_200.csv")); err != nil {
		t.Fatal(err)
	}

	e, warnings := openWithWarnings(t, d)
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping"); n != 1 {
		t.Fatalf("the surviving run contributes %d rows, want 1", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping WHERE learner_id = 901"); n != 1 {
		t.Fatal("the present run's learner was lost with the missing one")
	}
	if !strings.Contains(warnings, "report_200.csv") {
		t.Fatalf("the missing CSV was not named: %q", warnings)
	}
	if degraded(warnings, "student_id_mapping") {
		t.Fatalf("one missing CSV should cost its rows, not the whole view: %q", warnings)
	}
}

// The case a stand-in member without fetched_at fails: one surviving CSV hides it, so the test
// that matters removes every file.
func TestDimensionViewInstallsWhenEveryCSVIsMissing(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
	)})
	addDimensionCSV(t, d, dimFixture{run: 200, slug: "student-id-mapping", fetchedAt: at(2), csv: mappingCSV(
		mappingRow(902, 10, endpointBBB),
	)})
	for _, run := range []int{100, 200} {
		if err := os.Remove(d.Path(fmt.Sprintf("report_%d.csv", run))); err != nil {
			t.Fatal(err)
		}
	}

	e, warnings := openWithWarnings(t, d)
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping"); n != 0 {
		t.Fatalf("the view answers %d rows, want 0", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping WHERE learner_id IS NOT NULL"); n != 0 {
		t.Fatal("the view did not install with its columns")
	}
	// The fallback would rescue a stand-in member that dropped the recency column, so the
	// assertion that catches it is that the primary installed rather than that the view exists.
	if degraded(warnings, "student_id_mapping") {
		t.Fatalf("the primary view failed and only the fallback installed: %q", warnings)
	}
}

// The fallback is what installs when a present-but-unbindable CSV breaks the primary CREATE VIEW.
// It is only ever executed on that path, so nothing else proves it is valid SQL, and a stand-in
// member built without the recency column takes the fallback down with the primary.
func TestDimensionViewFallbackKeepsTheColumnShape(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
	)})
	addDimensionCSV(t, d, dimFixture{
		run: 101, slug: "student-metadata", fetchedAt: at(1), filter: `{"hide_names":true}`,
		csv: metadataCSV(metadataRow(901, endpointAAA, "s901")),
	})

	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	canon, err := canonicalize(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	vs := viewSet{canonDir: canon, m: m}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, stmt := range vs.dimensionViewStmts() {
		if _, err := db.Exec(stmt.fallback); err != nil {
			t.Fatalf("the fallback for %s is not valid SQL: %v", stmt.name, err)
		}
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + stmt.name + " WHERE learner_id IS NOT NULL").Scan(&n); err != nil {
			t.Fatalf("the fallback for %s lost its columns: %v", stmt.name, err)
		}
		if n != 0 {
			t.Errorf("the fallback for %s answered %d rows, want 0", stmt.name, n)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM "student_metadata" WHERE hide_names IS NULL`).Scan(&n); err != nil {
		t.Fatalf("the metadata fallback lost hide_names: %v", err)
	}
}

// The manifest is the provenance record and it survives an ordinary reindex, so the only case the
// views cannot cover is its own loss. Nothing is recovered from CSV shape there: the mapping
// report's columns are a strict subset of the log report's, so a positive column rule would
// resolve a log row as a learner's mapping. The views come back empty and the warning says why.
func TestDimensionViewsAreEmptyAfterAManifestLessReindex(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
	)})
	if err := os.Remove(d.Path(dataset.ManifestFile)); err != nil {
		t.Fatal(err)
	}
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}

	e, _ := openWithWarnings(t, d)
	if n := queryInt(t, e, "SELECT count(*) FROM student_id_mapping"); n != 0 {
		t.Fatalf("a slug was recovered from CSV shape, giving the view %d rows", n)
	}
	// The CSV is still on disk and still queryable per run; only its provenance is gone.
	if n := queryInt(t, e, "SELECT count(*) FROM report_100"); n != 1 {
		t.Fatalf("the CSV itself was lost, not just its provenance: %d rows", n)
	}

	s, err := d.BuildShowJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	var named bool
	for _, w := range s.Warnings {
		if strings.HasPrefix(w, "RECOVERED_PROVENANCE:") && strings.Contains(w, "100") && strings.Contains(w, "dimension view") {
			named = true
		}
	}
	if !named {
		t.Fatalf("nothing says which run lost its dimension view: %v", s.Warnings)
	}
}

// perDownloadViews builds report_<run_id> for every download of type report, with no report-type
// or allowlist condition on it. That is why no per-run dimension views are built, and it holds by
// omission, so one added condition would remove it silently.
func TestAPortalRunIsQueryablePerRun(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{run: 100, slug: "student-id-mapping", fetchedAt: at(1), csv: mappingCSV(
		mappingRow(901, 10, endpointAAA),
		mappingRow(902, 10, endpointBBB),
	)})

	e, _ := openWithWarnings(t, d)
	if n := queryInt(t, e, "SELECT count(*) FROM report_100"); n != 2 {
		t.Fatalf("report_100 has %d rows, want the run's 2", n)
	}
	// Deduplicated by nothing, which is what makes it the answer to "what did this run contain"
	// where the dimension view answers "what is currently true of these learners".
	if n := queryInt(t, e, "SELECT count(*) FROM report_100 WHERE run_remote_endpoint = '"+endpointBBB+"'"); n != 1 {
		t.Fatal("the per-run view rewrote the run's own columns")
	}
}

// A name column means one thing or the other depending on the run's hide-names setting, and the
// reports union has no column of its own to say which. Four of the five Athena reports carry the
// same ambiguity, so the discriminator belongs on the download rather than on one view's rows.
func TestDownloadsCarriesHideNamesForEveryReport(t *testing.T) {
	d := newDS(t, "ds")
	addDimensionCSV(t, d, dimFixture{
		run: 100, slug: "student-metadata", fetchedAt: at(1), filter: `{"hide_names":false}`,
		csv: metadataCSV(metadataRow(901, endpointAAA, "Ada Lovelace")),
	})
	addDimensionCSV(t, d, dimFixture{
		run: 101, slug: "student-metadata", fetchedAt: at(2), filter: `{"hide_names":true}`,
		csv: metadataCSV(metadataRow(902, endpointBBB, "s902")),
	})
	// A download with no recorded filter, which is what a dataset made by an earlier version
	// holds: the column has to read as unknown rather than as either role.
	addReportCSV(t, d, 216, dataset.ReportTypeAnswers, "student_id,student_name\nPrompt,p\nCorrect answer,c\n1,Bea\n")
	// A second download on run 100, as pulling that run's answers produces. It is what makes the
	// downloads join fan out unless it is qualified.
	if err := d.UpsertDownload(dataset.Download{Type: "answers", RunID: 100, Complete: true}); err != nil {
		t.Fatal(err)
	}

	e, _ := openWithWarnings(t, d)

	for _, tc := range []struct{ run, want string }{{"100", "false"}, {"101", "true"}, {"216", "<NULL>"}} {
		if got := queryString(t, e, "SELECT hide_names::VARCHAR FROM downloads WHERE type = 'report' AND run_id = "+tc.run); got != tc.want {
			t.Errorf("run %s hide_names = %q, want %q", tc.run, got, tc.want)
		}
	}

	// The join a caller writes to separate the two meanings in the union. It is type-qualified
	// because downloads holds one row per download, not per run: a run whose answers and history
	// were also pulled has three, and joining on run_id alone multiplies the rows it is meant to
	// label. Verified against a real run, where it turned 75 rows into 225.
	const join = `SELECT r.student_name FROM reports r JOIN downloads d
	                ON d.run_id = r.run_id AND d.type = 'report'
	              WHERE r.student_name IS NOT NULL AND d.hide_names = false`
	if got := queryStrings(t, e, join); len(got) != 1 || got[0] != "Ada Lovelace" {
		t.Fatalf("the join returned %v, want only the run fetched with names shown", got)
	}
	// Without it, the union blends them, which is what the column exists to make visible.
	if n := queryInt(t, e, "SELECT count(DISTINCT student_name) FROM reports"); n != 3 {
		t.Fatalf("the fixture no longer blends names, so the join above proves nothing: %d", n)
	}

	// The fixture has to hold a run with more than one download, or the type-qualified join is
	// indistinguishable from the unqualified one that fans out.
	if n := queryInt(t, e, "SELECT count(*) FROM downloads WHERE run_id = 100"); n != 2 {
		t.Fatalf("run 100 has %d downloads, want 2, so the join qualification proves nothing", n)
	}
	rows := queryInt(t, e, "SELECT count(*) FROM reports WHERE run_id = 100")
	qualified := queryInt(t, e, "SELECT count(*) FROM reports r JOIN downloads d ON d.run_id = r.run_id AND d.type = 'report' WHERE r.run_id = 100")
	if qualified != rows {
		t.Errorf("the type-qualified join changed the row count: %d, want %d", qualified, rows)
	}
	if unqualified := queryInt(t, e, "SELECT count(*) FROM reports r JOIN downloads d USING (run_id) WHERE r.run_id = 100"); unqualified == rows {
		t.Errorf("the unqualified join did not fan out, so the guidance's qualification is untested")
	}
}
