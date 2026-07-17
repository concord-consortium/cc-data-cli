package cmd

import (
	"context"

	"github.com/concord-consortium/cc-data-cli/internal/mcpserver"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
	var allowDirs []string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run a stdio MCP server for Claude Desktop",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := mcpserver.Run(context.Background(), mcpserver.Options{Version: version, AllowDirs: allowDirs}); err != nil {
				return output.Internalf("mcp server: %v", err)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&allowDirs, "allow-dir", nil, "additional directory the query tool may read (repeatable)")
	return cmd
}
