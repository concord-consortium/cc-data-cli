package cmd

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

func materializeFixture(t *testing.T) *dataset.Dataset {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	root := t.TempDir()
	t.Setenv("CC_DATA_ROOT", root)

	d, err := dataset.Create(root, dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "ds"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []int{100, 200} {
		name := writeReportCSV(t, d, run)
		rowCount, cols, order, dialect, err := dataset.DetectCSV(d.Path(name), "answers")
		if err != nil {
			t.Fatal(err)
		}
		if err := d.UpsertDownload(dataset.Download{
			Type: "report", RunID: run, ReportType: "answers", Slug: "slug",
			Files: []string{name}, RowCount: &rowCount, Columns: cols, ColumnOrder: order,
			CSVDialect: &dialect, Complete: true, FetchedAt: time.Unix(int64(run), 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A dimension view reading its own CSV, so one view stays clean when another
	// view's input is deleted and the refusal can be shown to be per view.
	mapping := "report_300.csv"
	body := "learner_id,user_id,primary_user_id,student_id,class_id,offering_id,runnable_url,run_remote_endpoint\n" +
		"1,11,11,s1,30,70,https://act/1,https://portal/dataservice/external_activity_data/AAA\n"
	if err := os.WriteFile(d.Path(mapping), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rowCount, cols, order, dialect, err := dataset.DetectCSV(d.Path(mapping), dataset.ReportTypePortal)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDownload(dataset.Download{
		Type: "report", RunID: 300, ReportType: dataset.ReportTypePortal, Slug: "student-id-mapping",
		Files: []string{mapping}, RowCount: &rowCount, Columns: cols, ColumnOrder: order,
		CSVDialect: &dialect, Complete: true, FetchedAt: time.Unix(300, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

// writeReportCSV writes one report CSV and returns its dataset-relative name.
func writeReportCSV(t *testing.T, d *dataset.Dataset, run int) string {
	t.Helper()
	name := "report_" + strconv.Itoa(run) + ".csv"
	body := "student_id,res_1_q1_answer\nPrompt,What?\nCorrect answer,42\n1,hello\n"
	if err := os.WriteFile(d.Path(name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestDatasetMaterializeReportsAndExitsZero(t *testing.T) {
	materializeFixture(t)

	_, errOut, code := runArgs(t, "dataset", "materialize", "learn.concord.org/ds")
	if code != output.ExitSuccess {
		t.Fatalf("clean run exit = %d, want 0: %s", code, errOut)
	}
	for _, want := range []string{"reports", "report_prompts", "written:"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("output should name %q: %s", want, errOut)
		}
	}
}

// TestDatasetMaterializeExitsNonZeroOnARefusal is the assertion that matters: the
// exit code is what stops a set -euo pipefail recipe from consuming a surface
// that is missing a view.
func TestDatasetMaterializeExitsNonZeroOnARefusal(t *testing.T) {
	d := materializeFixture(t)
	if err := os.Remove(d.Path("report_200.csv")); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := runArgs(t, "dataset", "materialize", "learn.concord.org/ds")
	if code == output.ExitSuccess {
		t.Fatalf("a refused view must not exit 0: %s", errOut)
	}
	if !strings.Contains(errOut, "report_200.csv") {
		t.Fatalf("the message must name the missing file: %s", errOut)
	}
	if !strings.Contains(errOut, "re-fetch run 200") || !strings.Contains(errOut, "dataset reindex") {
		t.Fatalf("the message must carry both remedies: %s", errOut)
	}
	if !strings.Contains(errOut, "view reports declares 3 inputs") {
		t.Fatalf("the message must name the view and the shortfall: %s", errOut)
	}
	// The view that does not read the deleted CSV is still written, which is what
	// makes the refusal per view rather than per command.
	if !strings.Contains(errOut, "written: student_id_mapping") {
		t.Fatalf("a clean view should still be written: %s", errOut)
	}

	_, partialOut, partialCode := runArgs(t, "dataset", "materialize", "learn.concord.org/ds", "--allow-partial")
	if partialCode != output.ExitSuccess {
		t.Fatalf("--allow-partial should exit 0, got %d: %s", partialCode, partialOut)
	}
	if !strings.Contains(partialOut, "reports: built from 2 of 3 declared inputs") {
		t.Fatalf("--allow-partial should report the shortfall as a count: %s", partialOut)
	}
}
