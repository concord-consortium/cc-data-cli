package cmd

import (
	"strconv"

	"github.com/concord-consortium/cc-data-cli/internal/fetch"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

const getRecentLogsLong = `Pull a class's log events from the last day or two into the dataset's recent_logs view,
about two minutes behind live. The window starts at 00:00 UTC yesterday.

The first pull reads the whole window; each later pull sends the cursor the previous one
stored, and the server answers from ten minutes before that pull, so pulls overlap and
an event pulled again replaces its row. An event delivered later than that margin can be
missed by a cursor pull: --refresh ignores the cursor and reads the whole window again.
When the stored cursor has expired, the pull reads the whole window and warns.

--start and --end (epoch milliseconds or RFC 3339) read part of the window and leave the
stored cursor as it was. Days before the window come from a Student Actions run, whose
events land in logs, not recent_logs.`

func newGetRecentLogsCmd() *cobra.Command {
	var datasetRef, start, end string
	var refresh bool
	cmd := &cobra.Command{
		Use:   "recent-logs <class-id> --dataset <ref>",
		Short: "Pull a class's log events from the last day or two into a dataset",
		Long:  getRecentLogsLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			classID, err := strconv.Atoi(args[0])
			if err != nil {
				return output.Usagef("class-id must be an integer")
			}
			if datasetRef == "" {
				return output.Usagef("--dataset is required")
			}
			d, client, err := openDatasetForFetch(datasetRef)
			if err != nil {
				return err
			}
			result, err := fetch.FetchRecentLogs(cmd.Context(), fetch.RecentLogsOptions{
				DS:       d,
				Client:   client,
				ClassID:  classID,
				Start:    start,
				End:      end,
				Refresh:  refresh,
				Progress: output.Stderr(),
			})
			if err != nil {
				return err
			}
			emitResult(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetRef, "dataset", "", "dataset ref <portal>/<name>")
	cmd.Flags().StringVar(&start, "start", "", "read from this time (epoch milliseconds or RFC 3339)")
	cmd.Flags().StringVar(&end, "end", "", "read up to this time (epoch milliseconds or RFC 3339)")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ignore the stored cursor and read the whole window")
	return cmd
}
