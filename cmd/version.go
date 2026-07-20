package cmd

import (
	"fmt"

	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the binary version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(output.Stdout(), version)
			return nil
		},
	}
}
