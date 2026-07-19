package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/auth"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var portal, token, server string
	cmd := &cobra.Command{
		Use:   "login [--portal <portal>]",
		Short: "Log in to a portal via the PKCE loopback flow",
		Long: `Log in to a portal and store an API token.

If --portal is omitted, the configured default_portal is used, falling back to
the production portal (learn.concord.org).

The default flow opens a browser to complete a PKCE loopback login. For headless
or SSH sessions, pass --token - to read a token from stdin (piped, or an
echo-off prompt on a TTY); this is the recommended manual form. The bare
--token <value> form works but is discouraged: flag values land in shell history
and process lists.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			host, err := resolveLoginPortal(cfg, portal)
			if err != nil {
				return err
			}

			serverOrigin, err := resolveLoginServer(cfg, server)
			if err != nil {
				return err
			}

			rawToken, err := resolveToken(cmd, token)
			if err != nil {
				return err
			}

			return auth.Login(context.Background(), auth.LoginOptions{
				Portal:   host,
				Server:   serverOrigin,
				Token:    rawToken,
				Progress: output.Stderr(),
			})
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", fmt.Sprintf("portal to log in to (default: default_portal, else %s)", config.ProductionPortal))
	cmd.Flags().StringVar(&token, "token", "", "store this token instead of the browser flow; use - to read from stdin (recommended for manual pastes)")
	cmd.Flags().StringVar(&server, "server", "", "report server origin (default: config or https://report-server.concord.org; staging: https://report-server.concordqa.org)")
	return cmd
}

// resolveLoginPortal normalizes the --portal flag, else the configured
// default_portal, else the production portal, in that order.
func resolveLoginPortal(cfg *config.Config, flagVal string) (string, error) {
	raw := flagVal
	if raw == "" {
		raw = cfg.DefaultPortal
	}
	if raw == "" {
		raw = config.ProductionPortal
	}
	host, err := config.NormalizePortal(raw)
	if err != nil {
		return "", output.Usagef("%v", err)
	}
	return host, nil
}

func resolveLoginServer(cfg *config.Config, flagVal string) (string, error) {
	if flagVal != "" {
		origin, err := config.ValidateServerURL(flagVal)
		if err != nil {
			return "", output.Usagef("%v", err)
		}
		return origin, nil
	}
	return cfg.ServerOrigin(), nil
}

// resolveToken returns the raw token: "" for the PKCE flow, the flag value, or
// the stdin/TTY-prompt read when the flag is "-".
func resolveToken(cmd *cobra.Command, flagVal string) (string, error) {
	if flagVal == "" {
		return "", nil
	}
	if flagVal != "-" {
		return flagVal, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(output.Stderr(), "Paste token: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(output.Stderr())
		if err != nil {
			return "", err
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return "", output.Usagef("no token read from stdin")
		}
		return token, nil
	}
	reader := bufio.NewReader(os.Stdin)
	// ReadString may return the final line together with io.EOF when stdin has no
	// trailing newline; the token is still valid in that case, so ignore the error
	// and validate the trimmed content instead.
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	// Empty input (immediate EOF, or newline/whitespace-only) is invalid: never
	// silently fall back to the browser flow when --token - was requested.
	if line == "" {
		return "", output.Usagef("no token read from stdin")
	}
	return line, nil
}
