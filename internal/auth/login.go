package auth

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
)

// DefaultLoginTimeout matches the server's grant TTL.
const DefaultLoginTimeout = 5 * time.Minute

// LoginOptions parameterizes a login. Portal is a normalized host and Server a
// validated origin; a non-empty Token skips the PKCE flow and stores directly.
type LoginOptions struct {
	Portal   string
	Server   string
	Token    string
	Timeout  time.Duration
	Progress io.Writer
}

// Login runs the PKCE loopback flow (or stores a manual token) and saves the
// resulting credential for the portal.
func Login(ctx context.Context, opts LoginOptions) error {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultLoginTimeout
	}
	var store creds.Store

	if opts.Token != "" {
		if err := store.Save(opts.Portal, opts.Token, opts.Server); err != nil {
			return err
		}
		fmt.Fprintf(opts.Progress, "Stored token for %s.\n", opts.Portal)
		return nil
	}

	token, err := pkceFlow(ctx, opts)
	if err != nil {
		return err
	}
	if err := store.Save(opts.Portal, token, opts.Server); err != nil {
		return err
	}
	fmt.Fprintf(opts.Progress, "Logged in to %s.\n", opts.Portal)
	return nil
}

func pkceFlow(ctx context.Context, opts LoginOptions) (string, error) {
	verifier, err := GenerateVerifier()
	if err != nil {
		return "", err
	}
	state, err := GenerateState()
	if err != nil {
		return "", err
	}
	challenge := Challenge(verifier)

	lb, err := StartLoopback(state)
	if err != nil {
		return "", err
	}
	defer lb.Close()

	authURL := buildAuthURL(opts.Server, opts.Portal, lb.RedirectURI(), state, challenge)
	RedirectBrowserOutput(opts.Progress)
	fmt.Fprintf(opts.Progress, "Opening browser to log in. If it does not open, visit:\n%s\n", authURL)
	_ = OpenBrowser(authURL)

	code, err := lb.Wait(ctx, opts.Timeout)
	if err != nil {
		return "", err
	}

	hostname, _ := os.Hostname()
	label := "CLI login (" + hostname + ")"
	client := api.New(opts.Server, "")
	token, err := client.ExchangeCLIToken(ctx, code, verifier, label)
	if err != nil {
		return "", err
	}
	return token, nil
}

func buildAuthURL(server, portalHost, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("portal", config.PortalOrigin(portalHost))
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return server + "/auth/cli?" + q.Encode()
}
