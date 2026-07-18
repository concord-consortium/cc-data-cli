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
		Short: "Remove the Claude Code skill and pointer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			errOut := output.Stderr()
			if err := claude.Uninstall(); err != nil {
				return output.Internalf("removing skill: %v", err)
			}
			fmt.Fprintln(errOut, "Removed the cc-data skill and the ~/.claude/CLAUDE.md pointer.")

			if removeCreds {
				removeCredentials(errOut)
			}

			// Always tell the user where their datasets remain.
			cfg, err := config.Load()
			if err == nil {
				if root, derr := cfg.DataRootDir(); derr == nil {
					fmt.Fprintf(errOut, "Your datasets remain at %s (sensitive student data). Remove them manually or with cc-data dataset delete/purge when no longer needed.\n", root)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&removeCreds, "credentials", false, "also revoke and remove stored credentials and config")
	return cmd
}

// removeCredentials revokes each portal's token (the logout path) then deletes it,
// then removes the config file.
func removeCredentials(errOut io.Writer) {
	var store creds.Store
	infos, err := store.List()
	if err == nil {
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
	}
	// Remove the config file.
	if dir, derr := config.ConfigDir(); derr == nil {
		os.Remove(filepath.Join(dir, "config.json"))
		fmt.Fprintln(errOut, "Removed stored credentials and config.")
	}
}
