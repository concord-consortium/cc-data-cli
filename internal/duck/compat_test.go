package duck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// copyV02Fixture copies the dataset written by cc-data 0.2.x into a fresh data root and
// sets each materialized input's modification time back to the one its manifest
// fingerprint recorded, since neither a checkout nor the copy keeps it.
func copyV02Fixture(t *testing.T) *dataset.Dataset {
	t.Helper()
	src := filepath.Join("..", "dataset", "testdata", "v0.2")
	d := dataset.Open(t.TempDir(), dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "v02"})
	err := filepath.WalkDir(src, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		dst := filepath.Join(d.Dir, rel)
		if e.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, mat := range m.Materialized {
		for rel, fp := range mat.Inputs {
			_, nanos, ok := strings.Cut(fp, "-")
			ns, perr := strconv.ParseInt(nanos, 10, 64)
			if !ok || perr != nil {
				t.Fatalf("fingerprint %q of %s", fp, rel)
			}
			mtime := time.Unix(0, ns)
			if err := os.Chtimes(d.Path(rel), mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
	}
	return d
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestV02DatasetOpensQueriesMergesAndReindexes(t *testing.T) {
	d := copyV02Fixture(t)
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	stale, err := staleMaterializedViews(d, m)
	if err != nil {
		t.Fatal(err)
	}
	// Only run_membership's definition differs from 0.2.x's, so only its copy reads stale.
	if !slices.Equal(stale, []string{"run_membership"}) {
		t.Fatalf("materialized views stale after the upgrade: %v, want only run_membership", stale)
	}
	show, err := ShowJSON(d, false)
	if err != nil {
		t.Fatal(err)
	}
	var staleWarnings []string
	for _, w := range show.Warnings {
		if strings.HasPrefix(w, "STALE_MATERIALIZED") {
			staleWarnings = append(staleWarnings, w)
		}
	}
	if len(staleWarnings) != 1 || !strings.Contains(staleWarnings[0], "run_membership") {
		t.Fatalf("dataset show stale warnings %v, want one for run_membership", staleWarnings)
	}

	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for view, want := range map[string]int{"answers": 3, "history": 2, "run_membership": 6, "answers_585": 2, "history_584": 2} {
		rows, err := e.Query(context.Background(), "SELECT count(*) FROM "+view)
		if err != nil {
			t.Fatalf("%s: %v", view, err)
		}
		var n int
		rows.Next()
		rows.Scan(&n)
		rows.Close()
		if n != want {
			t.Fatalf("%s has %d rows, want %d", view, n, want)
		}
	}
	e.Close()

	// Re-merging a run with the records it already holds, at the same stamp, is a no-op:
	// the store and the run's membership come out byte-identical. History covers the
	// optional history_id column and a value the 0.2.x encoder HTML-escaped.
	remergeIsNoOp(t, d, store.TypeAnswers, 585, 2)
	remergeIsNoOp(t, d, store.TypeHistory, 584, 2)

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m3, _ := d.ReadManifest()
	for typ, want := range map[string]int{store.TypeAnswers: 3, store.TypeHistory: 2} {
		if got := m3.Stores[typ].Count; got != want {
			t.Fatalf("reindexed %s count %d, want %d", typ, got, want)
		}
		if !mapsEqual(m3.Stores[typ].Columns, m.Stores[typ].Columns) {
			t.Fatalf("reindexed %s columns %v, want %v", typ, m3.Stores[typ].Columns, m.Stores[typ].Columns)
		}
	}
	if len(m3.Membership) != 3 {
		t.Fatalf("reindexed membership %v", m3.Membership)
	}
	if len(m.Downloads) != 3 || len(m3.Downloads) != len(m.Downloads) {
		t.Fatalf("reindexed downloads %+v, fixture %+v", m3.Downloads, m.Downloads)
	}
	for i, dl := range m.Downloads {
		got := m3.Downloads[i]
		if got.Type != dl.Type || got.RunID != dl.RunID || got.HistoryMode != dl.HistoryMode || !got.Complete {
			t.Fatalf("reindexed download %d = %+v, fixture %+v", i, got, dl)
		}
	}
}

func remergeIsNoOp(t *testing.T, d *dataset.Dataset, typ string, run, wantRecs int) {
	t.Helper()
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	storePath := d.Path(m.Stores[typ].File)
	oldStore := readFile(t, storePath)
	oldMembers := readFile(t, d.Path(m.Membership[dataset.MembershipKey(typ, run)].File))
	var recs [][]byte
	tag := []byte(fmt.Sprintf(`"_run_id":%d`, run))
	sc := bufio.NewScanner(bytes.NewReader(oldStore))
	for sc.Scan() {
		if bytes.Contains(sc.Bytes(), tag) {
			recs = append(recs, bytes.Clone(sc.Bytes()))
		}
	}
	if len(recs) != wantRecs {
		t.Fatalf("fixture holds %d run-%d %s records, want %d", len(recs), run, typ, wantRecs)
	}
	var stamp struct {
		FetchedAt time.Time `json:"_fetched_at"`
	}
	if err := json.Unmarshal(recs[0], &stamp); err != nil {
		t.Fatal(err)
	}
	seg := store.OpenSegment(d.Dir, typ, run)
	if err := seg.AppendPage(recs, stamp.FetchedAt, run); err != nil {
		t.Fatal(err)
	}
	seg.WriteCursor(&store.Cursor{Items: len(recs)})
	counts, err := d.MergeCompact(typ, run, seg)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Fetched != wantRecs {
		t.Fatalf("re-merge fetched %d records, want %d", counts.Fetched, wantRecs)
	}
	m2, _ := d.ReadManifest()
	key := dataset.MembershipKey(typ, run)
	if m2.Stores[typ].Version != m.Stores[typ].Version+1 || m2.Stores[typ].File == m.Stores[typ].File || m2.Membership[key].Version != m2.Stores[typ].Version {
		t.Fatalf("re-merge wrote no new version: store %+v, membership %+v", m2.Stores[typ], m2.Membership[key])
	}
	if got := readFile(t, d.Path(m2.Stores[typ].File)); !bytes.Equal(got, oldStore) {
		t.Fatalf("re-merged %s store differs:\n%s\nwant\n%s", typ, got, oldStore)
	}
	if got := readFile(t, d.Path(m2.Membership[dataset.MembershipKey(typ, run)].File)); !bytes.Equal(got, oldMembers) {
		t.Fatalf("re-merged %s membership differs:\n%s\nwant\n%s", typ, got, oldMembers)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
