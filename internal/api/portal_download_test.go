package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Captured from GET /api/v1/reports/:id, GET /api/v1/reports/:id/download and GET
// /api/v1/reports/:id/answers for a student-id-mapping run against the report-service test
// fixture, so the decode is pinned to what the server actually emits rather than to what this
// package expects. The download's headers are captured with them: it is sent chunked, so it
// carries no Content-Length. The client does not branch on the content type, so the fake sends
// what the server sends without that being a contract: a 2xx body is written to the file whatever
// it is labeled, and a refusal is identified by its status.
const (
	portalRunWire = `{"id":101342,"inserted_at":"2026-09-09T17:52:13Z","report_filter":{"state":null,"filters":["class"],"app":[],"class":[601],"cohort":null,"school":null,"teacher":null,"assignment":null,"permission_form":null,"student":null,"country":null,"subject_area":null,"end_date":null,"exclude_internal":false,"hide_names":false,"start_date":null},"report_filter_values":{},"report_slug":"student-id-mapping","athena_query_error":null,"athena_query_id":null,"athena_query_state":null,"updated_at":"2026-09-09T17:52:13Z","execution":"sync","report_type":null}`

	portalDownloadWire = "learner_id,user_id,primary_user_id,student_id,class_id,offering_id,runnable_url,run_remote_endpoint\n" +
		"901,11,11,s901,601,70,https://act/1,https://portal/dataservice/external_activity_data/AAA\n" +
		"902,12,12,s902,601,70,https://act/1,https://portal/dataservice/external_activity_data/\n"

	portalDownloadContentType = "text/csv; charset=utf-8"

	portalAnswersWire = `{"items":[{"id":"a1","question_id":"q1","remote_endpoint":"https://portal/dataservice/external_activity_data/AAA","source_key":"activity-player.concord.org"}],"next_page_token":null,"total_endpoints":1}`
)

// A Portal run declares no api_report_type by design and has no query to be in a state, so
// execution is the only field that identifies it.
func TestPortalRunDecodesFromTheWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reports/101342" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, portalRunWire)
	}))
	defer srv.Close()

	run, err := testClient(srv.URL).GetReport(context.Background(), 101342)
	if err != nil {
		t.Fatal(err)
	}
	if run.Execution != ExecutionSync {
		t.Errorf("execution = %q, want %q", run.Execution, ExecutionSync)
	}
	if run.ReportType != nil {
		t.Errorf("report_type = %v, want null", *run.ReportType)
	}
	if run.AthenaQueryState != nil {
		t.Errorf("athena_query_state = %v, want null", *run.AthenaQueryState)
	}
	if run.ReportSlug != "student-id-mapping" {
		t.Errorf("report_slug = %q", run.ReportSlug)
	}
	// The filter the download records, and what hide_names is read from.
	var filter map[string]any
	if err := json.Unmarshal(run.ReportFilter, &filter); err != nil {
		t.Fatalf("report_filter did not decode: %v", err)
	}
	if filter["hide_names"] != false {
		t.Errorf("hide_names = %v, want false", filter["hide_names"])
	}
}

// The response is chunked with no Content-Length, which is what raised the question of whether a
// mid-stream failure would arrive as a short but apparently successful read.
func TestPortalDownloadStreamsTheCapturedCSV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reports/101342/download" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", portalDownloadContentType)
		w.Header().Set("Content-Disposition", `attachment; filename="student-id-mapping-run-101342.csv"`)
		fmt.Fprint(w, portalDownloadWire)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "report_101342.csv")
	if err := testClient(srv.URL).StreamReportCSV(context.Background(), 101342, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != portalDownloadWire {
		t.Fatalf("wrote %q", got)
	}
}

// The bulk endpoints derive learners from the run's filter for any report that derives learner
// data, and both Portal student reports do, so a mapping run id drives them with no client change.
func TestPortalRunDrivesTheAnswersPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reports/101342/answers" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, portalAnswersWire)
	}))
	defer srv.Close()

	page, err := FetchBulkPage(context.Background(), testClient(srv.URL), "/api/v1/reports/101342/answers", BulkPageMax, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	if page.TotalEndpoints == nil || *page.TotalEndpoints != 1 {
		t.Fatalf("total_endpoints = %v, want 1", page.TotalEndpoints)
	}
	if page.NextPageToken != nil {
		t.Fatalf("next_page_token = %v, want null", *page.NextPageToken)
	}
	var rec map[string]any
	if err := json.Unmarshal(page.Items[0], &rec); err != nil {
		t.Fatal(err)
	}
	// The stored identity tuple, unchanged by the run kind that produced it and carrying no
	// learner_id: that lives only in the two dimension CSVs.
	for _, key := range []string{"source_key", "remote_endpoint", "question_id"} {
		if rec[key] == nil {
			t.Errorf("the record is missing its identity column %q: %v", key, rec)
		}
	}
	if _, ok := rec["learner_id"]; ok {
		t.Errorf("a fetched record carries a learner_id: %v", rec)
	}
}
