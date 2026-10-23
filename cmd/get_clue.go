package cmd

import (
	"github.com/concord-consortium/cc-data-cli/internal/store"
	"github.com/spf13/cobra"
)

func newGetClueDocumentsCmd() *cobra.Command {
	return newPagedGetCmd("clue-documents", store.TypeClueDocuments, "Download a run's CLUE documents into a dataset",
		"Download the CLUE documents of a run's students, groups and classes: one row per\n"+
			"document with its owner, unit, problem, offering and raw metadata. A Student ID\n"+
			"Mapping run is the intended source. Fetch it before clue-history, which needs the\n"+
			"run's documents to count owners.")
}

func newGetClueHistoryCmd() *cobra.Command {
	return newPagedGetCmd("clue-history", store.TypeClueHistory, "Download a run's CLUE document edit history into a dataset",
		"Download the edit history of a run's CLUE documents: one row per history entry,\n"+
			"the stored entry kept as text. This is CLUE document history; for interactive\n"+
			"state history, use cc-data get history. Fetch clue-documents for the run first,\n"+
			"or the result cannot count owners. A Student ID Mapping run is the intended source.")
}

func newGetClueContentCmd() *cobra.Command {
	return newPagedGetCmd("clue-content", store.TypeClueContent, "Download a run's CLUE document content into a dataset",
		"Download the current content of a run's CLUE documents: one row per document, the\n"+
			"stored content kept as text. A Student ID Mapping run is the intended source.")
}
