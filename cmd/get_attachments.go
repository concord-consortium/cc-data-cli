package cmd

import (
	"context"
	"strconv"

	"github.com/concord-consortium/cc-data-cli/internal/fetch"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newGetAttachmentsCmd() *cobra.Command {
	var datasetRef string
	var refresh, url, inline bool
	var answer, history, question, name string
	cmd := &cobra.Command{
		Use:     "attachments <run-id> --dataset <ref>",
		Aliases: []string{"attachment"},
		Short:   "Download a run's file attachments into a dataset",
		Args:    cobra.ExactArgs(1),
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
			result, err := fetch.FetchAttachments(context.Background(), fetch.AttachmentOptions{
				DS:       d,
				Client:   client,
				RunID:    runID,
				Refresh:  refresh,
				URL:      url,
				Inline:   inline,
				Answer:   answer,
				History:  history,
				Question: question,
				Name:     name,
				Progress: output.Stderr(),
			})
			emitResult(result)
			return err
		},
	}
	cmd.Flags().StringVar(&datasetRef, "dataset", "", "dataset ref <portal>/<name>")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "re-download all attachments")
	cmd.Flags().BoolVar(&url, "url", false, "print presigned URLs instead of downloading")
	cmd.Flags().BoolVar(&inline, "inline", false, "request the browser-renderable disposition")
	cmd.Flags().StringVar(&answer, "answer", "", "only attachments referenced by this answer id")
	cmd.Flags().StringVar(&history, "history", "", "only attachments referenced by this history id")
	cmd.Flags().StringVar(&question, "question", "", "only attachments for this question id")
	cmd.Flags().StringVar(&name, "name", "", "only attachments with this name")
	return cmd
}
