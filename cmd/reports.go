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
			client, err := reportsClient(portal)
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
			client, err := reportsClient(portal)
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
	// Whether each flag was given, which is the only way to tell `--report-filter ""` from an
	// omitted flag. The two must not be treated alike. Someone writing `--report-filter "$FILTER"`
	// with an empty variable believes they filtered, so silently creating a run over everything
	// they can see would be wrong. raw() refuses it.
	inlineSet bool
	fileSet   bool
}

func (f *reportFilterFlags) bind(cmd *cobra.Command) {
	f.inlineSet = cmd.Flags().Changed("report-filter")
	f.fileSet = cmd.Flags().Changed("report-filter-file")
}

func (f *reportFilterFlags) register(cmd *cobra.Command, what string) {
	cmd.Flags().StringVar(&f.inline, "report-filter", "", what+`, as a JSON object, e.g. '{"cohort":[1,2]}'`)
	cmd.Flags().StringVar(&f.file, "report-filter-file", "", "read the JSON filter from a file instead of the command line")
}

// raw returns the filter to send, or nil when none was given.
func (f reportFilterFlags) raw() (json.RawMessage, error) {
	if f.inline != "" && f.file != "" {
		return nil, output.Usagef("--report-filter and --report-filter-file are mutually exclusive")
	}
	if f.inlineSet && strings.TrimSpace(f.inline) == "" {
		return nil, output.Usagef("--report-filter is empty; pass a JSON object, or omit the flag entirely")
	}
	if f.fileSet && f.file == "" {
		return nil, output.Usagef("--report-filter-file is empty; pass a path, or omit the flag entirely")
	}
	if f.file != "" {
		data, err := os.ReadFile(f.file)
		if err != nil {
			return nil, output.Usagef("unable to read --report-filter-file: %v", err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, output.Usagef("--report-filter-file %s holds no filter", f.file)
		}
		return decodeReportFilter(string(data), fmt.Sprintf("--report-filter-file %s", f.file))
	}
	if strings.TrimSpace(f.inline) == "" {
		return nil, nil
	}
	return decodeReportFilter(f.inline, "--report-filter")
}

// The filter is validated as JSON locally so a typo is a usage error rather than a server round
// trip, and is otherwise passed through untouched: the server owns what a valid filter is, and a
// client-side schema could only disagree with it.
// source names where the text came from, so a malformed file blames the file and its path rather
// than a flag the caller never passed.
func decodeReportFilter(text, source string) (json.RawMessage, error) {
	var probe map[string]any
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return nil, output.Usagef("%s must be a JSON object: %v", source, err)
	}
	if probe == nil {
		return nil, output.Usagef("%s must be a JSON object, not null", source)
	}
	return json.RawMessage(text), nil
}

func renderRun(run api.ReportRun, asJSON bool) error {
	if asJSON {
		return output.JSONLine(reportview.RunPayload{Run: reportview.ToRunJSON(run)})
	}
	renderRunsTable([]api.ReportRun{run})
	return nil
}

// reportCreateFlags is the reports create flag set. The flags-to-request step and the call live on
// it so both can be exercised without a stored credential; reached only through RunE, neither can.
type reportCreateFlags struct {
	portal, slug string
	filter       reportFilterFlags
	asJSON       bool
}

func (f reportCreateFlags) request() (api.CreateReportReq, error) {
	if f.slug == "" {
		return api.CreateReportReq{}, output.Usagef("--report-slug is required")
	}
	raw, err := f.filter.raw()
	if err != nil {
		return api.CreateReportReq{}, err
	}
	return api.CreateReportReq{ReportSlug: f.slug, ReportFilter: raw}, nil
}

func (f reportCreateFlags) run(ctx context.Context, client *api.Client, req api.CreateReportReq) error {
	run, err := client.CreateReport(ctx, req)
	if err != nil {
		return api.AsWriteCLIError(err, api.RunMayExistAction(f.portal))
	}
	return renderRun(run, f.asJSON)
}

type reportDuplicateFlags struct {
	portal        string
	force, asJSON bool
}

func (f reportDuplicateFlags) run(ctx context.Context, client *api.Client, runID int) error {
	run, err := client.DuplicateReport(ctx, runID, f.force)
	if err != nil {
		return api.AsWriteCLIError(err, api.RunMayExistAction(f.portal))
	}
	return renderRun(run, f.asJSON)
}

func newReportsCreateCmd() *cobra.Command {
	var f reportCreateFlags

	cmd := &cobra.Command{
		Use:   "create --report-slug <slug> --portal <portal|env>",
		Short: "Create a report run from a filter",
		Long: "Create a report run from a filter, without the web form.\n\n" +
			"The filter is the JSON object the API emits on a run, so a filter assembled with " +
			"reports filter-options can be passed straight through. The server derives the run's " +
			"filter labels and forces hide_names by role, and refuses an id the user cannot see.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.filter.bind(cmd)
			// Built once, before the credential lookup, so a usage error needs no stored token and
			// --report-filter-file is read exactly once rather than re-read for the request.
			req, err := f.request()
			if err != nil {
				return err
			}
			client, err := reportsClient(f.portal)
			if err != nil {
				return err
			}
			return f.run(context.Background(), client, req)
		},
	}
	cmd.Flags().StringVar(&f.portal, "portal", "", "portal to create the run on: an environment alias or a hostname")
	cmd.Flags().StringVar(&f.slug, "report-slug", "", "the report to run")
	cmd.Flags().BoolVar(&f.asJSON, "json", false, "emit JSON instead of a table")
	f.filter.register(cmd, "the run filter")
	return cmd
}

func newReportsDuplicateCmd() *cobra.Command {
	var f reportDuplicateFlags

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
			client, err := reportsClient(f.portal)
			if err != nil {
				return err
			}
			return f.run(context.Background(), client, runID)
		},
	}
	cmd.Flags().StringVar(&f.portal, "portal", "", "portal the run belongs to: an environment alias or a hostname")
	cmd.Flags().BoolVar(&f.force, "force", false, "duplicate a Portal run anyway, rather than re-reading it")
	cmd.Flags().BoolVar(&f.asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// reportsClient resolves the flag or the configured default to the portal's stored credential.
func reportsClient(portal string) (*api.Client, error) {
	cfg, _, err := loadRuntime()
	if err != nil {
		return nil, err
	}
	host, err := resolvePortal(cfg, portal)
	if err != nil {
		return nil, err
	}
	return api.ForPortal(host)
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
			f.filter.bind(cmd)
			if _, err := f.request(); err != nil {
				return err
			}
			client, err := reportsClient(f.portal)
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
	f.filter.register(cmd, "the selections already made, to narrow the options by")
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
	fmt.Fprintln(tw, "RUN\tSLUG\tEXECUTION\tSTATE\tFILTERS")
	for _, r := range runs {
		labels := strings.Join(reportview.FilterLabels(r), ", ")
		if labels == "" {
			labels = "-"
		}
		// A server too old to report an execution leaves the cell empty, which reads as a
		// rendering fault rather than as an unanswered field.
		execution := r.Execution
		if execution == "" {
			execution = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", r.ID, r.ReportSlug, execution, reportview.StateText(r), labels)
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
