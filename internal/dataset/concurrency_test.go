package dataset

import (
	"sync"
	"testing"
)

// TestConcurrentMergesDifferentRuns runs two merges of different runs into one
// dataset concurrently and asserts no records are lost from either.
func TestConcurrentMergesDifferentRuns(t *testing.T) {
	d := newDataset(t)
	segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A"),
		rec("s", "e2", "q2", "", "A"),
	})
	segB := writeFinishedSegment(t, d, "answers", 612, [][]byte{
		rec("s", "e3", "q3", "", "B"),
		rec("s", "e4", "q4", "", "B"),
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.MergeCompact("answers", 584, segA) }()
	go func() { defer wg.Done(); d.MergeCompact("answers", 612, segB) }()
	wg.Wait()

	assertDistinct(t, d, "answers")
	if got := len(storeIdentities(t, d, "answers")); got != 4 {
		t.Fatalf("concurrent merges lost records: want 4, got %d", got)
	}
}

// TestLargeSegmentMerges asserts the merge handles oversized records (the
// working set is identity tuples plus a single-record buffer, never the full set).
func TestLargeSegmentMerges(t *testing.T) {
	d := newDataset(t)
	big := make([]byte, 512*1024)
	for i := range big {
		big[i] = 'x'
	}
	var recs [][]byte
	for i := 0; i < 20; i++ {
		recs = append(recs, rec("s", "e"+numStr(i), "q", "", string(big)))
	}
	seg := writeFinishedSegment(t, d, "answers", 584, recs)
	counts, err := d.MergeCompact("answers", 584, seg)
	if err != nil {
		t.Fatal(err)
	}
	if counts.New != 20 {
		t.Fatalf("expected 20 new, got %+v", counts)
	}
	if len(storeIdentities(t, d, "answers")) != 20 {
		t.Fatal("large-record store should hold 20 identities")
	}
}

func numStr(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
