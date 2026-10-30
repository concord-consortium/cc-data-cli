package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSegment(t *testing.T, dir, typ string, run int, lines ...string) *SegmentIndex {
	t.Helper()
	seg := OpenSegment(dir, typ, run)
	if err := os.MkdirAll(filepath.Dir(seg.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seg.Path(), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx, err := seg.BuildIndex()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func answerLine(q, data, fetchedAt string, run int) string {
	b, _ := json.Marshal(map[string]any{"source_key": "s", "remote_endpoint": "r", "question_id": q, "data": data, "_fetched_at": fetchedAt, "_run_id": run})
	return string(b)
}

func drain(t *testing.T, src MergeSource) (keys, data []string, runs []int) {
	t.Helper()
	for {
		key, rec, run, ok, err := src.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
		var f struct{ Data string }
		json.Unmarshal(rec, &f)
		keys, data, runs = append(keys, key), append(data, f.Data), append(runs, run)
	}
}

func TestBuildIndexKeepsTheLastOfRepeatedIdentities(t *testing.T) {
	// Enough lines that an unstable sort would reorder equal keys.
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, answerLine(fmt.Sprintf("q%d", i%7), fmt.Sprintf("v%d", i), "t", 1))
	}
	idx := writeSegment(t, t.TempDir(), TypeAnswers, 1, lines...)
	if idx.Len() != 7 || idx.RawCount() != 200 {
		t.Fatalf("Len %d RawCount %d, want 7 and 200", idx.Len(), idx.RawCount())
	}
	keys, data, _ := drain(t, NewSingleSource(idx, 1))
	if len(keys) != 7 {
		t.Fatalf("drained %d keys, want 7", len(keys))
	}
	for i, k := range keys {
		q := MustKind(TypeAnswers).Decode(k)[2]
		var n int
		fmt.Sscanf(q, "q%d", &n)
		if want := fmt.Sprintf("v%d", 199-(199-n)%7); data[i] != want {
			t.Fatalf("%s kept %s, want the last occurrence %s", q, data[i], want)
		}
	}
}

func TestMultiSourceBreaksAStampTieOnRunID(t *testing.T) {
	dir := t.TempDir()
	at := time.Unix(100, 0).UTC().Format(time.RFC3339)
	later := time.Unix(200, 0).UTC().Format(time.RFC3339)
	byRun := map[int]*SegmentIndex{
		3: writeSegment(t, dir, TypeAnswers, 3, answerLine("shared", "run3", at, 3), answerLine("only3", "run3", at, 3)),
		7: writeSegment(t, dir, TypeAnswers, 7, answerLine("shared", "run7", at, 7), answerLine("newer", "run7", at, 7)),
		5: writeSegment(t, dir, TypeAnswers, 5, answerLine("shared", "run5", at, 5), answerLine("newer", "run5", later, 5)),
	}
	keys, data, runs := drain(t, NewMultiSource(byRun))
	got := map[string]string{}
	for i, k := range keys {
		got[MustKind(TypeAnswers).Decode(k)[2]] = data[i]
		if want := map[string]int{"run3": 3, "run5": 5, "run7": 7}[data[i]]; runs[i] != want {
			t.Fatalf("record %q owned by run %d", data[i], runs[i])
		}
	}
	want := map[string]string{"shared": "run7", "newer": "run5", "only3": "run3"}
	if len(got) != 3 || got["shared"] != want["shared"] || got["newer"] != want["newer"] || got["only3"] != want["only3"] {
		t.Fatalf("winners %v, want %v", got, want)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys out of order: %q", keys)
		}
	}
}
