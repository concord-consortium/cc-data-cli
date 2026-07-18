package cmd

import (
	"fmt"

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
	cmd.AddCommand(newGetReportCmd(), newGetAnswersCmd(), newGetHistoryCmd(), newGetAttachmentsCmd())
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

// emitResult writes a fetch's result-line value to stdout when present. A write
// failure (e.g. a broken pipe to a downstream consumer) is reported to stderr so
// the dropped machine output is never silent.
func emitResult(result any) {
	if result == nil {
		return
	}
	if err := output.ResultLine(result); err != nil {
		fmt.Fprintf(output.Stderr(), "warning: could not write result line: %v\n", err)
	}
}
