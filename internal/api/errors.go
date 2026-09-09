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
// the server answering.
const RunMayExistAction = "The run may still have been created; check with: cc-data reports list"

// AsWriteCLIError is AsCLIError for a request the client will not retry. Client.do returns a
// transport failure on a non-idempotent method immediately, precisely because the request may have
// reached the server, so the error has to say that rather than invite a retry.
func AsWriteCLIError(err error, action string) *output.CLIError {
	cliErr := AsCLIError(err)
	if answered(err) {
		return cliErr
	}
	cliErr.Action = action
	return cliErr
}

// Whether the server itself answered, which is what proves the write did not happen. A 4xx is its
// answer; a 5xx can come from a proxy in front of it, and a retry budget that ran out says nothing
// about what the attempts before it did.
func answered(err error) bool {
	var transient *TransientError
	if errors.As(err, &transient) {
		return false
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500
}
