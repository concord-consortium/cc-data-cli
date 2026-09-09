package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/output"
)

func TestAsWriteCLIErrorLeavesTheServersOwnAnswerAlone(t *testing.T) {
	answered := []*APIError{
		{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: "cohort values must be integer ids"},
		{Status: http.StatusConflict, Code: CodePortalDuplicateUnnecessary, Message: "Re-read run 9"},
		{Status: http.StatusUnauthorized, Code: CodeNotAuthed},
	}

	for _, err := range answered {
		if got := AsWriteCLIError(err, RunMayExistAction); got.Action == RunMayExistAction {
			t.Fatalf("%s: a %d proves nothing was created, so it must not claim otherwise", err.Code, err.Status)
		}
	}
}

// A 401 keeps the login action AsCLIError already gives it.
func TestAsWriteCLIErrorKeepsTheAuthAction(t *testing.T) {
	got := AsWriteCLIError(&APIError{Status: http.StatusUnauthorized, Code: CodeNotAuthed}, RunMayExistAction)

	if got.ExitCode != output.ExitNotAuth || got.Action == "" {
		t.Fatalf("cli error = %+v", got)
	}
}

func TestAsWriteCLIErrorSaysWhatAnUnansweredWriteMayHaveDone(t *testing.T) {
	unanswered := []error{
		errors.New("dial tcp: connection reset"),
		&APIError{Status: http.StatusBadGateway, Code: "HTTP_502"},
		&TransientError{Attempts: 3, Last: &APIError{Status: http.StatusServiceUnavailable, Code: CodeServerError}},
	}

	for _, err := range unanswered {
		if got := AsWriteCLIError(err, RunMayExistAction); got.Action != RunMayExistAction {
			t.Fatalf("%v: action = %q", err, got.Action)
		}
	}
}
