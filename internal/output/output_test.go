package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmitErrorSingleJSONObject(t *testing.T) {
	var out, errb bytes.Buffer
	restore := SetStreams(&out, &errb)
	defer restore()

	EmitError(&CLIError{
		ExitCode: ExitNotReady,
		Code:     "NOT_READY",
		Message:  "not ready",
		Extra:    map[string]any{"athena_query_state": "running"},
	})

	if errb.Len() != 0 {
		t.Fatalf("stderr must be empty, got %q", errb.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one stdout line, got %d: %q", len(lines), out.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("stdout not valid JSON: %v", err)
	}
	if got["error"] != "NOT_READY" || got["message"] != "not ready" || got["athena_query_state"] != "running" {
		t.Fatalf("unexpected envelope: %v", got)
	}
}

func TestEnvelopeOmitsEmptyFields(t *testing.T) {
	e := &CLIError{Code: "BAD_REQUEST", Message: "bad"}
	env := e.Envelope()
	if _, ok := env["action"]; ok {
		t.Fatalf("empty action should be omitted: %v", env)
	}
	if env["error"] != "BAD_REQUEST" {
		t.Fatalf("error field wrong: %v", env)
	}
}

func TestNotAuthenticated(t *testing.T) {
	e := NotAuthenticated()
	if e.ExitCode != ExitNotAuth || e.Code != "NOT_AUTHENTICATED" {
		t.Fatalf("wrong not-auth error: %+v", e)
	}
	if e.Action != "A human must run: cc-data login" {
		t.Fatalf("wrong action: %q", e.Action)
	}
}

func TestExitCodeConstants(t *testing.T) {
	matrix := []struct {
		got  int
		want int
	}{
		{ExitSuccess, 0},
		{ExitInternal, 1},
		{ExitUsage, 2},
		{ExitNotAuth, 3},
		{ExitNotReady, 4},
		{ExitContract, 5},
		{ExitTransient, 6},
	}
	for _, m := range matrix {
		if m.got != m.want {
			t.Fatalf("exit constant mismatch: got %d want %d", m.got, m.want)
		}
	}
}

func TestResultLineToStdout(t *testing.T) {
	var out, errb bytes.Buffer
	restore := SetStreams(&out, &errb)
	defer restore()

	if err := ResultLine(map[string]any{"type": "answers", "complete": true}); err != nil {
		t.Fatal(err)
	}
	if errb.Len() != 0 {
		t.Fatalf("result line must not touch stderr: %q", errb.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got["complete"] != true {
		t.Fatalf("bad result line: %v", got)
	}
}

func TestProgressAndWarnGoToStderr(t *testing.T) {
	var out, errb bytes.Buffer
	restore := SetStreams(&out, &errb)
	defer restore()

	Progressf("polling %d", 1)
	Warnf("heads up")
	if out.Len() != 0 {
		t.Fatalf("progress/warn must not touch stdout: %q", out.String())
	}
	if !strings.Contains(errb.String(), "polling 1") || !strings.Contains(errb.String(), "warning: heads up") {
		t.Fatalf("stderr missing content: %q", errb.String())
	}
}
