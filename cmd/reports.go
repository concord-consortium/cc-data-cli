package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/reportview"
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
				return output.JSONLine(reportview.Runs(runs))
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
				return output.JSONLine(reportview.JobsPayload{Jobs: jobs})
			}
			renderJobsTable(jobs)
			return nil
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", "portal the run belongs to")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func renderRunsTable(runs []api.ReportRun) {
	tw := tabwriter.NewWriter(output.Stdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tSLUG\tSTATE\tFILTERS")
	for _, r := range runs {
		labels := strings.Join(reportview.FilterLabels(r), ", ")
		if labels == "" {
			labels = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", r.ID, r.ReportSlug, reportview.StateText(r.AthenaQueryState), labels)
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
