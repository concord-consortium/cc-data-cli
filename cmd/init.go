package cmd

import (
	"fmt"
	"io"

	"github.com/concord-consortium/cc-data-cli/internal/claude"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

// Wire the every-invocation skill freshness check into the root PersistentPreRun.
func init() {
	freshnessCheck = func() { claude.MaybeRefresh(version) }
}

func newInitCmd() *cobra.Command {
	var noMCP bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install the Claude Code skill and pointer, and register the MCP server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := claude.Install(version); err != nil {
				return output.Internalf("installing skill: %v", err)
			}
			dir, _ := claude.SkillDir()
			errOut := output.Stderr()
			fmt.Fprintf(errOut, "Installed the cc-data skill to %s and added the ~/.claude/CLAUDE.md pointer.\n", dir)

			if !noMCP {
				registerMCP(errOut)
			}
			fmt.Fprintln(errOut, "Next: log in to a portal with  cc-data login")
			return nil
		},
	}
	cmd.Flags().BoolVar(&noMCP, "no-mcp", false, "skip registering the Claude Code MCP server")
	return cmd
}

// registerMCP registers the stdio MCP server with Claude Code (user scope) and
// reports the outcome. Failures are warnings, not fatal: the skill install above
// already succeeded, and the user can register the MCP by hand.
func registerMCP(errOut io.Writer) {
	res, err := claude.RegisterMCP()
	if err != nil {
		fmt.Fprintf(errOut, "Warning: could not register the Claude Code MCP server: %v\n", err)
		fmt.Fprintf(errOut, "  Register it manually with: %s\n", claude.ManualMCPCommand(""))
		return
	}
	switch {
	case res.SkippedReason != "":
		fmt.Fprintf(errOut, "Skipped MCP registration (%s).\n", res.SkippedReason)
		fmt.Fprintf(errOut, "  Register it later with: %s\n", claude.ManualMCPCommand(res.Binary))
	case res.AlreadyPresent:
		fmt.Fprintf(errOut, "Claude Code MCP server %q is already registered.\n", claude.MCPServerName)
	case res.Registered:
		fmt.Fprintf(errOut, "Registered the Claude Code MCP server %q (user scope).\n", claude.MCPServerName)
	}
}
