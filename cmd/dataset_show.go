package cmd

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newDatasetShowCmd() *cobra.Command {
	var asJSON, full bool
	cmd := &cobra.Command{
		Use:   "show <ref>",
		Short: "Show a dataset's holdings and warnings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, root, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveRef(cfg, args[0])
			if err != nil {
				return err
			}
			d := dataset.Open(root, ref)
			if !d.Exists() {
				return notFound(ref)
			}
			summary, err := d.BuildShowJSON(full)
			if err != nil {
				return output.Internalf("%v", err)
			}
			if asJSON {
				return output.JSONLine(summary)
			}
			renderShow(summary, full)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	cmd.Flags().BoolVar(&full, "full", false, "include per-file detail")
	return cmd
}

func renderShow(s *dataset.ShowJSON, full bool) {
	out := output.Stdout()
	fmt.Fprintf(out, "%s\n", s.Ref)
	if s.Description != "" {
		fmt.Fprintf(out, "%s\n", s.Description)
	}
	fmt.Fprintf(out, "created %s, %s on disk\n\n", s.CreatedAt.Format("2006-01-02"), humanBytes(s.SizeBytes))

	fmt.Fprintln(out, "Totals:")
	types := make([]string, 0, len(s.Totals))
	for t := range s.Totals {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(out, "  %-12s %d\n", t, s.Totals[t])
	}

	if len(s.Downloads) > 0 {
		fmt.Fprintln(out, "\nDownloads:")
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "RUN\tTYPE\tSLUG\tFETCHED\tSTATUS\tCOVERAGE")
		for _, dl := range s.Downloads {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
				dl.RunID, dl.Type, dl.Slug, dl.FetchedAt.Format("2006-01-02"), downloadStatus(dl), coverageText(dl.Coverage))
		}
		tw.Flush()
	}

	if len(s.Warnings) > 0 {
		errOut := output.Stderr()
		fmt.Fprintln(errOut, "\nWarnings:")
		for _, w := range s.Warnings {
			fmt.Fprintf(errOut, "  %s\n", w)
		}
	}
}

func downloadStatus(dl dataset.DownloadJSON) string {
	if !dl.Complete {
		return "incomplete"
	}
	if dl.Recovered {
		return "recovered"
	}
	return "complete"
}

func coverageText(cov *dataset.Coverage) string {
	if cov == nil {
		return "-"
	}
	if cov.Queried == nil {
		return "coverage unknown"
	}
	return fmt.Sprintf("%d queried, %d with data", *cov.Queried, cov.WithData)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
