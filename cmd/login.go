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
		Use:   "login [environment] [flags]",
		Short: "Log in to a portal via the PKCE loopback flow",
		Long: fmt.Sprintf(`Log in to a portal and store an API token.

The optional environment argument sets the portal and its paired report server
in one word, so "cc-data login staging" points the staging portal at the staging
report server. The environments are %s. The same names work
on --portal and --server, and they expand independently, so
"--portal staging --server dev" is a staging portal against a local dev server.
A full hostname or URL still works on either flag, and each flag overrides its
own side of the environment argument.

With no environment and no --portal, the configured default_portal is used,
falling back to the production portal (%s).

The report server is chosen in this order: --server, the server paired with the
resolved portal, the server of the environment named as an argument, the
configured server_url, then the built-in default (%s).
A known portal always brings its own paired server, and naming an environment
covers a portal that has none, so server_url applies only to a portal reached
without either; when one of them overrides it, the login says which server it
used.

The default flow opens a browser to complete a PKCE loopback login. For headless
or SSH sessions, pass --token - to read a token from stdin (piped, or an
echo-off prompt on a TTY); this is the recommended manual form. The bare
--token <value> form works but is discouraged: flag values land in shell history
and process lists.`, strings.Join(config.EnvironmentNames(), " / "), config.ProductionPortal, config.DefaultServerURL),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			env, err := resolveEnvironmentArg(args)
			if err != nil {
				return err
			}
			target, err := resolveLoginTarget(cfg, env, portal, server)
			if err != nil {
				return err
			}

			// Say this before resolveToken: a manually pasted token is issued by
			// one specific report server, so which server the login targets has
			// to be on screen before the paste, not after it.
			if target.ignoredServerURL != "" {
				fmt.Fprintf(output.Stderr(),
					"Using %s, %s; the configured server_url %s was not used (pass --server to override).\n",
					target.server, target.serverReason(), target.ignoredServerURL)
			}

			rawToken, err := resolveToken(cmd, token)
			if err != nil {
				return err
			}

			return auth.Login(context.Background(), auth.LoginOptions{
				Portal:       target.portal,
				PortalOrigin: target.portalOrigin,
				Server:       target.server,
				Token:        rawToken,
				Progress:     output.Stderr(),
			})
		},
	}
	cmd.Flags().StringVar(&portal, "portal", "", fmt.Sprintf("portal to log in to: an environment alias or a hostname (default: default_portal, else %s)", config.ProductionPortal))
	cmd.Flags().StringVar(&token, "token", "", "store this token instead of the browser flow; use - to read from stdin (recommended for manual pastes)")
	cmd.Flags().StringVar(&server, "server", "", fmt.Sprintf("report server: an environment alias or an origin (default: the environment's paired server, else config, else %s)", config.DefaultServerURL))
	return cmd
}

// resolveEnvironmentArg resolves the optional positional environment argument.
// A value that is not a known alias is a usage error rather than a portal host:
// the positional slot exists only for the aliases, and silently treating a typo
// as a hostname would send a login somewhere unexpected.
func resolveEnvironmentArg(args []string) (*config.Environment, error) {
	if len(args) == 0 {
		return nil, nil
	}
	env, ok := config.LookupEnvironment(args[0])
	if !ok {
		return nil, output.Usagef("unknown environment %q: expected one of %s (use --portal for a hostname)",
			args[0], strings.Join(config.EnvironmentNames(), ", "))
	}
	return &env, nil
}

// loginTarget is the resolved portal host, the origin the auth flow sends that
// portal as, and the report-server origin for a login.
type loginTarget struct {
	portal       config.Portal
	portalOrigin string
	server       string
	// serverFrom says why the server was chosen, so the notice below can name
	// the reason instead of claiming a pairing that may not exist.
	serverFrom serverSource
	// ignoredServerURL is a configured server_url the chosen server outranked. A
	// login that quietly targets a server other than the one the config names is
	// exactly the mismatch that later reads as "run not found", so the caller
	// says so instead of letting it pass in silence.
	ignoredServerURL string
}

// serverSource is why a login's report server was chosen, for the notice that
// reports an outranked server_url.
type serverSource int

const (
	serverFromNothing serverSource = iota
	// serverFromPairing: the resolved portal is a known portal, which brings its
	// own server.
	serverFromPairing
	// serverFromEnvironment: the portal is one no environment claims, and an
	// environment named as an argument supplied the server instead.
	serverFromEnvironment
)

// resolveLoginTarget applies the precedence: an explicit flag wins over the
// environment argument, which wins over the configured defaults. --portal and
// --server expand independently so they can name different environments.
//
// The server is whatever --server states, else the one paired with the resolved
// portal host, else the one belonging to an environment named as an argument,
// else the configured server_url. The pairing outranks the environment argument
// so that "staging", "--portal staging", and
// "--portal learn.portal.staging.concord.org" run through one code path and land
// on the same server, reporting an overridden server_url identically; server_url
// is left to the portals neither the pairing nor a named environment covers.
func resolveLoginTarget(cfg *config.Config, env *config.Environment, portalFlag, serverFlag string) (loginTarget, error) {
	var rawPortal, rawServer string
	if env != nil {
		rawPortal = env.Portal.Host()
	}
	if portalFlag != "" {
		rawPortal = portalFlag
	}
	if serverFlag != "" {
		rawServer = config.ExpandServerAlias(serverFlag)
	}
	if rawPortal == "" {
		rawPortal = cfg.DefaultPortal
	}
	if rawPortal == "" {
		rawPortal = config.ProductionPortal
	}

	host, portalOrigin, err := auth.ResolvePortalTarget(rawPortal)
	if err != nil {
		return loginTarget{}, output.Usagef("%v", err)
	}
	var ignoredServerURL string
	from := serverFromNothing
	if rawServer == "" {
		var paired string
		if paired, from = pairedServer(host, env); from != serverFromNothing {
			rawServer = paired
			if cfg.ServerURL != "" && cfg.ServerURL != rawServer {
				ignoredServerURL = cfg.ServerURL
			}
		} else {
			rawServer = cfg.ServerOrigin()
		}
	}
	// Validate after expansion so an alias cannot widen the origin allowlist.
	origin, err := config.ValidateServerURL(rawServer)
	if err != nil {
		return loginTarget{}, output.Usagef("%v", err)
	}
	return loginTarget{
		portal:           host,
		portalOrigin:     portalOrigin,
		server:           origin,
		serverFrom:       from,
		ignoredServerURL: ignoredServerURL,
	}, nil
}

// pairedServer returns the report server a login should use when --server was
// not given: the one paired with the resolved portal, else the one belonging to
// an environment named as an argument. That second case is what keeps
// "cc-data login dev --portal localhost:3005" a dev login; a portal no
// environment claims would otherwise fall through to the production default
// while the command line said dev.
func pairedServer(portal config.Portal, env *config.Environment) (string, serverSource) {
	if pe, ok := config.EnvironmentForPortal(portal); ok {
		return pe.Server, serverFromPairing
	}
	if env != nil {
		return env.Server, serverFromEnvironment
	}
	return "", serverFromNothing
}

// serverReason renders why the login used the server it did, for the notice.
// Naming a pairing that does not exist would send someone hunting the alias
// table for an entry they will not find.
func (t loginTarget) serverReason() string {
	if t.serverFrom == serverFromEnvironment {
		return "the report server for the environment you named"
	}
	return "the report server paired with " + t.portal.Host()
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
