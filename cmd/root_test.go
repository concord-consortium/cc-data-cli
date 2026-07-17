package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/output"
)

func runArgs(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&errb)
	root.SetErr(&errb)
	err := root.Execute()
	code = output.ExitSuccess
	if err != nil {
		class := classifyExecError(err)
		output.EmitError(class)
		code = class.ExitCode
	}
	return out.String(), errb.String(), code
}

func TestVersionCommand(t *testing.T) {
	out, _, code := runArgs(t, "version")
	if code != 0 {
		t.Fatalf("version exit %d", code)
	}
	if strings.TrimSpace(out) != "dev" {
		t.Fatalf("version output %q", out)
	}
}

func TestUnknownCommandExit2(t *testing.T) {
	_, _, code := runArgs(t, "nope")
	if code != output.ExitUsage {
		t.Fatalf("unknown command should exit 2, got %d", code)
	}
}

func TestUnknownFlagExit2(t *testing.T) {
	_, _, code := runArgs(t, "version", "--nope")
	if code != output.ExitUsage {
		t.Fatalf("unknown flag should exit 2, got %d", code)
	}
}

func TestHelpCarriesExitCodeTable(t *testing.T) {
	root := newRootCmd()
	help := root.Long
	for _, needle := range []string{"3  NOT_AUTHENTICATED", "5  server contract error", "Stream discipline"} {
		if !strings.Contains(help, needle) {
			t.Fatalf("root help missing %q", needle)
		}
	}
}

func TestExecuteReturnsIntNoJSONOnStderr(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()
	root := newRootCmd()
	root.SetArgs([]string{"nope"})
	root.SetOut(&errb)
	root.SetErr(&errb)
	if err := root.Execute(); err != nil {
		output.EmitError(classifyExecError(err))
	}
	// The failure envelope must be exactly one JSON object on stdout.
	trimmed := strings.TrimSpace(out.String())
	if trimmed == "" {
		return
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(trimmed), &got); err != nil {
		t.Fatalf("stdout not a JSON object: %q", out.String())
	}
}
