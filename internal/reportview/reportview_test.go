package reportview

import (
	"encoding/json"
	"reflect"
	"strings"
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
	running := "running"
	for _, tc := range []struct {
		name string
		run  api.ReportRun
		want string
	}{
		{"an async run reports its state", api.ReportRun{Execution: api.ExecutionAsync, AthenaQueryState: &running}, "running"},
		{"an async run with no state has not started", api.ReportRun{Execution: api.ExecutionAsync}, "(none)"},
		{"a sync run is live", api.ReportRun{Execution: api.ExecutionSync}, "live"},
		// The one that fails if the branch is written as "a null state means live".
		{"a sync run is live whatever its state field holds", api.ReportRun{Execution: api.ExecutionSync, AthenaQueryState: &running}, "live"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StateText(tc.run); got != tc.want {
				t.Fatalf("StateText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToRunJSONCarriesExecution(t *testing.T) {
	j := ToRunJSON(api.ReportRun{ID: 584, ReportSlug: "student-id-mapping", Execution: api.ExecutionSync})
	if j.Execution != api.ExecutionSync {
		t.Errorf("execution = %q, want %q", j.Execution, api.ExecutionSync)
	}
	if j.State != "live" {
		t.Errorf("state = %q, want live", j.State)
	}
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"execution":"sync"`) {
		t.Errorf("the MCP payload must carry execution: %s", raw)
	}
}

// The hand-copy into the payload is deliberate, so each field it carries needs an assertion:
// truncated is what tells a caller the list is partial.
func TestFilterOptionsPayloadCarriesTruncated(t *testing.T) {
	token := "next"
	got := FilterOptions(api.FilterOptionsPage{
		Items:         []api.FilterOption{{ID: "1", Label: "Ada"}},
		NextPageToken: &token,
		Truncated:     true,
	})
	if !got.Truncated {
		t.Error("truncated did not survive the copy into the payload")
	}
	if got.NextPageToken == nil || *got.NextPageToken != token {
		t.Errorf("next_page_token did not survive: %v", got.NextPageToken)
	}
	if plain := FilterOptions(api.FilterOptionsPage{}); plain.Truncated {
		t.Error("a complete walk must not report truncation")
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

func TestToRunJSONCarriesTheFailureFields(t *testing.T) {
	state, id := "failed", "qid-failed"
	reason := "HIVE_EXCEEDED_PARTITION_LIMIT: too many"
	guidance := "This query covers too many Athena partitions."

	j := ToRunJSON(api.ReportRun{
		ID: 5, ReportSlug: "student-actions", Execution: api.ExecutionAsync,
		AthenaQueryState: &state, AthenaQueryID: &id, AthenaQueryError: &reason, AthenaQueryGuidance: &guidance,
	})

	if j.AthenaQueryID == nil || *j.AthenaQueryID != id {
		t.Errorf("query id = %v", j.AthenaQueryID)
	}
	if j.AthenaQueryError == nil || *j.AthenaQueryError != reason {
		t.Errorf("reason = %v", j.AthenaQueryError)
	}
	if j.AthenaQueryGuidance == nil || *j.AthenaQueryGuidance != guidance {
		t.Errorf("guidance = %v", j.AthenaQueryGuidance)
	}
}

func TestToRunJSONOmitsTheFailureFieldsWhenTheServerSendsNone(t *testing.T) {
	state := "queued"
	raw, err := json.Marshal(RunPayload{Run: ToRunJSON(api.ReportRun{
		ID: 90070, ReportSlug: "student-answers", Execution: api.ExecutionAsync, AthenaQueryState: &state,
	})})
	if err != nil {
		t.Fatal(err)
	}

	want := `{"run":{"run_id":90070,"slug":"student-answers","state":"queued","execution":"async","filter_labels":null}}`
	if string(raw) != want {
		t.Errorf("a created run's payload changed:\n got %s\nwant %s", raw, want)
	}
}

func TestToRunJSONPortalRunCarriesNoFailureFields(t *testing.T) {
	raw, err := json.Marshal(ToRunJSON(api.ReportRun{ID: 229, ReportSlug: "school-metrics", Execution: api.ExecutionSync}))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"athena_query_id", "athena_query_error", "athena_query_guidance"} {
		if strings.Contains(string(raw), key) {
			t.Errorf("a Portal run has no Athena query, so %q must not appear: %s", key, raw)
		}
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
