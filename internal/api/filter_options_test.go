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

// Captured from POST /api/v1/reports/filter-options against the report-service test fixture, so
// the decode is pinned to what the server actually emits rather than to what this package expects.
const (
	firstPageWire = `{"count":9,"count_skipped":false,"count_skipped_reason":null,"items":[{"id":"3","label":""},{"id":"4","label":""},{"id":"2","label":"Adams (a)"}],"next_page_token":"WyJBZGFtcyAoYSkiLCIyIl0"}`
	nextPageWire  = `{"count":null,"count_skipped":false,"count_skipped_reason":null,"items":[{"id":"601","label":"Class 601 (c)"},{"id":"602","label":"Class 602 (c)"},{"id":"5","label":"Lincoln High (sec)"}],"next_page_token":null}`
	skippedWire   = `{"count":null,"count_skipped":true,"count_skipped_reason":"counting every student without a narrowing selection is unbounded","items":[{"id":"74","label":"Stu Four <104>"}],"next_page_token":null}`
)

// decodeBody runs on the server's goroutine, where FailNow is not allowed, so a bad body is
// reported with Errorf and returned as nil rather than stopping the test from the wrong place.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("reading request body: %v", err)
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Errorf("decoding request body %q: %v", raw, err)
		return nil
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

	drained, err := testClient(srv.URL).DrainFilterOptions(context.Background(), FilterOptionsReq{Dimension: "class"})
	if err != nil {
		t.Fatal(err)
	}
	if len(drained.Items) != 6 || drained.Items[5].ID != "5" {
		t.Fatalf("options = %+v", drained.Items)
	}
	if drained.Count == nil || *drained.Count != 9 {
		t.Fatalf("count = %v, want the first page's 9", drained.Count)
	}
	// The walk consumed every page, so there is nothing left to continue from.
	if drained.NextPageToken != nil {
		t.Fatalf("a drained walk must not hand back a token, got %q", *drained.NextPageToken)
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
	// Routing the stub on whether a token is present, not on its value, would pass for a walk
	// that asked for the wrong page.
	if bodies[1]["page_token"] != "WyJBZGFtcyAoYSkiLCIyIl0" {
		t.Fatalf("second request asked for page %v, want the first page's next_page_token", bodies[1]["page_token"])
	}
}

func TestDrainFilterOptionsStopsOnARepeatedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, firstPageWire)
	}))
	defer srv.Close()

	_, err := testClient(srv.URL).DrainFilterOptions(context.Background(), FilterOptionsReq{Dimension: "class"})
	if err == nil {
		t.Fatal("a server repeating a page token must stop the walk, not loop forever")
	}
	if !strings.Contains(err.Error(), "repeated page token") {
		t.Fatalf("stopped for the wrong reason: %v", err)
	}
}

// A dimension bounded by the size of a portal rather than by one researcher's work must not walk
// without end. The cap stops at a page boundary and says where it stopped.
func TestDrainFilterOptionsStopsAtTheCap(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		items := make([]string, 0, 400)
		for i := 0; i < 400; i++ {
			items = append(items, fmt.Sprintf(`{"id":"%d","label":"Student %d"}`, page*1000+i, i))
		}
		fmt.Fprintf(w, `{"count":null,"count_skipped":false,"count_skipped_reason":null,"items":[%s],"next_page_token":"page-%d"}`,
			strings.Join(items, ","), page+1)
	}))
	defer srv.Close()

	drained, err := testClient(srv.URL).DrainFilterOptions(context.Background(), FilterOptionsReq{Dimension: "student"})
	if err != nil {
		t.Fatal(err)
	}
	if !drained.Truncated {
		t.Error("a walk that stopped at the cap must say so")
	}
	if drained.NextPageToken == nil || *drained.NextPageToken == "" {
		t.Fatal("a truncated walk must hand back the token it stopped on")
	}
	if len(drained.Items) < FilterOptionsDrainMax {
		t.Errorf("stopped early at %d items, cap is %d", len(drained.Items), FilterOptionsDrainMax)
	}
	if page > (FilterOptionsDrainMax/400)+1 {
		t.Errorf("kept walking past the cap: %d pages", page)
	}
}

func TestFilterOptionsForKeepsTheWholeEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, firstPageWire)
	}))
	defer srv.Close()

	// A single page has to carry the token, or a caller that passed page_token cannot continue.
	page, err := testClient(srv.URL).FilterOptionsFor(context.Background(), FilterOptionsReq{Dimension: "class"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextPageToken == nil || *page.NextPageToken != "WyJBZGFtcyAoYSkiLCIyIl0" {
		t.Fatalf("next_page_token = %v", page.NextPageToken)
	}
	if page.Count == nil || *page.Count != 9 || page.CountSkipped {
		t.Fatalf("count fields = %v / %v", page.Count, page.CountSkipped)
	}
}

func TestDrainFilterOptionsKeepsARefusedCountDistinct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, skippedWire)
	}))
	defer srv.Close()

	drained, err := testClient(srv.URL).DrainFilterOptions(context.Background(), FilterOptionsReq{Dimension: "student"})
	if err != nil {
		t.Fatal(err)
	}
	// A nil Count alone cannot say whether the server refused or was never asked.
	if !drained.CountSkipped || drained.CountSkippedReason == nil {
		t.Fatalf("a refused count must survive the drain: %+v", drained)
	}
}
