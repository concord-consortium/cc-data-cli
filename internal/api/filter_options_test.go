package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Captured from POST /api/v1/reports/filter-options against the report-service test fixture, so
// the decode is pinned to what the server actually emits rather than to what this package expects.
const (
	firstPageWire = `{"count":9,"count_skipped":false,"count_skipped_reason":null,"items":[{"id":"3","label":""},{"id":"4","label":""},{"id":"2","label":"Adams (a)"}],"next_page_token":"WyJBZGFtcyAoYSkiLCIyIl0"}`
	nextPageWire  = `{"count":null,"count_skipped":false,"count_skipped_reason":null,"items":[{"id":"601","label":"Class 601 (c)"},{"id":"602","label":"Class 602 (c)"},{"id":"5","label":"Lincoln High (sec)"}],"next_page_token":null}`
	skippedWire   = `{"count":null,"count_skipped":true,"count_skipped_reason":"counting every student without a narrowing selection is unbounded","items":[{"id":"74","label":"Stu Four <104>"}],"next_page_token":null}`
)

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestFilterOptionsDecodesTheCountFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/reports/filter-options" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, firstPageWire)
	}))
	defer srv.Close()

	page, err := testClient(srv.URL).FilterOptions(context.Background(), FilterOptionsReq{Dimension: "class"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Count == nil || *page.Count != 9 {
		t.Fatalf("count = %v, want 9", page.Count)
	}
	if page.CountSkipped {
		t.Fatal("count_skipped should be false when a count was produced")
	}
	if len(page.Items) != 3 || page.Items[2] != (FilterOption{ID: "2", Label: "Adams (a)"}) {
		t.Fatalf("items = %+v", page.Items)
	}
	// A coalesced label arrives as an empty string, not as a missing field.
	if page.Items[0].Label != "" || page.Items[0].ID != "3" {
		t.Fatalf("first item = %+v", page.Items[0])
	}
}

func TestFilterOptionsDistinguishesSkippedFromNotRequested(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wire        string
		wantSkipped bool
		wantReason  bool
	}{
		{"not requested", nextPageWire, false, false},
		{"refused", skippedWire, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.wire)
			}))
			defer srv.Close()

			page, err := testClient(srv.URL).FilterOptions(context.Background(), FilterOptionsReq{Dimension: "student"})
			if err != nil {
				t.Fatal(err)
			}
			if page.Count != nil {
				t.Fatalf("count = %v, want nil", *page.Count)
			}
			if page.CountSkipped != tc.wantSkipped {
				t.Fatalf("count_skipped = %v, want %v", page.CountSkipped, tc.wantSkipped)
			}
			if (page.CountSkippedReason != nil) != tc.wantReason {
				t.Fatalf("count_skipped_reason = %v", page.CountSkippedReason)
			}
		})
	}
}

func TestFilterOptionsSendsOnlyTheFieldsItWasGiven(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = decodeBody(t, r)
		fmt.Fprint(w, firstPageWire)
	}))
	defer srv.Close()

	if _, err := testClient(srv.URL).FilterOptions(context.Background(), FilterOptionsReq{Dimension: "class"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["dimension"] != "class" {
		t.Fatalf("body = %+v, want only the dimension", got)
	}
}

func TestFilterOptionsPassesTheFilterThrough(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = decodeBody(t, r)
		fmt.Fprint(w, firstPageWire)
	}))
	defer srv.Close()

	// The blob is what reports_list handed back on a run: the client never decodes it.
	filter := json.RawMessage(`{"class":[601],"cohort":null,"hide_names":false}`)
	req := FilterOptionsReq{Dimension: "student", ReportSlug: "student-actions", Search: "smi", Limit: 25, ReportFilter: filter}

	if _, err := testClient(srv.URL).FilterOptions(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got["report_slug"] != "student-actions" || got["search"] != "smi" || got["limit"] != float64(25) {
		t.Fatalf("body = %+v", got)
	}
	sent, ok := got["report_filter"].(map[string]any)
	if !ok {
		t.Fatalf("report_filter = %T, want an object", got["report_filter"])
	}
	if _, present := sent["cohort"]; !present {
		t.Fatal("an explicit null must survive: it means something different from an empty list")
	}
	if len(sent["class"].([]any)) != 1 {
		t.Fatalf("class = %+v", sent["class"])
	}
}

func TestDrainFilterOptionsWalksAndCountsOnce(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		bodies = append(bodies, body)
		if body["page_token"] == nil {
			fmt.Fprint(w, firstPageWire)
			return
		}
		fmt.Fprint(w, nextPageWire)
	}))
	defer srv.Close()

	options, count, err := testClient(srv.URL).DrainFilterOptions(context.Background(), FilterOptionsReq{Dimension: "class"})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 6 || options[5].ID != "5" {
		t.Fatalf("options = %+v", options)
	}
	if count == nil || *count != 9 {
		t.Fatalf("count = %v, want the first page's 9", count)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if _, asked := bodies[0]["include_count"]; asked {
		t.Fatal("the first page should use the server's default rather than overriding it")
	}
	if bodies[1]["include_count"] != false {
		t.Fatalf("later pages must decline the count, got %v", bodies[1]["include_count"])
	}
}

func TestDrainFilterOptionsStopsOnARepeatedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, firstPageWire)
	}))
	defer srv.Close()

	_, _, err := testClient(srv.URL).DrainFilterOptions(context.Background(), FilterOptionsReq{Dimension: "class"})
	if err == nil {
		t.Fatal("a server repeating a page token must stop the walk, not loop forever")
	}
}
