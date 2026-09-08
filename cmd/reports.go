package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/auth"
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
	cmd.AddCommand(newReportsListCmd(), newReportsJobsCmd(), newReportsFilterOptionsCmd())
	return cmd
}

// resolvePortal resolves the --portal flag or the configured default to a
// portal host. These commands have no --server flag (the server comes from the
// stored credential), so only the portal side of an environment alias applies.
// A portal only a stored credential vouches for is reachable here for the same
// reason it is at logout: its runs would otherwise be impossible to list.
func resolvePortal(cfg *config.Config, flagVal string) (config.Portal, error) {
	raw := flagVal
	if raw == "" {
		raw = cfg.DefaultPortal
	}
	if raw == "" {
		return config.Portal{}, output.Usagef("--portal is required (no default_portal configured)")
	}
	host, _, err := auth.ResolvePortalTarget(raw)
	if err != nil {
		return config.Portal{}, output.Usagef("%v", err)
	}
	return host, nil
}

func newReportsListCmd() *cobra.Command {
	var portal string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list --portal <portal|env>",
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
	cmd.Flags().StringVar(&portal, "portal", "", "portal to list runs for: an environment alias or a hostname")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func newReportsJobsCmd() *cobra.Command {
	var portal string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "jobs <run-id> --portal <portal|env>",
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
	cmd.Flags().StringVar(&portal, "portal", "", "portal the run belongs to: an environment alias or a hostname")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func newReportsFilterOptionsCmd() *cobra.Command {
	var portal, dimension, slug, search string
	var limit int
	var all, asJSON bool

	cmd := &cobra.Command{
		Use:   "filter-options --dimension <dimension> --portal <portal|env>",
		Short: "Browse the values a report filter dimension offers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dimension == "" {
				return output.Usagef("--dimension is required")
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

			req := api.FilterOptionsReq{
				Dimension:  dimension,
				ReportSlug: slug,
				Search:     search,
				Limit:      limit,
			}

			options, count, err := client.FilterOptionsFor(context.Background(), req, all)
			if err != nil {
				return api.AsCLIError(err)
			}
			if asJSON {
				return output.JSONLine(reportview.FilterOptions(options, count))
			}
			renderFilterOptionsTable(options, count)
			return nil
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", "portal to browse: an environment alias or a hostname")
	cmd.Flags().StringVar(&dimension, "dimension", "", "the dimension to list options for")
	cmd.Flags().StringVar(&slug, "report-slug", "", "restrict to a report that offers the dimension")
	cmd.Flags().StringVar(&search, "search", "", "narrow the options by a substring of the label")
	cmd.Flags().IntVar(&limit, "limit", 0, "options per page (server default when unset)")
	cmd.Flags().BoolVar(&all, "all", false, "walk every page instead of returning the first")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func renderFilterOptionsTable(options []api.FilterOption, count *int) {
	tw := tabwriter.NewWriter(output.Stdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL")
	for _, o := range options {
		fmt.Fprintf(tw, "%s\t%s\n", o.ID, o.Label)
	}
	tw.Flush()
	if count != nil {
		fmt.Fprintf(output.Stdout(), "\n%d shown of %d total\n", len(options), *count)
	}
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
