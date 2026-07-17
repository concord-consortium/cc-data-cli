package reportview

import (
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
