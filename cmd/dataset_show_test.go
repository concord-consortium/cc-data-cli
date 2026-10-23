package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

func TestShowTotalsAlignOnTheLongestStoreName(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()
	renderShow(&dataset.ShowJSON{Ref: "learn.concord.org/x", Totals: map[string]int{"answers": 1, "clue_documents": 22, "clue_history": 333}}, false)
	col, matched := -1, 0
	for _, line := range strings.Split(out.String(), "\n") {
		for _, name := range []string{"answers", "clue_documents", "clue_history"} {
			if strings.HasPrefix(strings.TrimSpace(line), name+" ") {
				matched++
				at := strings.LastIndex(line, " ") + 1
				if col == -1 {
					col = at
				} else if at != col {
					t.Fatalf("totals counts misaligned:\n%s", out.String())
				}
			}
		}
	}
	if matched != 3 {
		t.Fatalf("found %d of 3 totals lines in:\n%s", matched, out.String())
	}
}
