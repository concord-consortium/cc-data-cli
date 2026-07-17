package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
