package auth

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
)

// Logout revokes the portal's token server-side then removes it locally. A 401
// (already invalid) or a 404 (older server without the revoke route) still
// removes the local credential and exits successfully; only an unexpected error
// aborts before local deletion.
func Logout(ctx context.Context, portal string, progress io.Writer) error {
	var store creds.Store
	token, server, err := store.Get(portal)
	if errors.Is(err, creds.ErrNotFound) {
		fmt.Fprintf(progress, "No stored credential for %s; nothing to do.\n", portal)
		return nil
	}
	if err != nil {
		return err
	}

	client := api.New(server, token)
	revokeErr := client.RevokeCurrentToken(ctx)
	var apiErr *api.APIError
	switch {
	case revokeErr == nil:
		// revoked
	case errors.As(revokeErr, &apiErr) && apiErr.Code == api.CodeNotAuthed:
		fmt.Fprintf(progress, "Token for %s was already invalid server-side; nothing needed revoking.\n", portal)
	case errors.As(revokeErr, &apiErr) && apiErr.Code == api.CodeNotFound:
		fmt.Fprintf(progress, "This server does not support token revocation; the token may still be active. Revoke it in the report server token UI (%s).\n", server)
	default:
		return revokeErr
	}

	if err := store.Delete(portal); err != nil {
		return err
	}
	fmt.Fprintf(progress, "Removed local credential for %s.\n", portal)
	return nil
}
