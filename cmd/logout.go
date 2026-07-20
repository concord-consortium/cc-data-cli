package cmd

import (
	"context"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/auth"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	var portal string
	cmd := &cobra.Command{
		Use:   "logout --portal <portal>",
		Short: "Revoke and remove a portal's stored token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if portal == "" {
				return output.Usagef("--portal is required")
			}
			host, err := config.NormalizePortal(portal)
			if err != nil {
				return output.Usagef("%v", err)
			}
			if err := auth.Logout(context.Background(), host, output.Stderr()); err != nil {
				return api.AsCLIError(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", "portal to log out of")
	return cmd
}
