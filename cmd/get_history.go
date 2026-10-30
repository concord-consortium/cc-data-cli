package cmd

import (
	"github.com/concord-consortium/cc-data-cli/internal/store"
	"github.com/spf13/cobra"
)

func newGetHistoryCmd() *cobra.Command {
	return newPagedGetCmd("history", store.TypeHistory, "Download a run's interactive state history into a dataset",
		"Download a run's interactive state history: every saved snapshot of each answer's\n"+
			"interactive state, from report-service. For a CLUE document's edit history, use\n"+
			"cc-data get clue-history.")
}
