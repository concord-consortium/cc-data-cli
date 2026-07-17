package cmd

import (
	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Download report CSVs, answers, history, and attachments into a dataset",
	}
	cmd.AddCommand(newGetReportCmd(), newGetAnswersCmd(), newGetHistoryCmd())
	return cmd
}

// openDatasetForFetch resolves a dataset ref for a fetch, echoing it and building
// the portal's API client.
func openDatasetForFetch(datasetRef string) (*dataset.Dataset, *api.Client, error) {
	cfg, root, err := loadRuntime()
	if err != nil {
		return nil, nil, err
	}
	ref, err := resolveRef(cfg, datasetRef)
	if err != nil {
		return nil, nil, err
	}
	echoRef(ref)
	d := dataset.Open(root, ref)
	if !d.Exists() {
		return nil, nil, notFound(ref)
	}
	client, err := api.ForPortal(ref.Portal)
	if err != nil {
		return nil, nil, err
	}
	return d, client, nil
}

// emitResult writes a fetch's result-line value to stdout when present.
func emitResult(result any) {
	if result != nil {
		output.ResultLine(result)
	}
}
