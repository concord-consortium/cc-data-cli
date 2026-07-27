package auth

import (
	"errors"
	"fmt"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
)

// ResolvePortalTarget parses a portal the CLI is about to talk to, and is the
// one entry point every portal-scoped command uses: login, logout, the reports
// commands, and the MCP portal arguments. It adds a single exception to
// config.ParsePortalTarget: a *config.UnshapedHostError host (a single-label
// non-loopback name the shape check turns away) is still accepted when a
// credential is actually stored under it.
//
// An older build accepted single-label hosts at login and auth status still
// lists what it stored, so refusing them outright would leave such a credential
// visible with no way to revoke it, its runs impossible to list, and, because
// login refuses it too, no way to refresh the token once it expires. A portal
// nothing is stored under stays a typo.
//
// The exception does NOT cover a credential stored under a literal environment
// alias name (e.g. "staging", which a pre-alias build stored as-is): on the
// target path ParsePortalTarget expands the alias before any shape check, so the
// input never reaches here as an unshaped host, and "logout --portal staging"
// resolves to the expanded hostname instead. Such a credential is still
// revocable with "cc-data uninstall", which adopts every stored host directly.
// Preferring the literal here would misroute a fresh "login staging", which must
// expand, so the gap is left to uninstall rather than papered over in the shared
// resolver.
func ResolvePortalTarget(v string) (config.Portal, string, error) {
	portal, origin, err := config.ParsePortalTarget(v)
	if err == nil {
		return portal, origin, nil
	}
	var unshaped *config.UnshapedHostError
	if !errors.As(err, &unshaped) {
		return config.Portal{}, "", err
	}
	stored, listErr := hasStoredCredential(unshaped.Portal)
	if stored {
		return unshaped.Portal, unshaped.Portal.Origin(), nil
	}
	if listErr != nil {
		// A store that cannot be read is not a typo. Reporting only the shape
		// error here would send the user to fix their spelling when the real
		// problem is that the credential listing failed.
		return config.Portal{}, "", fmt.Errorf("%w (could not read stored credentials: %v)", err, listErr)
	}
	return config.Portal{}, "", err
}

// hasStoredCredential reports whether a portal has a credential on disk. It
// reads the offline listing only, so it never prompts for a keyring unlock.
func hasStoredCredential(portal config.Portal) (bool, error) {
	infos, err := creds.Store{}.List()
	if err != nil {
		return false, err
	}
	for _, info := range infos {
		if info.Portal == portal.Host() {
			return true, nil
		}
	}
	return false, nil
}
