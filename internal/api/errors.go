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
