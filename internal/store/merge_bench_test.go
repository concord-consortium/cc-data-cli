package store_test

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

const memoryTestIdentities = 1_000_000

// writeHistoryShapedSegment writes n records whose identity is two 20-character ids, as
// in a CLUE history key, carried in the history kind's source_key and question_id.
func writeHistoryShapedSegment(t *testing.T, dir string, run, n int) *store.Segment {
	t.Helper()
	seg := store.OpenSegment(dir, store.TypeHistory, run)
	if err := os.MkdirAll(filepath.Dir(seg.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(seg.Path())
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	for i := 0; i < n; i++ {
		fmt.Fprintf(w, `{"_fetched_at":"2026-10-23T00:00:00Z","_run_id":%d,"entry_json":"{}","question_id":"Ab3dEf6hIj9lMn%06d","source_key":"-NabcdefGHIJ%08d"}`+"\n", run, i, i/1365)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := seg.WriteCursor(&store.Cursor{Pages: 1, Items: n}); err != nil {
		t.Fatal(err)
	}
	return seg
}

func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

func TestMergeMemoryPerIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a million-record segment")
	}

	t.Run("index", func(t *testing.T) {
		seg := writeHistoryShapedSegment(t, t.TempDir(), 1, memoryTestIdentities)
		before := heapInUse()
		idx, err := seg.BuildIndex()
		if err != nil {
			t.Fatal(err)
		}
		perIdentity := float64(heapInUse()-before) / memoryTestIdentities
		runtime.KeepAlive(idx)
		idx.Close()
		if perIdentity > 120 {
			t.Fatalf("index holds %.0f bytes per identity, want at most 120", perIdentity)
		}
		t.Logf("index: %.0f bytes per identity", perIdentity)
	})

	t.Run("covered walk", func(t *testing.T) {
		d, err := dataset.Create(t.TempDir(), dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "mem"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.MergeCompact(store.TypeHistory, 1, writeHistoryShapedSegment(t, d.Dir, 1, memoryTestIdentities)); err != nil {
			t.Fatal(err)
		}
		small := writeHistoryShapedSegment(t, d.Dir, 2, 1)
		base := heapInUse()
		var peak atomic.Uint64
		done := make(chan struct{})
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			var m runtime.MemStats
			for {
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
				select {
				case <-done:
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		}()
		counts, err := d.MergeCompact(store.TypeHistory, 2, small)
		close(done)
		<-sampled
		if err != nil {
			t.Fatal(err)
		}
		if m, _ := d.ReadManifest(); counts.Removed != 0 || m.Stores[store.TypeHistory].Count != memoryTestIdentities {
			t.Fatalf("the merge dropped records run 1 holds: %+v, store count %d", counts, m.Stores[store.TypeHistory].Count)
		}
		grew := float64(peak.Load()-min(peak.Load(), base)) / (1 << 20)
		if grew > 32 {
			t.Fatalf("a 1-record merge over a 1M-record store held by another run peaked %.1f MB over its base, want under 32", grew)
		}
		t.Logf("covered walk: peak %.1f MB over base", grew)
	})
}
