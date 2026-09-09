package api

import (
	"errors"
	"net/http"
	"strings"
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
		if got := AsWriteCLIError(err, RunMayExistAction("")); got.Action == RunMayExistAction("") {
			t.Fatalf("%s: a %d proves nothing was created, so it must not claim otherwise", err.Code, err.Status)
		}
	}
}

// A 401 keeps the login action AsCLIError already gives it.
func TestAsWriteCLIErrorKeepsTheAuthAction(t *testing.T) {
	got := AsWriteCLIError(&APIError{Status: http.StatusUnauthorized, Code: CodeNotAuthed}, RunMayExistAction(""))

	if got.ExitCode != output.ExitNotAuth || got.Action == "" {
		t.Fatalf("cli error = %+v", got)
	}
}

// Exit 6 tells a caller that retrying is the right move, which is true of a read and exactly wrong
// of a write the server may have taken: retrying that creates a second run. The unknown outcome is
// carried by the action field instead, which is why an unanswered write stays in the catch-all class.
func TestAsWriteCLIErrorDoesNotClassifyAnUnansweredWriteAsRetryable(t *testing.T) {
	got := AsWriteCLIError(errors.New("dial tcp: connection reset"), RunMayExistAction(""))

	if got.ExitCode == output.ExitTransient {
		t.Fatal("an unanswered write must not exit as transient; a script that retries it duplicates the run")
	}
	if got.ExitCode != output.ExitInternal {
		t.Fatalf("exit = %d, want %d", got.ExitCode, output.ExitInternal)
	}
}

// `reports list` without a portal reads the configured default, so a write attempted elsewhere
// would look absent and invite the retry this advice exists to prevent.
func TestRunMayExistActionNamesThePortalTheWriteWentTo(t *testing.T) {
	if got := RunMayExistAction("staging"); !strings.Contains(got, "reports list --portal staging") {
		t.Fatalf("action = %q", got)
	}
	if got := RunMayExistAction(""); strings.Contains(got, "--portal") {
		t.Fatalf("a caller who named no portal must not be told to name one: %q", got)
	}
}

func TestAsWriteCLIErrorSaysWhatAnUnansweredWriteMayHaveDone(t *testing.T) {
	unanswered := []error{
		errors.New("dial tcp: connection reset"),
		&APIError{Status: http.StatusBadGateway, Code: "HTTP_502"},
		&TransientError{Attempts: 3, Last: &APIError{Status: http.StatusServiceUnavailable, Code: CodeServerError}},
	}

	for _, err := range unanswered {
		if got := AsWriteCLIError(err, RunMayExistAction("")); got.Action != RunMayExistAction("") {
			t.Fatalf("%v: action = %q", err, got.Action)
		}
	}
}

// AsCLIError hands back the caller's own *output.CLIError when the error already is one, so
// AsWriteCLIError must not set Action on it in place. Nothing reachable returns one from a write
// today; this pins the copy so that stays true when something does.
func TestAsWriteCLIErrorDoesNotMutateTheCallersError(t *testing.T) {
	original := &output.CLIError{ExitCode: output.ExitInternal, Code: "INTERNAL", Message: "boom"}

	got := AsWriteCLIError(original, "check with: cc-data reports list")

	if original.Action != "" {
		t.Errorf("the caller's error was mutated: Action = %q", original.Action)
	}
	if got == original {
		t.Error("AsWriteCLIError returned the caller's own error rather than a copy")
	}
	if got.Action != "check with: cc-data reports list" {
		t.Errorf("Action = %q", got.Action)
	}
	if got.Code != original.Code || got.ExitCode != original.ExitCode || got.Message != original.Message {
		t.Errorf("the copy lost a field: %+v", got)
	}
}

func TestRunMayExistToolActionNamesTheToolNotTheCLI(t *testing.T) {
	withPortal := RunMayExistToolAction("staging")
	if !strings.Contains(withPortal, "reports_list") || !strings.Contains(withPortal, "staging") {
		t.Errorf("action = %q", withPortal)
	}
	// An agent has reports_list and can take this step itself, so naming the CLI would hand it an
	// instruction it cannot follow.
	if strings.Contains(withPortal, "cc-data") {
		t.Errorf("tool advice names a CLI command: %q", withPortal)
	}
	if bare := RunMayExistToolAction(""); strings.Contains(bare, "portal ") {
		t.Errorf("an unqualified caller must get unqualified advice, got %q", bare)
	}
}
