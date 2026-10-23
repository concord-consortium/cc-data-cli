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

func TestShowNamesARecentLogsDownloadByClass(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()
	renderShow(&dataset.ShowJSON{Ref: "learn.concord.org/x", Totals: map[string]int{}, Downloads: []dataset.DownloadJSON{
		{Type: "recent_logs", ClassID: 590, Complete: true},
		{Type: "answers", RunID: 584, Complete: true},
	}}, false)
	var rows []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "recent_logs") || strings.Contains(line, "answers") {
			rows = append(rows, strings.TrimSpace(line))
		}
	}
	if len(rows) != 2 || !strings.HasPrefix(rows[0], "class 590 ") || !strings.HasPrefix(rows[1], "584 ") {
		t.Fatalf("download rows = %q in:\n%s", rows, out.String())
	}
}
