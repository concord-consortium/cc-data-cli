package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListReportsDrainsPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page_token") {
		case "":
			fmt.Fprint(w, `{"items":[{"id":216,"report_slug":"student-answers","athena_query_state":"succeeded"},{"id":215,"report_slug":"teacher-actions","athena_query_state":"succeeded"}],"next_page_token":"p2"}`)
		case "p2":
			fmt.Fprint(w, `{"items":[{"id":214,"report_slug":"student-assignment-usage","athena_query_state":"succeeded"}],"next_page_token":null}`)
		default:
			t.Errorf("unexpected token")
		}
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	runs, err := cl.ListReports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].ID != 216 || runs[2].ReportSlug != "student-assignment-usage" {
		t.Fatalf("drained runs = %+v", runs)
	}
}

func TestListJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reports/216/jobs" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"items":[{"id":1,"status":"completed","has_result":true}],"next_page_token":null}`)
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	jobs, err := cl.ListJobs(context.Background(), 216)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != 1 || jobs[0].Status != "completed" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

// Captured from POST /api/v1/reports and POST /api/v1/reports/:id/duplicate against the
// report-service test fixture, so the decode is pinned to what the server actually emits.
const (
	createdRunWire      = `{"id":90070,"inserted_at":"2026-09-09T10:56:27Z","report_filter":{"state":null,"filters":["school","cohort"],"app":[],"class":null,"cohort":[1],"school":[51],"teacher":null,"assignment":null,"permission_form":null,"student":null,"country":null,"subject_area":null,"end_date":null,"exclude_internal":false,"hide_names":false,"start_date":null},"report_filter_values":{"cohort":{"1":"Cohort One"},"school":{"51":"School W"}},"report_slug":"student-answers","athena_query_error":null,"athena_query_id":null,"athena_query_state":null,"updated_at":"2026-09-09T10:56:27Z","execution":"async","report_type":"answers"}`
	duplicatedRunWire   = `{"id":90072,"inserted_at":"2026-09-09T10:56:27Z","report_filter":{"state":null,"filters":["cohort"],"app":[],"class":null,"cohort":[1],"school":null,"teacher":null,"assignment":null,"permission_form":null,"student":null,"country":null,"subject_area":null,"end_date":null,"exclude_internal":false,"hide_names":false,"start_date":null},"report_filter_values":{"cohort":{"1":"Cohort One"}},"report_slug":"student-answers","athena_query_error":null,"athena_query_id":null,"athena_query_state":null,"updated_at":"2026-09-09T10:56:27Z","execution":"async","report_type":"answers"}`
	portalDuplicateWire = `{"error":"PORTAL_DUPLICATE_UNNECESSARY","message":"Run 90073 is a Portal report, computed live on every request, so a duplicate returns the same data under a new id. Re-read run 90073 for current data, or pass force: true to duplicate anyway.","run_id":90073}`
	badFilterWire       = `{"error":"BAD_REQUEST","message":"cohort values must be integer ids"}`
)

func TestCreateReportSendsTheFilterAndDecodesTheRun(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/reports" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decoding body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, createdRunWire)
	}))
	defer srv.Close()

	run, err := testClient(srv.URL).CreateReport(context.Background(), CreateReportReq{
		ReportSlug:   "student-answers",
		ReportFilter: json.RawMessage(`{"cohort":[1],"school":[51]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["report_slug"] != "student-answers" {
		t.Fatalf("report_slug = %v", body["report_slug"])
	}
	filter, ok := body["report_filter"].(map[string]any)
	if !ok {
		t.Fatalf("report_filter = %#v, want an object", body["report_filter"])
	}
	if fmt.Sprint(filter["cohort"]) != "[1]" {
		t.Fatalf("report_filter.cohort = %v", filter["cohort"])
	}
	if _, sent := body["report_filter_values"]; sent {
		t.Fatal("report_filter_values must never be sent; the server derives it")
	}
	if run.ID != 90070 || run.ReportSlug != "student-answers" {
		t.Fatalf("run = %+v", run)
	}
	if run.AthenaQueryState != nil {
		t.Fatalf("athena_query_state = %v, want null on a just-created run", *run.AthenaQueryState)
	}
	if got := run.ReportFilterValues["cohort"]; fmt.Sprint(got) != "map[1:Cohort One]" {
		t.Fatalf("report_filter_values.cohort = %v", got)
	}
}

func TestCreateReportOmitsAnAbsentFilter(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decoding body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, createdRunWire)
	}))
	defer srv.Close()

	if _, err := testClient(srv.URL).CreateReport(context.Background(), CreateReportReq{ReportSlug: "student-answers"}); err != nil {
		t.Fatal(err)
	}
	if _, sent := body["report_filter"]; sent {
		t.Fatalf("report_filter was sent as %v when none was given", body["report_filter"])
	}
}

func TestCreateReportSurfacesACodedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, badFilterWire)
	}))
	defer srv.Close()

	_, err := testClient(srv.URL).CreateReport(context.Background(), CreateReportReq{ReportSlug: "student-answers"})
	cliErr := AsCLIError(err)
	if cliErr.Code != CodeBadRequest || !strings.Contains(cliErr.Message, "integer ids") {
		t.Fatalf("cli error = %+v", cliErr)
	}
}

func TestDuplicateReportPostsToTheRunAndOmitsForce(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/reports/90070/duplicate" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decoding body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, duplicatedRunWire)
	}))
	defer srv.Close()

	run, err := testClient(srv.URL).DuplicateReport(context.Background(), 90070, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := body["force"]; sent {
		t.Fatal("force must be absent unless asked for")
	}
	if run.ID != 90072 {
		t.Fatalf("run = %+v", run)
	}
}

func TestDuplicateReportSendsForce(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decoding body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, duplicatedRunWire)
	}))
	defer srv.Close()

	if _, err := testClient(srv.URL).DuplicateReport(context.Background(), 90070, true); err != nil {
		t.Fatal(err)
	}
	if body["force"] != true {
		t.Fatalf("force = %v, want true", body["force"])
	}
}

// The guard's run_id reaches the CLI envelope and the MCP result without either surface naming it,
// which is why the server pins the body's keys.
func TestDuplicateReportForwardsTheGuardAndItsRunID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, portalDuplicateWire)
	}))
	defer srv.Close()

	_, err := testClient(srv.URL).DuplicateReport(context.Background(), 90073, false)
	cliErr := AsCLIError(err)
	if cliErr.Code != CodePortalDuplicateUnnecessary {
		t.Fatalf("code = %s", cliErr.Code)
	}
	if !strings.Contains(cliErr.Message, "Re-read run 90073") {
		t.Fatalf("message = %s", cliErr.Message)
	}
	if fmt.Sprint(cliErr.Envelope()["run_id"]) != "90073" {
		t.Fatalf("envelope = %v", cliErr.Envelope())
	}
}
