package cmd

import (
	"context"
	"strconv"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/fetch"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newGetReportCmd() *cobra.Command {
	var datasetRef string
	var jobID int
	var noWait, refresh bool
	var pollTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "report <run-id> --dataset <ref>",
		Short: "Download a report CSV into a dataset",
		Long: `Download a report CSV into a dataset.

How the CSV is produced depends on the run, which "reports list" shows as its
EXECUTION. Either way the file is written atomically, never partially.

An async run is an Athena report. It is polled until its query succeeds and then
streamed from a presigned URL. --no-wait reports the current state and exits 4
without waiting; --poll-timeout bounds the wait (default 30m); --job downloads a
post-processing job's CSV instead. A terminal failure (failed/cancelled) exits 5.

A sync run is a Portal report, computed live and streamed when it is asked for.
It has no query to poll, so --no-wait and --poll-timeout do nothing, and no
post-processing jobs, so --job is refused. The download is bounded by the
server's own budget; a run with no filters is refused by the server.

An existing CSV requires --refresh to re-download. For a Portal report that is
the normal way to get current data, since it is recomputed on every request.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := strconv.Atoi(args[0])
			if err != nil {
				return output.Usagef("run-id must be an integer")
			}
			if datasetRef == "" {
				return output.Usagef("--dataset is required")
			}
			d, client, err := openDatasetForFetch(datasetRef)
			if err != nil {
				return err
			}
			opts := fetch.ReportOptions{
				DS:          d,
				Client:      client,
				RunID:       runID,
				NoWait:      noWait,
				Refresh:     refresh,
				PollTimeout: pollTimeout,
				Progress:    output.Stderr(),
			}
			if cmd.Flags().Changed("job") {
				opts.JobID = &jobID
			}
			result, err := fetch.FetchReport(context.Background(), opts)
			emitResult(result)
			return err
		},
	}
	cmd.Flags().StringVar(&datasetRef, "dataset", "", "dataset ref <portal>/<name>")
	cmd.Flags().IntVar(&jobID, "job", 0, "download a post-processing job's CSV instead of the run's (Athena runs only)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "do not poll; report the current state and exit (no effect on a Portal run)")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "re-download even if the CSV already exists")
	cmd.Flags().DurationVar(&pollTimeout, "poll-timeout", 0, "overall polling budget (default 30m; no effect on a Portal run)")
	return cmd
}
