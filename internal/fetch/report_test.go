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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
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
	d, err := dataset.Create(root, dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "ds"}, "")
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

// reportServer is a fake report + S3 server for report-fetch tests. A sync execution makes it
// answer /download the way a Portal run does, computing the CSV and streaming it as text/csv.
type reportServer struct {
	*httptest.Server
	slug           string
	reportType     *string
	execution      string
	notReadyStates []string // states returned before ready; "" means null
	csv            string
	reportFilter   string // the run's filter and its resolved labels, as the server emits them
	filterValues   string
	portalRefusal  string // a 422 error envelope the Portal download answers with instead
	notReadyBody   string // the exact 409 body for every not-ready poll; notReadyStates then only sets how many
	pollCount      int32
	stateIndex     int32
}

func newReportServer(t *testing.T, s *reportServer) *reportServer {
	if s.execution == "" {
		s.execution = api.ExecutionAsync
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reports/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/download"):
			s.handleDownload(w, r)
		case strings.HasSuffix(path, "/s3"):
			w.Write([]byte(s.csv))
		default:
			s.handleShow(w)
		}
	})
	s.Server = httptest.NewServer(mux)
	return s
}

func (s *reportServer) handleShow(w http.ResponseWriter) {
	rt, state := "null", `"succeeded"`
	if s.reportType != nil {
		rt = fmt.Sprintf("%q", *s.reportType)
	}
	// A Portal run declares no api_report_type and has no query to be in a state.
	if s.execution == api.ExecutionSync {
		rt, state = "null", "null"
	}
	filter, values := s.reportFilter, s.filterValues
	if filter == "" {
		filter = "null"
	}
	if values == "" {
		values = "{}"
	}
	fmt.Fprintf(w, `{"id":584,"report_slug":%q,"report_type":%s,"athena_query_state":%s,"execution":%q,"report_filter":%s,"report_filter_values":%s}`,
		s.slug, rt, state, s.execution, filter, values)
}

func (s *reportServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&s.pollCount, 1)
	if s.execution == api.ExecutionSync {
		if s.portalRefusal != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, s.portalRefusal)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, s.csv)
		return
	}
	i := atomic.LoadInt32(&s.stateIndex)
	if int(i) < len(s.notReadyStates) {
		atomic.AddInt32(&s.stateIndex, 1)
		state := s.notReadyStates[i]
		w.WriteHeader(http.StatusConflict)
		if s.notReadyBody != "" {
			fmt.Fprint(w, s.notReadyBody)
			return
		}
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

// Captured from GET /api/v1/reports/:id/download against failed runs on the report-service test
// fixture, so these tests are pinned to what the server emits rather than to what this package
// expects. The job body comes from the jobs route, which has no Athena query of its own.
const (
	failedMappedWire     = `{"error":"NOT_READY","message":"The report is not ready to download.","athena_query_error":"HIVE_EXCEEDED_PARTITION_LIMIT: too many","athena_query_id":"qid-failed","athena_query_state":"failed","athena_query_guidance":"This query covers too many Athena partitions. Narrow it with a date range or one or more applications and run it again."}`
	failedReasonlessWire = `{"error":"NOT_READY","message":"The report is not ready to download.","athena_query_error":null,"athena_query_id":"qid-failed","athena_query_state":"failed","athena_query_guidance":null}`
	failedUnmappedWire   = `{"error":"NOT_READY","message":"The report is not ready to download.","athena_query_error":"WEIRD_NEW_CODE: something Athena has not said before","athena_query_id":"qid-failed","athena_query_state":"failed","athena_query_guidance":null}`
	failedJobWire        = `{"error":"NOT_READY","message":"The job result is not ready to download.","status":"failed"}`
)

// Synthetic rather than captured: today's server sends null for a reason it has no guidance for,
// so an empty string is a shape only a later server could produce.
const failedEmptyGuidanceWire = `{"error":"NOT_READY","message":"The report is not ready to download.","athena_query_error":"WEIRD_NEW_CODE: something Athena has not said before","athena_query_id":"qid-failed","athena_query_state":"failed","athena_query_guidance":""}`

// Synthetic rather than captured: a body from a server that has grown a field this client predates,
// which the passthrough has to carry without knowing it.
const failedUnknownFieldWire = `{"error":"NOT_READY","message":"The report is not ready to download.","athena_query_error":"HIVE_EXCEEDED_PARTITION_LIMIT: too many","athena_query_id":"qid-failed","athena_query_state":"failed","athena_query_guidance":"Narrow it.","athena_query_bytes_scanned":1234}`

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
	// This server sends only the state, as one without the failure fields does.
	got, err := json.Marshal(cliErr.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"athena_query_state":"failed","error":"NOT_READY","message":"run 584 is in terminal state \"failed\"; nothing to download"}`
	if string(got) != want {
		t.Errorf("envelope = %s, want %s", got, want)
	}
}

func TestGetReportTerminalFailureCarriesTheServersFields(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}, notReadyBody: failedMappedWire})
	defer srv.Close()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil {
		t.Fatal("terminal failure should error")
	}
	env := cliErr.Envelope()
	for k, want := range map[string]any{
		"athena_query_state": "failed",
		"athena_query_id":    "qid-failed",
		"athena_query_error": "HIVE_EXCEEDED_PARTITION_LIMIT: too many",
	} {
		if env[k] != want {
			t.Errorf("envelope[%q] = %v, want %v", k, env[k], want)
		}
	}
	if !strings.Contains(env["message"].(string), "terminal state") {
		t.Errorf("the client's own message was lost: %v", env["message"])
	}
}

func TestGetReportReasonlessFailureCarriesNoNullKeys(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}, notReadyBody: failedReasonlessWire})
	defer srv.Close()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil {
		t.Fatal("terminal failure should error")
	}
	env := cliErr.Envelope()
	if env["athena_query_state"] != "failed" {
		t.Errorf("state = %v", env["athena_query_state"])
	}
	for _, k := range []string{"athena_query_error", "athena_query_guidance"} {
		if _, present := env[k]; present {
			t.Errorf("a null the server sent reached the envelope as %q", k)
		}
	}
}

func wireField(t *testing.T, wire, key string) any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(wire), &body); err != nil {
		t.Fatal(err)
	}
	return body[key]
}

func TestGetReportMappedReasonBecomesTheAction(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}, notReadyBody: failedMappedWire})
	defer srv.Close()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil {
		t.Fatal("terminal failure should error")
	}
	env := cliErr.Envelope()
	if env["action"] != wireField(t, failedMappedWire, api.FieldAthenaQueryGuidance) {
		t.Errorf("action = %v, want the guidance the server sent", env["action"])
	}
	if _, present := env[api.FieldAthenaQueryGuidance]; present {
		t.Errorf("the guidance is in the envelope twice: %v", env)
	}
	if env["athena_query_error"] != "HIVE_EXCEEDED_PARTITION_LIMIT: too many" || env["athena_query_id"] != "qid-failed" || env["athena_query_state"] != "failed" {
		t.Errorf("the other fields did not survive the promotion: %v", env)
	}
}

func TestGetReportUnmappedReasonLeavesNoAction(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}, notReadyBody: failedUnmappedWire})
	defer srv.Close()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil {
		t.Fatal("terminal failure should error")
	}
	env := cliErr.Envelope()
	if _, present := env["action"]; present {
		t.Errorf("an unmapped reason should leave action absent, not empty: %v", env)
	}
	if env["athena_query_error"] != "WEIRD_NEW_CODE: something Athena has not said before" {
		t.Errorf("the raw reason is the authority and must survive: %v", env)
	}
}

func TestGetReportForwardsAFieldThisClientDoesNotKnow(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}, notReadyBody: failedUnknownFieldWire})
	defer srv.Close()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil {
		t.Fatal("terminal failure should error")
	}
	env := cliErr.Envelope()
	if env["athena_query_bytes_scanned"] != float64(1234) {
		t.Errorf("a field the client predates should still reach the envelope: %v", env)
	}
	if env["action"] != wireField(t, failedUnknownFieldWire, api.FieldAthenaQueryGuidance) {
		t.Errorf("action = %v, want the guidance the server sent", env["action"])
	}
}

func TestGetReportEmptyGuidanceLeavesNoActionAndNoKey(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}, notReadyBody: failedEmptyGuidanceWire})
	defer srv.Close()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil {
		t.Fatal("terminal failure should error")
	}
	env := cliErr.Envelope()
	if _, present := env["action"]; present {
		t.Errorf("an empty guidance should leave action absent, not empty: %v", env)
	}
	if _, present := env[api.FieldAthenaQueryGuidance]; present {
		t.Errorf("the guidance key reached the caller under its own name: %v", env)
	}
	if env["athena_query_error"] != "WEIRD_NEW_CODE: something Athena has not said before" {
		t.Errorf("the raw reason is the authority and must survive: %v", env)
	}
}

func TestGetReportJobFailureEnvelopeIsUnchanged(t *testing.T) {
	d := newTestDataset(t)
	rt := "answers"
	srv := newReportServer(t, &reportServer{slug: "student-answers", reportType: &rt, notReadyStates: []string{"failed"}, notReadyBody: failedJobWire})
	defer srv.Close()

	jobID := 3
	_, _, cliErr := runFetch(t, d, srv, fetch1{jobID: &jobID})
	if cliErr == nil {
		t.Fatal("a failed job should error")
	}
	got, err := json.Marshal(cliErr.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":"NOT_READY","message":"run 584 is in terminal state \"failed\"; nothing to download","status":"failed"}`
	if string(got) != want {
		t.Errorf("job envelope changed:\n got %s\nwant %s", got, want)
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

const portalMappingCSV = "learner_id,user_id,run_remote_endpoint\n" +
	"901,11,https://portal/dataservice/external_activity_data/AAA\n" +
	"902,12,https://portal/dataservice/external_activity_data/BBB\n"

func newPortalServer(t *testing.T) *reportServer {
	t.Helper()
	return newReportServer(t, &reportServer{
		slug:      "student-id-mapping",
		execution: api.ExecutionSync,
		csv:       portalMappingCSV,
	})
}

func TestGetReportPortalRunDownloadsTheStreamedCSV(t *testing.T) {
	d := newTestDataset(t)
	srv := newPortalServer(t)
	defer srv.Close()

	result, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr != nil {
		t.Fatalf("unexpected error: %+v", cliErr)
	}
	if m := result.(map[string]any); m["complete"] != true || m["row_count"].(int) != 2 {
		t.Fatalf("result = %+v", m)
	}
	got, err := os.ReadFile(d.Path("report_584.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != portalMappingCSV {
		t.Fatalf("CSV = %q", got)
	}
	man, _ := d.ReadManifest()
	if len(man.Downloads) != 1 {
		t.Fatalf("expected 1 download, got %d", len(man.Downloads))
	}
	dl := man.Downloads[0]
	if dl.Type != typeReport || dl.RunID != 584 || dl.Slug != "student-id-mapping" || !dl.Complete {
		t.Fatalf("manifest entry = %+v", dl)
	}
	// The lock, the manifest entry and the detection are shared with the Athena path, so the
	// entry a Portal download records carries the same shape a presigned one does.
	if dl.RowCount == nil || *dl.RowCount != 2 || dl.CSVDialect == nil || dl.Columns["learner_id"] == "" {
		t.Fatalf("manifest entry lost the detected shape: %+v", dl)
	}
}

// Re-downloading is the only way to get current data from a live report, and the server's
// duplicate guard sends callers here to do it, so the refusal cannot read as "this is redundant".
func TestGetReportPortalRepullGuardNamesLiveData(t *testing.T) {
	d := newTestDataset(t)
	srv := newPortalServer(t)
	defer srv.Close()

	if _, _, cliErr := runFetch(t, d, srv, fetch1{}); cliErr != nil {
		t.Fatal(cliErr)
	}
	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil || cliErr.ExitCode != output.ExitUsage || cliErr.Code != "EXISTS" {
		t.Fatalf("re-pull without --refresh should be a usage error: %+v", cliErr)
	}
	for _, want := range []string{"live", "--refresh", "re-pull current data"} {
		if !strings.Contains(cliErr.Message, want) {
			t.Errorf("the refusal does not name %q: %q", want, cliErr.Message)
		}
	}
	if _, _, cliErr := runFetch(t, d, srv, fetch1{refresh: true}); cliErr != nil {
		t.Fatalf("refresh should re-pull: %+v", cliErr)
	}
}

// The flag promises "do not poll, report and exit", and a sync run has nothing to poll, so the
// promise is kept by construction. Refusing it would break one --no-wait over a mixed run list.
func TestGetReportPortalIgnoresNoWait(t *testing.T) {
	d := newTestDataset(t)
	srv := newPortalServer(t)
	defer srv.Close()

	result, _, cliErr := runFetch(t, d, srv, fetch1{noWait: true})
	if cliErr != nil {
		t.Fatalf("--no-wait on a Portal run should download normally: %+v", cliErr)
	}
	if m := result.(map[string]any); m["complete"] != true {
		t.Fatalf("result = %+v", m)
	}
}

// Everything downstream of the fork reads opts.JobID, so passing one through would store the run's
// own CSV under a job's filename, manifest type and per-download view.
func TestGetReportPortalRefusesAJob(t *testing.T) {
	d := newTestDataset(t)
	srv := newPortalServer(t)
	defer srv.Close()

	jobID := 3
	_, _, cliErr := runFetch(t, d, srv, fetch1{jobID: &jobID})
	if cliErr == nil || cliErr.ExitCode != output.ExitUsage {
		t.Fatalf("--job on a Portal run should be a usage error: %+v", cliErr)
	}
	if !strings.Contains(cliErr.Message, "Portal report") || !strings.Contains(cliErr.Message, "--job") {
		t.Fatalf("the refusal names neither the report kind nor the flag: %q", cliErr.Message)
	}
	if _, err := os.Stat(d.Path("report_584_job_3.csv")); !os.IsNotExist(err) {
		t.Fatalf("a refused --job still wrote a CSV: %v", err)
	}
}

// The one refusal only the Portal path can produce. Nothing retries it, and its message is the
// server's own.
func TestGetReportPortalFilterlessRunSurfacesItsRefusal(t *testing.T) {
	d := newTestDataset(t)
	srv := newReportServer(t, &reportServer{
		slug:          "student-id-mapping",
		execution:     api.ExecutionSync,
		portalRefusal: `{"error":"UNPROCESSABLE","message":"This report run has no filters and cannot be downloaded."}`,
	})
	defer srv.Close()

	_, _, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr == nil || cliErr.Code != "UNPROCESSABLE" {
		t.Fatalf("err = %+v, want the server's coded refusal", cliErr)
	}
	if !strings.Contains(cliErr.Message, "no filters") {
		t.Fatalf("message = %q", cliErr.Message)
	}
	if atomic.LoadInt32(&srv.pollCount) != 1 {
		t.Fatalf("a contract refusal was requested %d times, want 1", srv.pollCount)
	}
	if _, err := os.Stat(d.Path("report_584.csv")); !os.IsNotExist(err) {
		t.Fatalf("a refused download left a CSV: %v", err)
	}
}

func TestGetReportTypesAPortalRunFromItsExecution(t *testing.T) {
	d := newTestDataset(t)
	srv := newPortalServer(t)
	defer srv.Close()

	result, stderr, cliErr := runFetch(t, d, srv, fetch1{})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	if m := result.(map[string]any); m["report_type"] != dataset.ReportTypePortal {
		t.Fatalf("report_type = %v, want %q", m["report_type"], dataset.ReportTypePortal)
	}
	man, _ := d.ReadManifest()
	if man.Downloads[0].ReportType != dataset.ReportTypePortal {
		t.Fatalf("manifest report_type = %q", man.Downloads[0].ReportType)
	}
	// Recorded verbatim, the slug would have warned here and then vanished from the reports view.
	if stderr != "" {
		t.Fatalf("a recognized Portal report warned: %q", stderr)
	}
}

// The regression guard for the execution check: without it the new branch swallows the slug
// fallback and every async run with a null report type is typed portal.
func TestGetReportStillDerivesAnAsyncTypeFromTheSlug(t *testing.T) {
	for _, tc := range []struct {
		name, slug, want string
		warns            bool
	}{
		{"a known slug", "student-actions-with-metadata", dataset.ReportTypeLog, false},
		{"an unknown slug", "student-something-new", "student-something-new", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDataset(t)
			srv := newReportServer(t, &reportServer{slug: tc.slug, csv: "student_id,x\n1,a\n"})
			defer srv.Close()

			result, stderr, cliErr := runFetch(t, d, srv, fetch1{})
			if cliErr != nil {
				t.Fatal(cliErr)
			}
			if m := result.(map[string]any); m["report_type"] != tc.want {
				t.Fatalf("report_type = %v, want %q", m["report_type"], tc.want)
			}
			if warned := strings.Contains(stderr, "unknown to this cc-data version"); warned != tc.warns {
				t.Fatalf("warned = %v, want %v: %q", warned, tc.warns, stderr)
			}
		})
	}
}

// The recorded filter is what the metadata view's hide_names column is derived from, so a
// download that does not carry it leaves that column NULL for every one of its rows.
func TestGetReportRecordsTheRunsFilter(t *testing.T) {
	d := newTestDataset(t)
	srv := newReportServer(t, &reportServer{
		slug:         "student-metadata",
		execution:    api.ExecutionSync,
		csv:          "learner_id,student_name\n901,Ada\n",
		reportFilter: `{"cohort":[1],"hide_names":true}`,
		filterValues: `{"cohort":{"1":"Cohort One"}}`,
	})
	defer srv.Close()

	if _, _, cliErr := runFetch(t, d, srv, fetch1{}); cliErr != nil {
		t.Fatal(cliErr)
	}
	dl := firstDownload(t, d)
	var filter map[string]any
	if err := json.Unmarshal(dl.Filters, &filter); err != nil {
		t.Fatalf("the run's filter was not recorded: %v", err)
	}
	if filter["hide_names"] != true {
		t.Fatalf("filter = %v, want the run's own hide_names", filter)
	}
	// Rendered through the shared helper rather than a second copy of the label rules.
	if want := []string{"cohort: Cohort One"}; !slices.Equal(dl.FilterLabels, want) {
		t.Fatalf("filter_labels = %v, want %v", dl.FilterLabels, want)
	}
}

func TestGetReportRecordsNoFilterForAFilterlessRun(t *testing.T) {
	d := newTestDataset(t)
	srv := newReportServer(t, &reportServer{slug: "student-answers", csv: "student_id,x\n1,a\n"})
	defer srv.Close()

	if _, _, cliErr := runFetch(t, d, srv, fetch1{}); cliErr != nil {
		t.Fatal(cliErr)
	}
	dl := firstDownload(t, d)
	if len(dl.FilterLabels) != 0 {
		t.Fatalf("a filter-less run recorded labels: %v", dl.FilterLabels)
	}
}

func firstDownload(t *testing.T, d *dataset.Dataset) dataset.Download {
	t.Helper()
	man, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Downloads) != 1 {
		t.Fatalf("expected 1 download, got %d", len(man.Downloads))
	}
	return man.Downloads[0]
}
