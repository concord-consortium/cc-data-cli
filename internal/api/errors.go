package api

import (
	"errors"
	"fmt"

	"github.com/concord-consortium/cc-data-cli/internal/output"
)

// Error codes in the server's landed vocabulary.
const (
	CodeBadRequest    = "BAD_REQUEST"
	CodeNotAuthed     = "NOT_AUTHENTICATED"
	CodeNotFound      = "NOT_FOUND"
	CodeNotReady      = "NOT_READY"
	CodeExpiredCursor = "EXPIRED_CURSOR"
	CodeServerError   = "SERVER_ERROR"
	CodeNotApplicable = "NOT_APPLICABLE"
	// CodePortalDuplicateUnnecessary is the 409 a duplicate of a Portal run answers without
	// force. AsCLIError needs no case for it: an unmapped code's message and extra reach the
	// exit-code contract unchanged, which is why the server returns a coded error rather than prose.
	CodePortalDuplicateUnnecessary = "PORTAL_DUPLICATE_UNNECESSARY"
)

// APIError is a coded, non-2xx response decoded from the JSON error envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Code
}

// IsExpiredCursor reports whether an error is a coded EXPIRED_CURSOR, which the
// paged fetcher catches for its restart rule.
func IsExpiredCursor(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == CodeExpiredCursor
}

// TransientError wraps a transport/429/5xx failure that survived the retry budget.
type TransientError struct {
	Attempts int
	Last     error
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("transient failure after %d attempts: %v", e.Attempts, e.Last)
}

func (e *TransientError) Unwrap() error { return e.Last }

// AsCLIError maps an API-layer error into the exit-code contract. NOT_READY is
// deliberately left as a contract error here; commands that poll (get report)
// intercept it before this mapping.
func AsCLIError(err error) *output.CLIError {
	if err == nil {
		return nil
	}
	var already *output.CLIError
	if errors.As(err, &already) {
		return already
	}
	// TransientError wraps the last APIError it saw, so it must be checked before
	// APIError or errors.As would surface that inner contract error instead.
	var transient *TransientError
	if errors.As(err, &transient) {
		return &output.CLIError{
			ExitCode: output.ExitTransient,
			Code:     "TRANSIENT",
			Message:  transient.Error(),
		}
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code == CodeNotAuthed {
			return output.NotAuthenticated()
		}
		return &output.CLIError{
			ExitCode: output.ExitContract,
			Code:     apiErr.Code,
			Message:  apiErr.Message,
			Extra:    apiErr.Extra,
		}
	}
	return &output.CLIError{ExitCode: output.ExitInternal, Code: "INTERNAL", Message: err.Error()}
}

// RunMayExistAction is what a caller must do when a write the client will not retry failed without
// the server answering. It names the portal the write was attempted on, because `reports list`
// without one reads the configured default, where the run would appear absent and invite the
// retry this advice exists to prevent.
func RunMayExistAction(portal string) string {
	if portal == "" {
		return "The run may still have been created; check with: cc-data reports list"
	}
	return fmt.Sprintf("The run may still have been created; check with: cc-data reports list --portal %s", portal)
}

// RunMayExistToolAction is RunMayExistAction for an MCP caller, who has reports_list and can take
// this step itself. Naming the CLI command instead would hand an agent an instruction it cannot
// follow. The convention is NotAuthenticated's, which says "A human must run: cc-data login"
// precisely because that step is the one an agent genuinely cannot do.
func RunMayExistToolAction(portal string) string {
	if portal == "" {
		return "The run may still have been created; check with reports_list"
	}
	return fmt.Sprintf("The run may still have been created; check with reports_list on portal %s", portal)
}

// AsWriteCLIError is AsCLIError for a request the client will not retry. Client.do returns a
// transport failure on a non-idempotent method immediately, precisely because the request may have
// reached the server, so the error has to say that rather than invite a retry.
func AsWriteCLIError(err error, action string) *output.CLIError {
	cliErr := AsCLIError(err)
	if answered(err) {
		return cliErr
	}
	// AsCLIError hands back the caller's own *output.CLIError when err already is one, so setting
	// Action in place would write into an error this function does not own. Nothing reachable
	// returns one from a write today; copying keeps that from becoming a trap.
	withAction := *cliErr
	withAction.Action = action
	return &withAction
}

// Whether the server itself answered, which is what proves the write did not happen. A 4xx is its
// answer; a 5xx can come from a proxy in front of it, so it proves nothing about what reached the
// application. The TransientError branch is defensive rather than reachable from a write: Client.do
// returns on the first attempt for a non-idempotent method and never exhausts a budget.
func answered(err error) bool {
	var transient *TransientError
	if errors.As(err, &transient) {
		return false
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500
}
