package cmd

import (
	"github.com/concord-consortium/cc-data-cli/internal/store"
	"github.com/spf13/cobra"
)

func newGetHistoryCmd() *cobra.Command {
	return newPagedGetCmd("history", store.TypeHistory, "Download a run's interactive state history into a dataset")
}
