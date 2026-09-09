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

// Exit 6 tells a caller that retrying is the right move, which is true of a read and exactly wrong
// of a write the server may have taken: retrying that creates a second run. The unknown outcome is
// carried by the action field instead, which is why an unanswered write stays in the catch-all class.
func TestAsWriteCLIErrorDoesNotClassifyAnUnansweredWriteAsRetryable(t *testing.T) {
	got := AsWriteCLIError(errors.New("dial tcp: connection reset"), RunMayExistAction)

	if got.ExitCode == output.ExitTransient {
		t.Fatal("an unanswered write must not exit as transient; a script that retries it duplicates the run")
	}
	if got.ExitCode != output.ExitInternal {
		t.Fatalf("exit = %d, want %d", got.ExitCode, output.ExitInternal)
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
