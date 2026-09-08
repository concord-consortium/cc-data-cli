package guidance

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// leadingNames matches the backticked identifiers that open a catalog entry, in either
// markup the guarded files use: a bulleted list item ("- `x`, `y` — ...") or a markdown
// table row ("| `x`, `y` | ...").
var leadingNames = regexp.MustCompile("^(?:- |\\| )((?:`[a-z0-9_]+`(?:, )?)+)")

// ParseCatalog returns every name documented in the first section whose heading
// contains heading. The section runs to the next heading of any level, and a later
// heading containing heading does not reopen it. Fenced code blocks are skipped whole,
// so a comment line inside one neither ends the section nor contributes entries. A
// section that is not found, or is found empty, is an error: the "every documented name
// still exists" check would otherwise pass against nothing.
func ParseCatalog(body, heading string) ([]string, error) {
	var names []string
	found, in, closed, fenced := false, false, false, false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if strings.HasPrefix(line, "#") {
			closed = closed || in
			in = !closed && strings.Contains(line, heading)
			found = found || in
			continue
		}
		if !in {
			continue
		}
		if m := leadingNames.FindStringSubmatch(line); m != nil {
			for _, tok := range strings.Split(m[1], ", ") {
				names = append(names, strings.Trim(tok, "`"))
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("no section heading containing %q", heading)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("section %q documents no names", heading)
	}
	return names, nil
}

// Missing returns the names in want that are absent from have, sorted, so a guard
// failure names the entries to fix rather than reporting that two sets differ. It
// ships here rather than in the guard's test file so the negative-control test
// exercises the same comparison the guard does.
func Missing(want, have []string) []string {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	var out []string
	for _, w := range want {
		if !set[w] {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}
