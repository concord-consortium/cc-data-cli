// Package output centralizes the CLI's exit-code classes and stdout/stderr
// stream discipline so no command can improvise them.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const (
	ExitSuccess   = 0
	ExitInternal  = 1
	ExitUsage     = 2
	ExitNotAuth   = 3
	ExitNotReady  = 4
	ExitContract  = 5
	ExitTransient = 6
)

// CLIError carries the coarse exit-code class plus the specific error code and
// the machine-readable envelope fields. Silent means the exit code applies but
// no error envelope is printed (the command already emitted its result line to
// stdout, e.g. --no-wait on a not-ready run).
type CLIError struct {
	ExitCode int
	Code     string
	Message  string
	Action   string
	Extra    map[string]any
	Silent   bool
}

func (e *CLIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Envelope renders the single-line JSON error object, omitting empty fields.
func (e *CLIError) Envelope() map[string]any {
	m := map[string]any{"error": e.Code}
	if e.Message != "" {
		m["message"] = e.Message
	}
	if e.Action != "" {
		m["action"] = e.Action
	}
	for k, v := range e.Extra {
		m[k] = v
	}
	return m
}

// NotAuthenticated is the canonical exit-3 error every command shares.
func NotAuthenticated() *CLIError {
	return &CLIError{
		ExitCode: ExitNotAuth,
		Code:     "NOT_AUTHENTICATED",
		Message:  "You must supply a valid API token.",
		Action:   "A human must run: cc-data login",
	}
}

// Usagef builds an exit-2 usage error.
func Usagef(format string, args ...any) *CLIError {
	return &CLIError{ExitCode: ExitUsage, Code: "USAGE", Message: fmt.Sprintf(format, args...)}
}

// Internalf builds an exit-1 internal error.
func Internalf(format string, args ...any) *CLIError {
	return &CLIError{ExitCode: ExitInternal, Code: "INTERNAL", Message: fmt.Sprintf(format, args...)}
}

// Busyf builds an exit-1 lock-busy error.
func Busyf(format string, args ...any) *CLIError {
	return &CLIError{ExitCode: ExitInternal, Code: "BUSY", Message: fmt.Sprintf(format, args...)}
}

var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// SetStreams overrides the output streams; used by tests and MCP handlers.
func SetStreams(out, err io.Writer) (restore func()) {
	prevOut, prevErr := stdout, stderr
	stdout, stderr = out, err
	return func() { stdout, stderr = prevOut, prevErr }
}

// Stdout and Stderr expose the current streams for callers that need them.
func Stdout() io.Writer { return stdout }
func Stderr() io.Writer { return stderr }

// ResultLine marshals v as one JSON line to stdout, the standard get result line.
func ResultLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(b))
	return err
}

// JSONLine writes any value as a single JSON line to stdout.
func JSONLine(v any) error { return ResultLine(v) }

// EmitError writes the single-line JSON error envelope to stdout.
func EmitError(e *CLIError) {
	b, _ := json.Marshal(e.Envelope())
	fmt.Fprintln(stdout, string(b))
}

// Progressf writes a progress line to stderr only.
func Progressf(format string, args ...any) {
	fmt.Fprintf(stderr, format+"\n", args...)
}

// Warnf writes a warning to stderr only.
func Warnf(format string, args ...any) {
	fmt.Fprintf(stderr, "warning: "+format+"\n", args...)
}
