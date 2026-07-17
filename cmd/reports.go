package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newReportsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reports",
		Short: "List report runs and jobs",
	}
	cmd.AddCommand(newReportsListCmd(), newReportsJobsCmd())
	return cmd
}

// resolvePortal normalizes the --portal flag or the configured default.
func resolvePortal(cfg *config.Config, flagVal string) (string, error) {
	raw := flagVal
	if raw == "" {
		raw = cfg.DefaultPortal
	}
	if raw == "" {
		return "", output.Usagef("--portal is required (no default_portal configured)")
	}
	host, err := config.NormalizePortal(raw)
	if err != nil {
		return "", output.Usagef("%v", err)
	}
	return host, nil
}

type reportRunJSON struct {
	RunID        int      `json:"run_id"`
	Slug         string   `json:"slug"`
	State        string   `json:"state"`
	ReportType   string   `json:"report_type,omitempty"`
	FilterLabels []string `json:"filter_labels"`
}

func newReportsListCmd() *cobra.Command {
	var portal string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list --portal <portal>",
		Short: "List the user's report runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadRuntime()
			if err != nil {
				return err
			}
			host, err := resolvePortal(cfg, portal)
			if err != nil {
				return err
			}
			client, err := api.ForPortal(host)
			if err != nil {
				return err
			}
			runs, err := client.ListReports(context.Background())
			if err != nil {
				return api.AsCLIError(err)
			}
			if asJSON {
				rows := make([]reportRunJSON, 0, len(runs))
				for _, r := range runs {
					rows = append(rows, toReportRunJSON(r))
				}
				return output.JSONLine(map[string]any{"runs": rows})
			}
			renderRunsTable(runs)
			return nil
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", "portal to list runs for")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func newReportsJobsCmd() *cobra.Command {
	var portal string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "jobs <run-id> --portal <portal>",
		Short: "List a run's post-processing jobs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.Atoi(args[0])
			if err != nil {
				return output.Usagef("run-id must be an integer")
			}
			cfg, _, err := loadRuntime()
			if err != nil {
				return err
			}
			host, err := resolvePortal(cfg, portal)
			if err != nil {
				return err
			}
			client, err := api.ForPortal(host)
			if err != nil {
				return err
			}
			jobs, err := client.ListJobs(context.Background(), runID)
			if err != nil {
				return api.AsCLIError(err)
			}
			if asJSON {
				return output.JSONLine(map[string]any{"jobs": jobs})
			}
			renderJobsTable(jobs)
			return nil
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", "portal the run belongs to")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func toReportRunJSON(r api.ReportRun) reportRunJSON {
	rt := ""
	if r.ReportType != nil {
		rt = *r.ReportType
	}
	return reportRunJSON{
		RunID:        r.ID,
		Slug:         r.ReportSlug,
		State:        stateText(r.AthenaQueryState),
		ReportType:   rt,
		FilterLabels: filterLabels(r),
	}
}

func renderRunsTable(runs []api.ReportRun) {
	tw := tabwriter.NewWriter(output.Stdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tSLUG\tSTATE\tFILTERS")
	for _, r := range runs {
		labels := strings.Join(filterLabels(r), ", ")
		if labels == "" {
			labels = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", r.ID, r.ReportSlug, stateText(r.AthenaQueryState), labels)
	}
	tw.Flush()
}

func renderJobsTable(jobs []api.Job) {
	tw := tabwriter.NewWriter(output.Stdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB\tSTATUS\tHAS RESULT")
	for _, j := range jobs {
		fmt.Fprintf(tw, "%d\t%s\t%t\n", j.ID, j.Status, j.HasResult)
	}
	tw.Flush()
}

func stateText(s *string) string {
	if s == nil || *s == "" {
		return "(none)"
	}
	return *s
}

// filterLabels renders the run's resolved filter labels. A filter-less run
// (programmatic run with a NULL filter normalized server-side to the empty
// object) simply yields no labels.
func filterLabels(r api.ReportRun) []string {
	var labels []string
	keys := make([]string, 0, len(r.ReportFilterValues))
	for k := range r.ReportFilterValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := r.ReportFilterValues[k]
		if s := labelValue(v); s != "" {
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
	case []any:
		var parts []string
		for _, e := range t {
			if s := labelValue(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "/")
	default:
		return fmt.Sprintf("%v", t)
	}
}
