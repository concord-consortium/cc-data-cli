package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/auth"
	"github.com/concord-consortium/cc-data-cli/internal/claude"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

// revokeTimeout bounds each portal's server-side revoke during uninstall.
const revokeTimeout = 20 * time.Second

func newUninstallCmd() *cobra.Command {
	var removeCreds bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Claude Code skill, pointer, and MCP registration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			errOut := output.Stderr()
			if err := claude.Uninstall(); err != nil {
				return output.Internalf("removing skill: %v", err)
			}
			fmt.Fprintln(errOut, "Removed the cc-data skill and the ~/.claude/CLAUDE.md pointer.")

			// Unregister the MCP server that init registered (mirror of init).
			unregisterMCP(errOut)

			var credErr error
			if removeCreds {
				credErr = removeCredentials(errOut)
			}

			// Always tell the user where their datasets remain.
			cfg, err := config.Load()
			if err == nil {
				if root, derr := cfg.DataRootDir(); derr == nil {
					fmt.Fprintf(errOut, "Your datasets remain at %s (sensitive student data). Remove them manually or with cc-data dataset delete/purge when no longer needed.\n", root)
				}
			}
			return credErr
		},
	}
	cmd.Flags().BoolVar(&removeCreds, "credentials", false, "also revoke and remove stored credentials and config")
	return cmd
}

// unregisterMCP removes the Claude Code MCP registration and reports the outcome.
// Failures are warnings, not fatal: the skill removal already succeeded.
func unregisterMCP(errOut io.Writer) {
	res, err := claude.UnregisterMCP()
	if err != nil {
		fmt.Fprintf(errOut, "Warning: could not unregister the Claude Code MCP server: %v\n", err)
		fmt.Fprintf(errOut, "  Remove it manually with: claude mcp remove -s user %s\n", claude.MCPServerName)
		return
	}
	if res.Removed {
		fmt.Fprintf(errOut, "Unregistered the Claude Code MCP server %q.\n", claude.MCPServerName)
	}
}

// removeCredentials revokes each portal's token (the logout path) then deletes it,
// then removes the config file. It returns an error (without claiming success) if
// the stored credentials could not be listed or the config file could not be
// removed, so uninstall never reports a false success on a security-sensitive path.
func removeCredentials(errOut io.Writer) error {
	var store creds.Store
	infos, err := store.List()
	if err != nil {
		return output.Internalf("could not list stored credentials; they were NOT revoked or removed: %v", err)
	}
	for _, info := range infos {
		// Bound each revoke so an offline or captive network cannot hang
		// uninstall for minutes per portal (the API client has no overall
		// timeout, only per-attempt deadlines across its retry budget).
		ctx, cancel := context.WithTimeout(context.Background(), revokeTimeout)
		lerr := auth.Logout(ctx, info.Portal, errOut)
		cancel()
		if lerr != nil {
			fmt.Fprintf(errOut, "warning: could not revoke token for %s: %v (it may still be active; revoke it in the token UI)\n", info.Portal, lerr)
		}
	}
	// Remove the config file.
	dir, derr := config.ConfigDir()
	if derr != nil {
		return output.Internalf("locating config dir: %v", derr)
	}
	if rerr := os.Remove(filepath.Join(dir, "config.json")); rerr != nil && !os.IsNotExist(rerr) {
		return output.Internalf("removing config file: %v", rerr)
	}
	fmt.Fprintln(errOut, "Removed stored credentials and config.")
	return nil
}
