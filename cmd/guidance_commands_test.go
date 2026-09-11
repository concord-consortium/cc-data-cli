package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/guidance"
)

// commandRefs matches a backticked cc-data invocation in the guidance, capturing
// the subcommand words that follow. It stops at the first token that is not a
// bare word, so flags, placeholders and quoted JSON are left out.
var commandRefs = regexp.MustCompile("`cc-data((?: [a-z][a-z-]*)+)")

// The guidance spells commands out because the core is forbidden to, so nothing
// else compares those spellings against the commands that exist. A renamed or
// removed subcommand would otherwise leave the guidance telling a researcher to
// run something that is gone. Names only: flags are not checked, because a flag
// like --portal is required only when no default portal is configured, so there
// is nothing declarative to compare against.
func TestGuidanceNamesOnlyRealCommands(t *testing.T) {
	root := newRootCmd()
	seen := map[string]bool{}
	// Both rendered surfaces: the skill spells out the CLI, and the MCP header
	// relays the login remedy, which is the one command an agent passes on.
	both := guidance.Skill() + "\n" + guidance.Instructions()
	for _, m := range commandRefs.FindAllStringSubmatch(both, -1) {
		words := strings.Fields(m[1])
		if seen[strings.Join(words, " ")] {
			continue
		}
		seen[strings.Join(words, " ")] = true
		// Find returns the deepest command it matched plus the args it could not
		// consume, and errors only when the first word is unknown, so a wrong
		// subcommand resolves to its parent with leftovers. The leftovers are the
		// signal.
		found, rest, err := root.Find(words)
		if err != nil {
			t.Errorf("the guidance names `cc-data %s`, which is not a command: %v", strings.Join(words, " "), err)
			continue
		}
		if len(rest) > 0 {
			t.Errorf("the guidance names `cc-data %s`, but %q is not a subcommand of %q",
				strings.Join(words, " "), rest[0], found.CommandPath())
		}
	}
	if len(seen) == 0 {
		t.Fatal("no cc-data invocations found in the guidance, so this checks nothing")
	}
	t.Logf("checked %d distinct invocations", len(seen))
}
