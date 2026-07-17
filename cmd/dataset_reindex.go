package cmd

import (
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newReindexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reindex <ref>",
		Short: "Rebuild a dataset's manifest from the filesystem",
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
			echoRef(ref)
			d := dataset.Open(root, ref)
			if err := d.Reindex(); err != nil {
				return mutationErr(err)
			}
			output.Progressf("reindexed %s", ref)
			return nil
		},
	}
	return cmd
}
