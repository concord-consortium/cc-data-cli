package cmd

import (
	"bytes"
	"encoding/json"
	"os"
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
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := buf.String()
	// Every exit-code class must appear in --help, matching the output constants.
	rows := []struct {
		code int
		text string
	}{
		{output.ExitSuccess, "0  success"},
		{output.ExitInternal, "1  internal"},
		{output.ExitUsage, "2  usage error"},
		{output.ExitNotAuth, "3  NOT_AUTHENTICATED"},
		{output.ExitNotReady, "4  not ready"},
		{output.ExitContract, "5  server contract error"},
		{output.ExitTransient, "6  transient failure"},
	}
	for _, r := range rows {
		want := strings.TrimSpace(r.text)
		if !strings.Contains(help, want) {
			t.Fatalf("--help missing exit-code row %q", want)
		}
		// The leading digit must equal the constant.
		if int(want[0]-'0') != r.code {
			t.Fatalf("exit-code table row %q does not match constant %d", want, r.code)
		}
	}
	if !strings.Contains(help, "Stream discipline") {
		t.Fatal("--help missing the stream-discipline note")
	}
}

func TestExecuteReturnsIntNoJSONOnStderr(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	// Exercise the package-level Execute, not a re-implementation of it. Execute
	// reads os.Args, so point it at a failing invocation and restore afterward.
	prevArgs := os.Args
	os.Args = []string{"cc-data", "nope"}
	defer func() { os.Args = prevArgs }()

	code := Execute("")
	if code != output.ExitUsage {
		t.Fatalf("Execute returned exit %d, want %d", code, output.ExitUsage)
	}

	// The failure envelope must be exactly one JSON object on stdout. No escape
	// hatch: empty stdout means the envelope was never emitted, which is a failure.
	trimmed := strings.TrimSpace(out.String())
	if trimmed == "" {
		t.Fatal("Execute emitted no failure envelope on stdout")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(trimmed), &got); err != nil {
		t.Fatalf("stdout not a JSON object: %q", out.String())
	}

	// The machine-readable envelope must not leak onto stderr.
	if strings.Contains(errb.String(), "{") {
		t.Fatalf("stderr carried JSON-shaped output: %q", errb.String())
	}
}
