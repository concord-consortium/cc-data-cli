package cmd

import (
	"context"
	"strconv"

	"github.com/concord-consortium/cc-data-cli/internal/fetch"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
	"github.com/spf13/cobra"
)

func newGetAnswersCmd() *cobra.Command {
	return newPagedGetCmd("answers", store.TypeAnswers, "Download a run's student answers into a dataset")
}

func newPagedGetCmd(use, typ, short string) *cobra.Command {
	var datasetRef string
	var refresh bool
	cmd := &cobra.Command{
		Use:   use + " <run-id> --dataset <ref>",
		Short: short,
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
			result, err := fetch.FetchPaged(context.Background(), fetch.PagedOptions{
				DS:       d,
				Client:   client,
				RunID:    runID,
				Type:     typ,
				Refresh:  refresh,
				Progress: output.Stderr(),
			})
			emitResult(result)
			return err
		},
	}
	cmd.Flags().StringVar(&datasetRef, "dataset", "", "dataset ref <portal>/<name>")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "delete the segment and re-fetch from the start")
	return cmd
}
