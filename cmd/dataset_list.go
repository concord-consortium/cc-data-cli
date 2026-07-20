package cmd

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newDatasetListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List datasets across all portals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, root, err := loadRuntime()
			if err != nil {
				return err
			}
			list, err := dataset.BuildListJSON(root)
			if err != nil {
				return output.Internalf("%v", err)
			}
			if asJSON {
				return output.JSONLine(list)
			}
			renderList(list)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func renderList(list *dataset.ListJSON) {
	out := output.Stdout()
	if len(list.Datasets) == 0 {
		fmt.Fprintln(out, "no datasets")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "REF\tDESCRIPTION\tAGE\tANSWERS\tHISTORY\tREPORT\tATTACH\tSIZE")
	for _, d := range list.Datasets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\n",
			d.Ref, d.Description, humanAge(d.AgeSeconds),
			d.Totals["answers"], d.Totals["history"], d.Totals["report"], d.Totals["attachments"], humanBytes(d.SizeBytes))
	}
	tw.Flush()
}

func humanAge(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
