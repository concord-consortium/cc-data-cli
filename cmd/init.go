package cmd

import (
	"fmt"

	"github.com/concord-consortium/cc-data-cli/internal/claude"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

// Wire the every-invocation skill freshness check into the root PersistentPreRun.
func init() {
	freshnessCheck = func() { claude.MaybeRefresh(version) }
}

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install the Claude Code skill and CLAUDE.md pointer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := claude.Install(version); err != nil {
				return output.Internalf("installing skill: %v", err)
			}
			dir, _ := claude.SkillDir()
			errOut := output.Stderr()
			fmt.Fprintf(errOut, "Installed the cc-data skill to %s and added the ~/.claude/CLAUDE.md pointer.\n", dir)
			fmt.Fprintln(errOut, "Next: log in to a portal with  cc-data login --portal <portal>")
			return nil
		},
	}
	return cmd
}
