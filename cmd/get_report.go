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
		Args:  cobra.ExactArgs(1),
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
	cmd.Flags().IntVar(&jobID, "job", 0, "download a post-processing job's CSV instead of the run's")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "do not poll; report the current state and exit")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "re-download even if the CSV already exists")
	cmd.Flags().DurationVar(&pollTimeout, "poll-timeout", 0, "overall polling budget (default 30m)")
	return cmd
}
