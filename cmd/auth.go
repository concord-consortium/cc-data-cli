package cmd

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/auth"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Inspect stored credentials",
	}
	cmd.AddCommand(newAuthStatusCmd())
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	var check, asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List portals with stored credentials",
		Long:  "Lists portals with stored credentials offline. --check validates each token against its server and shows its metadata. Always exits 0 when the command completes; per-portal validity travels in the output.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := auth.Status(context.Background(), check)
			if err != nil {
				return err
			}
			if asJSON {
				return output.JSONLine(res)
			}
			renderStatus(res, check)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "validate each token against its server")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func renderStatus(res auth.StatusResult, check bool) {
	out := output.Stdout()
	if res.DefaultPortal != "" {
		fmt.Fprintf(out, "default portal: %s\n\n", res.DefaultPortal)
	}
	if len(res.Portals) == 0 {
		fmt.Fprintln(out, "no stored credentials")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if check {
		fmt.Fprintln(tw, "PORTAL\tBACKEND\tSTORED\tVALID\tLABEL\tLAST USED\tREPORT ACCESS")
	} else {
		fmt.Fprintln(tw, "PORTAL\tBACKEND\tSTORED")
	}
	for _, p := range res.Portals {
		if check {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				p.Portal, p.Backend, fmtTime(p.StoredAt),
				validText(p), labelText(p.Label), lastUsedText(p), reportAccessText(p))
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Portal, p.Backend, fmtTime(p.StoredAt))
		}
	}
	tw.Flush()
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func validText(p auth.PortalStatus) string {
	if p.CheckError != "" && !p.Valid {
		return "unknown"
	}
	if p.Valid {
		return "yes"
	}
	return "no"
}

func labelText(label *string) string {
	if label == nil {
		return "(no label)"
	}
	return *label
}

func lastUsedText(p auth.PortalStatus) string {
	if p.MetadataUnknown {
		return "(unknown)"
	}
	if p.LastUsedAt == nil {
		return "never"
	}
	return p.LastUsedAt.UTC().Format(time.RFC3339)
}

func reportAccessText(p auth.PortalStatus) string {
	if p.ReportAccess == nil {
		return "(unknown)"
	}
	if *p.ReportAccess {
		return "yes"
	}
	return "no"
}
