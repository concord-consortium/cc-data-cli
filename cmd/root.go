package cmd

import (
	"errors"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

// version is the binary version, set by Execute from the ldflags-injected value.
var version = "dev"

// Version returns the binary version for callers that need it (skill stamp, etc).
func Version() string { return version }

const exitCodeHelp = `Exit codes:
  0  success (including nothing-new resumes)
  1  internal/other error, including lock-busy ("dataset is busy", "download busy")
  2  usage error
  3  NOT_AUTHENTICATED (run: cc-data login)
  4  not ready (--no-wait on an unfinished run, or the polling budget elapsing)
  5  server contract error (BAD_REQUEST, NOT_FOUND, terminal NOT_READY, EXPIRED_CURSOR)
  6  transient failure after the retry budget

Stream discipline: for get/query, stdout carries only machine output (the JSON
result line, --json documents, query results, or on failure the single JSON error
envelope); all progress, warnings, and prose go to stderr.`

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cc-data",
		Short:         "Download and query Concord Consortium researcher data",
		Long:          "cc-data downloads report CSVs, student answers, interactive state history, and\nfile attachments into local named datasets and queries across all of it with SQL\nvia an embedded DuckDB.\n\n" + exitCodeHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return persistentPreRun(cmd)
		},
	}
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &output.CLIError{ExitCode: output.ExitUsage, Code: "USAGE", Message: err.Error()}
	})
	root.AddCommand(newVersionCmd())
	root.AddCommand(newLoginCmd())
	root.AddCommand(newLogoutCmd())
	root.AddCommand(newAuthCmd())
	root.AddCommand(newDatasetCmd())
	root.AddCommand(newReportsCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newQueryCmd())
	root.AddCommand(newReplCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newUninstallCmd())
	return root
}

// Execute runs the root command and maps errors to the exit-code contract.
func Execute(v string) int {
	if v != "" {
		version = v
	}
	root := newRootCmd()
	err := root.Execute()
	if err == nil {
		return output.ExitSuccess
	}
	class := classifyExecError(err)
	if !class.Silent {
		output.EmitError(class)
	}
	return class.ExitCode
}

// classifyExecError maps a cobra Execute error into the exit-code contract.
func classifyExecError(err error) *output.CLIError {
	var cliErr *output.CLIError
	if errors.As(err, &cliErr) {
		return cliErr
	}
	if isCobraUsageError(err) {
		return &output.CLIError{ExitCode: output.ExitUsage, Code: "USAGE", Message: err.Error()}
	}
	return &output.CLIError{ExitCode: output.ExitInternal, Code: "INTERNAL", Message: err.Error()}
}

// isCobraUsageError recognizes the errors cobra raises while resolving a command
// or validating arg counts. Flag-parse errors never reach here: FlagErrorFunc
// already classifies them as exit-2 usage errors. Matching is by prefix against
// cobra's fixed message forms so an unrelated runtime error carrying a phrase
// like "invalid argument" is not misclassified as a usage error.
func isCobraUsageError(err error) bool {
	msg := err.Error()
	for _, p := range []string{
		"unknown command",
		"unknown flag",
		"unknown shorthand flag",
		"accepts ",  // cobra ExactArgs/RangeArgs: "accepts N arg(s), received M"
		"requires ", // cobra MinimumNArgs: "requires at least N arg(s)"
	} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// persistentPreRun is the hook where the skill freshness check is wired in a
// later step; it is a no-op for commands that need no setup.
func persistentPreRun(cmd *cobra.Command) error {
	freshnessCheck()
	return nil
}

// freshnessCheck is overridden by the Claude-integration step; default no-op.
var freshnessCheck = func() {}
