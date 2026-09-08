// Package reportview holds the CLI-side JSON shaping for report listings, shared
// by the CLI commands and the MCP tools so their payloads never drift.
package reportview

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/api"
)

// RunJSON is the machine form of a report run.
type RunJSON struct {
	RunID        int      `json:"run_id"`
	Slug         string   `json:"slug"`
	State        string   `json:"state"`
	ReportType   string   `json:"report_type,omitempty"`
	FilterLabels []string `json:"filter_labels"`
	// The run's filter as the server emitted it, which reports_filter_options takes back to
	// narrow a dimension by what this run already selected. FilterLabels renders the same
	// selections for a human and cannot be sent back. Decoded rather than held as
	// json.RawMessage, which reflects to a byte array in the MCP output schema and fails
	// validation for every run that has a filter.
	ReportFilter map[string]any `json:"report_filter,omitempty"`
}

// RunsPayload is the reports_list payload.
type RunsPayload struct {
	Runs []RunJSON `json:"runs"`
}

// JobsPayload is the reports_jobs payload.
type JobsPayload struct {
	Jobs []api.Job `json:"jobs"`
}

// Runs shapes a list of report runs.
func Runs(runs []api.ReportRun) RunsPayload {
	out := RunsPayload{Runs: make([]RunJSON, 0, len(runs))}
	for _, r := range runs {
		out.Runs = append(out.Runs, ToRunJSON(r))
	}
	return out
}

// ToRunJSON shapes one report run.
func ToRunJSON(r api.ReportRun) RunJSON {
	rt := ""
	if r.ReportType != nil {
		rt = *r.ReportType
	}
	var filter map[string]any
	if len(r.ReportFilter) > 0 {
		// A filter the server sends as anything but an object is left out rather than
		// reshaped; every documented filter is an object.
		_ = json.Unmarshal(r.ReportFilter, &filter)
	}
	return RunJSON{
		RunID:        r.ID,
		Slug:         r.ReportSlug,
		State:        StateText(r.AthenaQueryState),
		ReportType:   rt,
		FilterLabels: FilterLabels(r),
		ReportFilter: filter,
	}
}

// StateText renders a nullable athena_query_state.
func StateText(s *string) string {
	if s == nil || *s == "" {
		return "(none)"
	}
	return *s
}

// FilterLabels renders a run's resolved filter labels.
func FilterLabels(r api.ReportRun) []string {
	var labels []string
	keys := make([]string, 0, len(r.ReportFilterValues))
	for k := range r.ReportFilterValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s := labelValue(r.ReportFilterValues[k]); s != "" {
			labels = append(labels, fmt.Sprintf("%s: %s", k, s))
		}
	}
	return labels
}

func labelValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return fmt.Sprintf("%v", t)
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", t), ".0")
	case []any:
		return joinLabels(t)
	case map[string]any:
		// A resolved id->label map (e.g. assignment id -> title); render the
		// labels, not the raw map, in a stable order.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make([]any, 0, len(keys))
		for _, k := range keys {
			vals = append(vals, t[k])
		}
		return joinLabels(vals)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func joinLabels(items []any) string {
	var parts []string
	for _, e := range items {
		if s := labelValue(e); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "/")
}

// FilterOptionsPayload is the reports_filter_options payload.
//
// It carries the whole server envelope rather than just the options: next_page_token is how a
// caller that paged continues, and count_skipped is what separates a count the server refused from
// one that was never asked for, which a nil Count alone cannot express.
type FilterOptionsPayload struct {
	Options            []api.FilterOption `json:"options"`
	NextPageToken      *string            `json:"next_page_token"`
	Count              *int               `json:"count"`
	CountSkipped       bool               `json:"count_skipped"`
	CountSkippedReason *string            `json:"count_skipped_reason"`
	// Truncated says the walk stopped at its cap; next_page_token then continues it.
	Truncated bool `json:"truncated,omitempty"`
}

// FilterOptions shapes one page of filter options for the CLI and the MCP tool.
func FilterOptions(page api.FilterOptionsPage) FilterOptionsPayload {
	options := page.Items
	if options == nil {
		options = []api.FilterOption{}
	}
	return FilterOptionsPayload{
		Options:            options,
		NextPageToken:      page.NextPageToken,
		Count:              page.Count,
		CountSkipped:       page.CountSkipped,
		CountSkippedReason: page.CountSkippedReason,
		Truncated:          page.Truncated,
	}
}
