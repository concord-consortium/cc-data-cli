package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
	"github.com/zalando/go-keyring"
)

func init() { pollSleep = func(context.Context, time.Duration) bool { return true } }

func newTestDataset(t *testing.T) *dataset.Dataset {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	keyring.MockInit()
	root := t.TempDir()
	d, err := dataset.Create(root, dataset.Ref{Portal: "learn.concord.org", Name: "ds"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func storeDownloadLock(t *testing.T, d *dataset.Dataset, name string) *store.DownloadLock {
	t.Helper()
	l := store.DownloadLockFor(d.Dir, name)
	ok, err := l.TryLock()
	if err != nil || !ok {
		t.Fatalf("could not hold download lock %s: %v", name, err)
	}
	return l
}

func fastClient(baseURL string) *api.Client {
	c := api.New(baseURL, "ccd_test")
	c.BaseBackoff = time.Millisecond
	c.MaxBackoff = time.Millisecond
	return c
}

// reportServer is a fake report + S3 server for report-fetch tests.
type reportServer struct {
	*httptest.Server
	slug           string
	reportType     *string
	notReadyStates []string // states returned before ready; "" means null
	csv            string
	pollCount      int32
	stateIndex     int32
}

func newReportServer(t *testing.T, s *reportServer) *reportServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reports/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/download"):
			s.handleDownload(w, r)
		case strings.HasSuffix(path, "/s3"):
			w.Write([]byte(s.csv))
		default:
			rt := "null"
			if s.reportType != nil {
				rt = fmt.Sprintf("%q", *s.reportType)
			}
			fmt.Fprintf(w, `{"id":584,"report_slug":%q,"report_type":%s,"athena_query_state":"succeeded"}`, s.slug, rt)
		}
	})
	s.Server = httptest.NewServer(mux)
	return s
}

func (s *reportServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&s.pollCount, 1)
	i := atomic.LoadInt32(&s.stateIndex)
	if int(i) < len(s.notReadyStates) {
		atomic.AddInt32(&s.stateIndex, 1)
		state := s.notReadyStates[i]
		w.WriteHeader(http.StatusConflict)
		if state == "" {
			fmt.Fprint(w, `{"error":"NOT_READY","athena_query_state":null}`)
		} else {
			fmt.Fprintf(w, `{"error":"NOT_READY","athena_query_state":%q}`, state)
		}
		return
	}
	fmt.Fprintf(w, `{"download_url":%q,"filename":"student-answers-run-584.csv","expires_in_seconds":600}`, s.URL+"/api/v1/reports/584/s3")
}

func runFetch(t *testing.T, d *dataset.Dataset, srv *reportServer, opts fetch1) (any, string, *output.CLIError) {
	t.Helper()
	var stderr bytes.Buffer
	o := ReportOptions{
		DS:       d,
		Client:   fastClient(srv.URL),
		RunID:    584,
		JobID:    opts.jobID,
		NoWait:   opts.noWait,
		Refresh:  opts.refresh,
		Progress: &stderr,
	}
	result, err := FetchReport(context.Background(), o)
	var cliErr *output.CLIError
	if err != nil {
		cliErr = err.(*output.CLIError)
	}
	return result, stderr.String(), cliErr
}

type fetch1 struct {
	jobID   *int
	noWait  bool
	refresh bool
}

func TestGetReportSuccess(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{
		slug:       "student-answers",
		reportType: &rt,
		csv:        "student_id,res_1_q1_answer\nPrompt,What?\nCorrect answer,42\n1,hi\n2,yo\n",
	})
	defer srv.Close()

	result, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr != nil {
		t.Fatalf("unexpected error: %+v", cliErr)
	}
	m := result.(map[string]any)
	if m["complete"] != true || m["report_type"] != "answers" {
		t.Fatalf("result = %+v", m)
	}
	// row_count excludes the 2 pseudo-header rows: 2 data rows.
	if m["row_count"].(int) != 2 {
		t.Fatalf("row_count = %v, want 2", m["row_count"])
	}
	if _, err := os.Stat(d.Path("report_584.csv")); err != nil {
		t.Fatal("CSV not written")
	}
	// Manifest records the download with columns and dialect.
	man, _ := d.ReadManifest()
	if len(man.Downloads) != 1 {
		t.Fatalf("expected 1 download, got %d", len(man.Downloads))
	}
	dl := man.Downloads[0]
	if dl.Columns["res_1_q1_answer"] != "VARCHAR" {
		t.Fatalf("_answer column should be VARCHAR: %+v", dl.Columns)
	}
	if dl.CSVDialect == nil || dl.CSVDialect.Delim != "," {
		t.Fatalf("dialect wrong: %+v", dl.CSVDialect)
	}
}

func TestGetReportUsageCountsAllRows(t *testing.T) {
	d := newTestDataset(t)
	rt := "usage"
	srv := newReportServer(t, &reportServer{
		slug:       "student-assignment-usage",
		reportType: &rt,
		csv:        "student_id,logins\n1,5\n2,7\n3,2\n",
	})
	defer srv.Close()
	result, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	m := result.(map[string]any)
	if m["row_count"].(int) != 3 {
		t.Fatalf("usage row_count = %v, want 3", m["row_count"])
	}
	man, _ := d.ReadManifest()
	if man.Downloads[0].Columns["student_id"] != "BIGINT" {
		t.Fatalf("student_id should be BIGINT: %+v", man.Downloads[0].Columns)
	}
}

func TestGetReportNoWaitQueued(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"queued"}})
	defer srv.Close()
	result, _, cliErr := runFetch(t, d, srv, fetch1{noWait: true})
	if cliErr == nil || cliErr.ExitCode != output.ExitNotReady || !cliErr.Silent {
		t.Fatalf("no-wait should exit 4 silent, got %+v", cliErr)
	}
	m := result.(map[string]any)
	if m["complete"] != false || m["athena_query_state"] != "queued" {
		t.Fatalf("no-wait result = %+v", m)
	}
	if _, err := os.Stat(d.Path("report_584.csv")); !os.IsNotExist(err) {
		t.Fatal("no-wait should not write a CSV")
	}
}

func TestGetReportTerminalFailure(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}})
	defer srv.Close()
	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil || cliErr.ExitCode != output.ExitContract {
		t.Fatalf("terminal failure should exit 5, got %+v", cliErr)
	}
	// Should not poll past the terminal state.
	if srv.pollCount != 1 {
		t.Fatalf("terminal failure should not keep polling, polls=%d", srv.pollCount)
	}
}

func TestGetReportPollsThenSucceeds(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{
		slug:           "student-answers",
		reportType:     &rt,
		notReadyStates: []string{"queued", "running"},
		csv:            "student_id,x\nPrompt,p\nCorrect answer,c\n1,a\n",
	})
	defer srv.Close()
	result, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	if result.(map[string]any)["complete"] != true {
		t.Fatal("should complete after polling")
	}
}

func TestGetReportOscillation(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{
		slug:           "student-answers",
		reportType:     &rt,
		notReadyStates: []string{"", "queued", "", "queued", "", "queued", ""},
	})
	defer srv.Close()
	_, stderr, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil || cliErr.ExitCode != output.ExitContract {
		t.Fatalf("oscillation should exit 5, got %+v (stderr %s)", cliErr, stderr)
	}
	if !strings.Contains(cliErr.Message, "repeatedly failed to start") {
		t.Fatalf("wrong oscillation message: %q", cliErr.Message)
	}
}

func TestGetReportRepullGuard(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, csv: "student_id,x\n1,a\n"})
	defer srv.Close()
	if _, _, cliErr := runFetch(t, d, srv, fetch1{}); cliErr != nil {
		t.Fatal(cliErr)
	}
	// Second fetch without --refresh should be a usage error.
	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil || cliErr.ExitCode != output.ExitUsage {
		t.Fatalf("re-pull without --refresh should exit 2, got %+v", cliErr)
	}
	// With --refresh it succeeds.
	if _, _, cliErr := runFetch(t, d, srv, fetch1{refresh: true}); cliErr != nil {
		t.Fatalf("refresh should succeed: %+v", cliErr)
	}
}

func TestGetReportSameRunExclusion(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, csv: "student_id,x\n1,a\n"})
	defer srv.Close()

	// A live command holds run 584's report download lock.
	held := storeDownloadLock(t, d, "seg_report_584.lock")
	defer held.Unlock()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil || cliErr.ExitCode != output.ExitInternal || !strings.Contains(cliErr.Message, "download busy") {
		t.Fatalf("second same-run get report should be busy, got %+v", cliErr)
	}
}

func TestGetReportExpiredURLReMints(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	rs := &reportServer{slug: "student-answers", reportType: &rt, csv: "student_id,x\n1,a\n"}
	var s3Calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reports/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/s3"):
			if atomic.AddInt32(&s3Calls, 1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `<Error>expired</Error>`)
				return
			}
			fmt.Fprint(w, rs.csv)
		case strings.HasSuffix(r.URL.Path, "/download"):
			fmt.Fprintf(w, `{"download_url":%q,"filename":"x.csv","expires_in_seconds":600}`, rs.URL+"/api/v1/reports/584/s3")
		default:
			fmt.Fprintf(w, `{"id":584,"report_slug":%q,"report_type":"answers","athena_query_state":"succeeded"}`, rs.slug)
		}
	})
	rs.Server = httptest.NewServer(mux)
	defer rs.Close()

	_, _, cliErr := runFetch(t, d, rs, fetch1{})
	if cliErr != nil {
		t.Fatalf("expired URL should re-mint and succeed, got %+v", cliErr)
	}
	if s3Calls < 2 {
		t.Fatalf("expected a re-mint after the S3 403, s3 calls=%d", s3Calls)
	}
}

func TestGetReportStreamDiscipline(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, csv: "student_id,x\nPrompt,p\nCorrect answer,c\n1,a\n"})
	defer srv.Close()

	// Capture the real stdout stream so we can prove two things about the output
	// path: FetchReport itself writes no prose to stdout (all progress goes to
	// opts.Progress), and rendering the returned result via the CLI's renderer
	// emits exactly one JSON object line with no stray output.
	var out bytes.Buffer
	restore := output.SetStreams(&out, io.Discard)
	defer restore()

	result, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	if out.Len() != 0 {
		t.Fatalf("FetchReport wrote to stdout (must stay clean for the result line): %q", out.String())
	}

	if err := output.ResultLine(result); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one stdout line, got %d: %q", len(lines), out.String())
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &obj); err != nil {
		t.Fatalf("result line not a JSON object: %s", lines[0])
	}
}
