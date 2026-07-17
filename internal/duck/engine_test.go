package duck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

func answerRec(sk, re, qid, data string) []byte {
	m := map[string]any{"source_key": sk, "remote_endpoint": re, "question_id": qid, "score": 5, "answer_text": data}
	b, _ := json.Marshal(m)
	return b
}

func buildStore(t *testing.T, d *dataset.Dataset, run int, recs [][]byte) {
	t.Helper()
	seg := store.OpenSegment(d.Dir, store.TypeAnswers, run)
	if err := seg.AppendPage(recs, time.Unix(int64(run), 0).UTC(), run); err != nil {
		t.Fatal(err)
	}
	seg.WriteCursor(&store.Cursor{Items: len(recs)})
	if _, err := d.MergeCompact(store.TypeAnswers, run, seg); err != nil {
		t.Fatal(err)
	}
}

func addReportCSV(t *testing.T, d *dataset.Dataset, run int, reportType, content string) {
	t.Helper()
	name := fmt.Sprintf("report_%d.csv", run)
	if err := os.WriteFile(d.Path(name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	rowCount, cols, order, dialect, err := dataset.DetectCSV(d.Path(name), reportType)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDownload(dataset.Download{
		Type: "report", RunID: run, ReportType: reportType, Slug: "slug",
		Files: []string{name}, RowCount: &rowCount, Columns: cols, ColumnOrder: order, CSVDialect: &dialect, Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func newDS(t *testing.T, name string) *dataset.Dataset {
	t.Helper()
	root := t.TempDir()
	d, err := dataset.Create(root, dataset.Ref{Portal: "learn.concord.org", Name: name}, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func openEngine(t *testing.T, specs []DatasetSpec, allowDirs []string) *Engine {
	t.Helper()
	e, err := Open(context.Background(), specs, allowDirs, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func queryInt(t *testing.T, e *Engine, sql string) int {
	t.Helper()
	rows, err := e.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func TestEngineReportsAndStores(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi"), answerRec("s", "e2", "q2", "yo")})
	addReportCSV(t, d, 584, "answers", "student_id,res_1_q1_answer\nPrompt,What?\nCorrect answer,42\n1,hello\n2,world\n")

	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)

	// reports view filters out the two pseudo-header rows.
	if n := queryInt(t, e, "SELECT count(*) FROM reports"); n != 2 {
		t.Fatalf("reports count = %d, want 2", n)
	}
	// report_prompts exposes exactly the two pseudo-header rows.
	if n := queryInt(t, e, "SELECT count(*) FROM report_prompts"); n != 2 {
		t.Fatalf("report_prompts count = %d, want 2", n)
	}
	// answers store has 2 records.
	if n := queryInt(t, e, "SELECT count(*) FROM answers"); n != 2 {
		t.Fatalf("answers count = %d, want 2", n)
	}
	// run_membership has the 2 answers identities.
	if n := queryInt(t, e, "SELECT count(*) FROM run_membership WHERE type = 'answers'"); n != 2 {
		t.Fatalf("run_membership count = %d, want 2", n)
	}
	// per-download join view.
	if n := queryInt(t, e, "SELECT count(*) FROM answers_584"); n != 2 {
		t.Fatalf("answers_584 count = %d, want 2", n)
	}
}

func TestEngineUnknownReportTypeExcluded(t *testing.T) {
	d := newDS(t, "ds")
	addReportCSV(t, d, 584, "answers", "student_id,x_answer\nPrompt,p\nCorrect answer,c\n1,a\n")
	addReportCSV(t, d, 999, "brand-new-type", "student_id,y\n1,5\n")

	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	// The unknown-type run is excluded from reports; only run 584's data row remains.
	if n := queryInt(t, e, "SELECT count(*) FROM reports"); n != 1 {
		t.Fatalf("reports should exclude unknown type, count = %d, want 1", n)
	}
	// Its per-run view is still available.
	if n := queryInt(t, e, "SELECT count(*) FROM report_999"); n != 1 {
		t.Fatalf("per-run view for unknown type should work, got %d", n)
	}
}

func TestEngineFreshDatasetAllViewsQueryable(t *testing.T) {
	d := newDS(t, "empty")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, v := range []string{"reports", "report_prompts", "answers", "history", "run_membership", "downloads", "attachment_files", "attachment_states"} {
		if n := queryInt(t, e, "SELECT count(*) FROM "+v); n != 0 {
			t.Fatalf("fresh view %s should have 0 rows, got %d", v, n)
		}
	}
}

func TestEngineSandboxBlocksOutsideRead(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi")})
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)

	// A path outside the dataset folder must be denied.
	outside := filepath.Join(t.TempDir(), "secret.csv")
	os.WriteFile(outside, []byte("a\n1\n"), 0o600)
	_, err := e.Query(context.Background(), "SELECT * FROM read_csv('"+outside+"')")
	if err == nil {
		t.Fatal("reading a file outside the dataset should be denied by the sandbox")
	}
}

func TestEngineAllowDirAdmitsPath(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi")})
	extraDir := t.TempDir()
	roster := filepath.Join(extraDir, "roster.csv")
	os.WriteFile(roster, []byte("name\nAda\nGrace\n"), 0o600)

	e := openEngine(t, []DatasetSpec{{DS: d}}, []string{extraDir})
	if n := queryInt(t, e, "SELECT count(*) FROM read_csv('"+roster+"')"); n != 2 {
		t.Fatalf("--allow-dir should admit the roster, got %d", n)
	}
}

func TestEngineJSONFunctionsUnderSandbox(t *testing.T) {
	d := newDS(t, "ds")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	// TRY_CAST to JSON and ->> extraction must work with extensions autoloaded off.
	rows, err := e.Query(context.Background(), `SELECT (TRY_CAST('{"a":1}' AS JSON))->>'$.a'`)
	if err != nil {
		t.Fatalf("JSON functions should work under the locked sandbox: %v", err)
	}
	defer rows.Close()
	var got string
	if rows.Next() {
		rows.Scan(&got)
	}
	if got != "1" {
		t.Fatalf("json extraction = %q, want 1", got)
	}
}

func TestEngineMultiDatasetSchemas(t *testing.T) {
	d1 := newDS(t, "wildfire")
	buildStore(t, d1, 1, [][]byte{answerRec("s", "e1", "q1", "a")})
	d2 := newDS(t, "volcano")
	buildStore(t, d2, 2, [][]byte{answerRec("s", "e2", "q2", "b"), answerRec("s", "e3", "q3", "c")})

	e := openEngine(t, []DatasetSpec{{DS: d1}, {DS: d2}}, nil)
	if n := queryInt(t, e, `SELECT count(*) FROM "wildfire".answers`); n != 1 {
		t.Fatalf("wildfire.answers = %d, want 1", n)
	}
	if n := queryInt(t, e, `SELECT count(*) FROM "volcano".answers`); n != 2 {
		t.Fatalf("volcano.answers = %d, want 2", n)
	}
}

func TestEngineSchemaCollisionRequiresAlias(t *testing.T) {
	root := t.TempDir()
	d1, _ := dataset.Create(root, dataset.Ref{Portal: "a.concord.org", Name: "same"}, "")
	root2 := t.TempDir()
	d2, _ := dataset.Create(root2, dataset.Ref{Portal: "b.concord.org", Name: "same"}, "")

	_, err := Open(context.Background(), []DatasetSpec{{DS: d1}, {DS: d2}}, nil, io.Discard)
	if err == nil {
		t.Fatal("colliding schema names should error without aliases")
	}
	// With an alias it registers.
	e, err := Open(context.Background(), []DatasetSpec{{DS: d1}, {Alias: "same2", DS: d2}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("alias should resolve collision: %v", err)
	}
	e.Close()
}

func TestEngineSanitizedAutoName(t *testing.T) {
	d := newDS(t, "2026-07-16_wildfire")
	buildStore(t, d, 1, [][]byte{answerRec("s", "e1", "q1", "a")})
	d2 := newDS(t, "other")
	buildStore(t, d2, 2, [][]byte{answerRec("s", "e2", "q2", "b")})
	e := openEngine(t, []DatasetSpec{{DS: d}, {DS: d2}}, nil)
	// A hyphenated, digit-leading name registers as a quoted schema.
	if n := queryInt(t, e, `SELECT count(*) FROM "2026-07-16_wildfire".answers`); n != 1 {
		t.Fatalf("auto-name schema query failed, got %d", n)
	}
}

func TestEngineDataRootWithQuote(t *testing.T) {
	// A data_root containing a single quote must be escaped in view literals and SETs.
	base := filepath.Join(t.TempDir(), "o'brien")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Skipf("cannot create quote dir: %v", err)
	}
	d, err := dataset.Create(base, dataset.Ref{Portal: "learn.concord.org", Name: "ds"}, "")
	if err != nil {
		t.Fatal(err)
	}
	buildStore(t, d, 1, [][]byte{answerRec("s", "e1", "q1", "a")})
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	if n := queryInt(t, e, "SELECT count(*) FROM answers"); n != 1 {
		t.Fatalf("quote-bearing data_root should still query, got %d", n)
	}
}

func TestEngineBrokenArtifactDegrades(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi")})
	// Record a CSV download whose file is missing on disk.
	rc := 1
	d.UpsertDownload(dataset.Download{
		Type: "report", RunID: 700, ReportType: "answers", Files: []string{"report_700.csv"},
		RowCount: &rc, Columns: map[string]string{"student_id": "BIGINT", "x_answer": "VARCHAR"},
		CSVDialect: ptrDialect(), Complete: true,
	})
	var warn strings.Builder
	e, err := Open(context.Background(), []DatasetSpec{{DS: d}}, nil, &warn)
	if err != nil {
		t.Fatalf("one broken CSV should not fail the session: %v", err)
	}
	defer e.Close()
	// The store view still works; reports degrades but the session is alive.
	if n := queryInt(t, e, "SELECT count(*) FROM answers"); n != 1 {
		t.Fatalf("answers should still query, got %d", n)
	}
}

func ptrDialect() *dataset.CSVDialect {
	dl := dataset.DefaultCSVDialect()
	return &dl
}
