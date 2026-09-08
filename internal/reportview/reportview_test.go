package reportview

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/api"
)

func TestFilterLabels(t *testing.T) {
	run := api.ReportRun{
		ReportFilterValues: map[string]any{
			"class":   "Period 3",
			"teacher": []any{"Ms. A", "Mr. B"},
			"school":  nil,
		},
	}
	got := FilterLabels(run)
	want := []string{"class: Period 3", "teacher: Ms. A/Mr. B"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterLabels = %v, want %v", got, want)
	}
}

func TestFilterLabelsResolvedMap(t *testing.T) {
	// A resolved id->label map (as the server sends for assignment/teacher) must
	// render its labels, not the raw Go map.
	run := api.ReportRun{
		ReportFilterValues: map[string]any{
			"assignment": map[string]any{"573": "Demo Saved Interactive State History"},
			"teacher":    map[string]any{"14": "Doug Martin"},
		},
	}
	got := FilterLabels(run)
	want := []string{"assignment: Demo Saved Interactive State History", "teacher: Doug Martin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterLabels = %v, want %v", got, want)
	}
}

func TestFilterlessRunHasNoLabels(t *testing.T) {
	run := api.ReportRun{ReportFilterValues: map[string]any{}}
	if got := FilterLabels(run); len(got) != 0 {
		t.Fatalf("filter-less run should have no labels, got %v", got)
	}
}

func TestStateText(t *testing.T) {
	s := "running"
	if StateText(&s) != "running" {
		t.Fatal("state text wrong")
	}
	if StateText(nil) != "(none)" {
		t.Fatal("nil state should render (none)")
	}
}

func TestToRunJSON(t *testing.T) {
	rt := "answers"
	state := "succeeded"
	run := api.ReportRun{ID: 216, ReportSlug: "student-answers", ReportType: &rt, AthenaQueryState: &state}
	j := ToRunJSON(run)
	if j.RunID != 216 || j.Slug != "student-answers" || j.State != "succeeded" || j.ReportType != "answers" {
		t.Fatalf("json = %+v", j)
	}
}

func TestFilterOptionsPayload(t *testing.T) {
	total := 9
	token := "eyJ0b2tlbiI6MX0"
	payload := FilterOptions(api.FilterOptionsPage{
		Items:         []api.FilterOption{{ID: "3", Label: ""}, {ID: "2", Label: "Adams (a)"}},
		NextPageToken: &token,
		Count:         &total,
	})

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	options, ok := got["options"].([]any)
	if !ok || len(options) != 2 {
		t.Fatalf("options = %+v", got["options"])
	}
	first := options[0].(map[string]any)
	if first["id"] != "3" || first["label"] != "" {
		t.Fatalf("a coalesced empty label must keep its row: %+v", first)
	}
	if got["count"] != float64(9) {
		t.Fatalf("count = %v", got["count"])
	}
	// Without the token a caller that paged has no way to continue.
	if got["next_page_token"] != "eyJ0b2tlbiI6MX0" {
		t.Fatalf("next_page_token = %v", got["next_page_token"])
	}
}

func TestFilterOptionsPayloadKeepsTheThreeCountStates(t *testing.T) {
	reason := "counting every student without a narrowing selection is unbounded"
	total := 9

	for _, tc := range []struct {
		name        string
		page        api.FilterOptionsPage
		wantCount   any
		wantSkipped bool
		wantReason  any
	}{
		{"produced", api.FilterOptionsPage{Count: &total}, float64(9), false, nil},
		{"refused", api.FilterOptionsPage{CountSkipped: true, CountSkippedReason: &reason}, nil, true, reason},
		{"never asked for", api.FilterOptionsPage{}, nil, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(FilterOptions(tc.page))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got["count"] != tc.wantCount || got["count_skipped"] != tc.wantSkipped || got["count_skipped_reason"] != tc.wantReason {
				t.Fatalf("%s = %s", tc.name, raw)
			}
		})
	}
}

func TestFilterOptionsPayloadWithoutACount(t *testing.T) {
	raw, err := json.Marshal(FilterOptions(api.FilterOptionsPage{}))
	if err != nil {
		t.Fatal(err)
	}
	// An absent count is null rather than zero, and no options is [] rather than null, so a
	// consumer never reads "we did not count" as "there are none" or has to guard a nil list.
	if string(raw) != `{"options":[],"next_page_token":null,"count":null,"count_skipped":false,"count_skipped_reason":null}` {
		t.Fatalf("payload = %s", raw)
	}
}
