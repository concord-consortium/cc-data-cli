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
// config.ParsePortalTarget: a host the shape check turns away is still accepted
// when a credential is actually stored under it.
//
// An older build accepted single-label hosts at login and auth status still
// lists what it stored, so refusing them outright would leave such a credential
// visible with no way to revoke it, its runs impossible to list, and, because
// login refuses it too, no way to refresh the token once it expires. A portal
// nothing is stored under stays a typo.
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
