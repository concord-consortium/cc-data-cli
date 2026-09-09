package cmd

import (
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/api"
)

// --help is the first place a CLI user looks, and the command branches on the run's execution, so
// the help has to describe both paths and the flags that differ between them. Same shape as the
// guidance and researcher-guide drift guards: prose that has to keep up with a branch in the code.
func TestGetReportHelpCoversBothExecutions(t *testing.T) {
	long := newGetReportCmd().Long
	if long == "" {
		t.Fatal("the command has no long help, so this asserts nothing")
	}
	for _, want := range []string{api.ExecutionAsync, api.ExecutionSync, "Athena", "Portal"} {
		if !strings.Contains(long, want) {
			t.Errorf("the help does not mention %q, so one of the two paths is undocumented", want)
		}
	}
	// Each flag that behaves differently on a Portal run says so somewhere in the help.
	for _, want := range []string{"--job is refused", "--no-wait and --poll-timeout do nothing"} {
		if !strings.Contains(long, want) {
			t.Errorf("the help does not say %q", want)
		}
	}
}
