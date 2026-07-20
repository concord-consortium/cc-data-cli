package api

import (
	"github.com/concord-consortium/cc-data-cli/internal/creds"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

// ForPortal builds a client whose base URL is the portal credential's recorded
// minting server, never the global config server_url, so a bearer token is only
// ever sent to the origin that issued it.
func ForPortal(portal string) (*Client, error) {
	var store creds.Store
	token, server, err := store.Get(portal)
	if err != nil {
		if err == creds.ErrNotFound {
			return nil, output.NotAuthenticated()
		}
		return nil, err
	}
	if server == "" {
		return nil, output.Internalf("no recorded server origin for portal %s; re-run cc-data login", portal)
	}
	return New(server, token), nil
}
