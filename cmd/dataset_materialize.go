package cmd

import (
	"context"
	"sort"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newDatasetMaterializeCmd() *cobra.Command {
	var force, allowPartial bool
	cmd := &cobra.Command{
		Use:   "materialize <ref>",
		Short: "Write each of a dataset's views to a Parquet file queries can read",
		Long: "Write each of a dataset's file-backed views to " + dataset.MaterializedDir +
			"/<view>.parquet. Queries read a view's Parquet while it is fresh and fall back to the raw " +
			"artifacts otherwise, so materializing never changes an answer. The files are ordinary Parquet " +
			"and any tool can read them by path without going through cc-data.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, root, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveExistingRef(cfg, root, args[0])
			if err != nil {
				return err
			}
			echoRef(ref)
			d := dataset.Open(root, ref)
			if !d.Exists() {
				return notFound(ref)
			}
			opts := duck.MaterializeOptions{
				Force:        force,
				AllowPartial: allowPartial,
				Progress: func(view string, i, n int) {
					output.Progressf("[%d/%d] %s", i, n, view)
				},
			}
			res, err := duck.Materialize(context.Background(), d, opts, output.Stderr())
			if err != nil {
				return mutationErr(err)
			}
			return reportMaterialize(res)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "re-materialize a view whose inputs are unchanged")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "build a view even though a file it declares is missing")
	return cmd
}

// reportMaterialize renders the per-view outcome and returns a non-zero exit when
// any view was refused, so a set -euo pipefail recipe stops rather than consuming
// a surface that is missing a view.
func reportMaterialize(res duck.MaterializeResult) error {
	written, fresh := res.Written(), res.Fresh()
	discarded, refused := res.Discarded(), res.Refused()
	output.Progressf("written %d, already fresh %d, discarded %d, refused %d",
		len(written), len(fresh), len(discarded), len(refused))
	if len(written) > 0 {
		output.Progressf("  written: %s", strings.Join(written, ", "))
	}
	if len(discarded) > 0 {
		output.Progressf("  discarded, because a concurrent change moved their inputs; run again to pick them up: %s",
			strings.Join(discarded, ", "))
	}
	if !res.Refusals() {
		return nil
	}
	views := make([]string, 0, len(refused))
	for view := range refused {
		views = append(views, view)
	}
	sort.Strings(views)
	for _, view := range views {
		output.Progressf("view %s %s", view, refused[view])
	}
	// Exit 1, the table's "internal/other" class: a refusal is a local condition,
	// so the server-contract class would be a lie to anyone scripting on the code.
	return &output.CLIError{
		ExitCode: output.ExitInternal,
		Code:     "MATERIALIZE_REFUSED",
		Message:  "refused to materialize " + strings.Join(views, ", "),
	}
}
