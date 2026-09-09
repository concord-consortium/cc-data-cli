package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		Short: "List, create and duplicate report runs",
	}
	cmd.AddCommand(newReportsListCmd(), newReportsJobsCmd(), newReportsFilterOptionsCmd(),
		newReportsCreateCmd(), newReportsDuplicateCmd())
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

// reportFilterFlags is the terminal expression of a report filter: the JSON object the API emits
// on every run, inline or from a file. Shared by reports create and reports filter-options so the
// two cannot disagree about what a filter is.
type reportFilterFlags struct {
	inline string
	file   string
}

func (f *reportFilterFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.inline, "report-filter", "", "the run filter as a JSON object, e.g. '{\"cohort\":[1,2]}'")
	cmd.Flags().StringVar(&f.file, "report-filter-file", "", "read the JSON filter from a file instead of the command line")
}

// raw returns the filter to send, or nil when none was given. It is validated as JSON locally so a
// typo is a usage error rather than a server round trip, and is otherwise passed through untouched:
// the server owns what a valid filter is, and a client-side schema could only disagree with it.
func (f reportFilterFlags) raw() (json.RawMessage, error) {
	if f.inline != "" && f.file != "" {
		return nil, output.Usagef("--report-filter and --report-filter-file are mutually exclusive")
	}
	text := f.inline
	if f.file != "" {
		data, err := os.ReadFile(f.file)
		if err != nil {
			return nil, output.Usagef("unable to read --report-filter-file: %v", err)
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return nil, output.Usagef("--report-filter must be a JSON object: %v", err)
	}
	return json.RawMessage(text), nil
}

// A coded refusal is the server's answer, so nothing was created. Anything else may still have
// reached it, and the client refuses to retry a non-idempotent request for exactly that reason, so
// looking is the right advice and retrying is not.
func reportWriteError(err error) error {
	cliErr := api.AsCLIError(err)
	if cliErr.ExitCode == output.ExitContract || cliErr.ExitCode == output.ExitNotAuth {
		return cliErr
	}
	cliErr.Action = "The run may still have been created; check with: cc-data reports list"
	return cliErr
}

func renderRun(run api.ReportRun, asJSON bool) error {
	if asJSON {
		return output.JSONLine(reportview.RunPayload{Run: reportview.ToRunJSON(run)})
	}
	renderRunsTable([]api.ReportRun{run})
	return nil
}

func newReportsCreateCmd() *cobra.Command {
	var portal, slug string
	var filter reportFilterFlags
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "create --report-slug <slug> --portal <portal|env>",
		Short: "Create a report run from a filter",
		Long: "Create a report run from a filter, without the web form.\n\n" +
			"The filter is the JSON object the API emits on a run, so a filter assembled with " +
			"reports filter-options can be passed straight through. The server derives the run's " +
			"filter labels and forces hide_names by role, and refuses an id the user cannot see.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if slug == "" {
				return output.Usagef("--report-slug is required")
			}
			raw, err := filter.raw()
			if err != nil {
				return err
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
			run, err := client.CreateReport(context.Background(), api.CreateReportReq{ReportSlug: slug, ReportFilter: raw})
			if err != nil {
				return reportWriteError(err)
			}
			return renderRun(run, asJSON)
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", "portal to create the run on: an environment alias or a hostname")
	cmd.Flags().StringVar(&slug, "report-slug", "", "the report to run")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	filter.register(cmd)
	return cmd
}

func newReportsDuplicateCmd() *cobra.Command {
	var portal string
	var force, asJSON bool

	cmd := &cobra.Command{
		Use:   "duplicate <run-id> --portal <portal|env>",
		Short: "Take a fresh snapshot of an existing run",
		Long: "Create a new run from an existing run's report and filter.\n\n" +
			"A Portal report is computed live on every request, so re-reading the run with " +
			"get report returns current data and duplicating one is refused unless --force is " +
			"passed. Duplicating is how an Athena run is re-run, since a finished one is frozen.",
		Args: cobra.ExactArgs(1),
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
			run, err := client.DuplicateReport(context.Background(), runID, force)
			if err != nil {
				return reportWriteError(err)
			}
			return renderRun(run, asJSON)
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", "portal the run belongs to: an environment alias or a hostname")
	cmd.Flags().BoolVar(&force, "force", false, "duplicate a Portal run anyway, rather than re-reading it")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// filterOptionsFlags is the filter-options flag set. The flags-to-request step lives on it so
// it can be exercised without a server; reached only through RunE, every flag but --dimension
// was unverifiable.
type filterOptionsFlags struct {
	portal, dimension, slug, search, pageToken string
	limit                                      int
	all, asJSON                                bool
	filter                                     reportFilterFlags
}

func (f filterOptionsFlags) request() (api.FilterOptionsReq, error) {
	if f.dimension == "" {
		return api.FilterOptionsReq{}, output.Usagef("--dimension is required")
	}
	raw, err := f.filter.raw()
	if err != nil {
		return api.FilterOptionsReq{}, err
	}
	return api.FilterOptionsReq{
		Dimension:    f.dimension,
		ReportSlug:   f.slug,
		Search:       f.search,
		Limit:        f.limit,
		PageToken:    f.pageToken,
		ReportFilter: raw,
	}, nil
}

// fetch runs the request the flags describe, walking the pages when --all is set. The walk
// decision lives here rather than at the call site so a test can reach it.
func (f filterOptionsFlags) fetch(ctx context.Context, client *api.Client) (api.FilterOptionsPage, error) {
	req, err := f.request()
	if err != nil {
		return api.FilterOptionsPage{}, err
	}
	return client.FilterOptionsFor(ctx, req, f.all)
}

// render writes a page in the form the flags asked for.
func (f filterOptionsFlags) render(page api.FilterOptionsPage) error {
	if f.asJSON {
		return output.JSONLine(reportview.FilterOptions(page))
	}
	renderFilterOptionsTable(page)
	return nil
}

func newReportsFilterOptionsCmd() *cobra.Command {
	var f filterOptionsFlags

	cmd := &cobra.Command{
		Use:   "filter-options --dimension <dimension> --portal <portal|env>",
		Short: "Browse the values a report filter dimension offers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := f.request(); err != nil {
				return err
			}
			cfg, _, err := loadRuntime()
			if err != nil {
				return err
			}
			host, err := resolvePortal(cfg, f.portal)
			if err != nil {
				return err
			}
			client, err := api.ForPortal(host)
			if err != nil {
				return err
			}
			page, err := f.fetch(context.Background(), client)
			if err != nil {
				return api.AsCLIError(err)
			}
			return f.render(page)
		},
	}
	cmd.Flags().StringVar(&f.portal, "portal", "", "portal to browse: an environment alias or a hostname")
	cmd.Flags().StringVar(&f.dimension, "dimension", "", "the dimension to list options for")
	cmd.Flags().StringVar(&f.slug, "report-slug", "", "restrict to a report that offers the dimension")
	cmd.Flags().StringVar(&f.search, "search", "", "narrow the options by a substring of the label")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "options per page (server default when unset)")
	cmd.Flags().StringVar(&f.pageToken, "page-token", "", "continue from a token a previous run reported")
	cmd.Flags().BoolVar(&f.all, "all", false, fmt.Sprintf("walk the pages instead of returning the first, stopping after %d options", api.FilterOptionsDrainMax))
	cmd.Flags().BoolVar(&f.asJSON, "json", false, "emit JSON instead of a table")
	f.filter.register(cmd)
	return cmd
}

func renderFilterOptionsTable(page api.FilterOptionsPage) {
	tw := tabwriter.NewWriter(output.Stdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL")
	for _, o := range page.Items {
		fmt.Fprintf(tw, "%s\t%s\n", o.ID, o.Label)
	}
	tw.Flush()
	if page.Count != nil {
		fmt.Fprintf(output.Stdout(), "\n%d shown of %d total\n", len(page.Items), *page.Count)
	}
	if page.CountSkipped && page.CountSkippedReason == nil {
		fmt.Fprintf(output.Stdout(), "\nno total available\n")
	}
	if page.CountSkipped && page.CountSkippedReason != nil {
		fmt.Fprintf(output.Stdout(), "\n%d shown; no total: %s\n", len(page.Items), *page.CountSkippedReason)
	}
	if page.NextPageToken == nil || *page.NextPageToken == "" {
		return
	}
	if page.Truncated {
		// The walk already ran, so telling the user to pass --all would name what they just did.
		fmt.Fprintf(output.Stdout(), "stopped at the %d-option cap; continue with --page-token %s\n",
			api.FilterOptionsDrainMax, *page.NextPageToken)
		return
	}
	fmt.Fprintf(output.Stdout(), "more options remain; pass --all to walk them, or --page-token %s for the next page\n",
		*page.NextPageToken)
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
