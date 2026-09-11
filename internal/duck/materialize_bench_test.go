package duck

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
)

// timingEnvVar gates the timing assertions. CI runs plain `go test ./...` with no
// -short convention, and this fixture is hundreds of megabytes of generated
// JSONL, so it is opt-in rather than skipped by default.
const timingEnvVar = "CC_DATA_MATERIALIZE_TIMING"

// timingRows is large enough that the query cost dominates the fixed per-query
// overhead. At this size the fixture is roughly 540MB of JSONL.
const timingRows = 1_000_000

// TestMaterializeTiming asserts the shape of the speedup, not a single number.
// Column pruning is the mechanism: an aggregate over two narrow columns is the
// case materializing exists for, while a high-cardinality aggregate is bounded by
// the aggregation rather than the scan and is only required not to get worse.
//
// Run with: CC_DATA_MATERIALIZE_TIMING=1 go test ./internal/duck/ -run Timing -timeout 30m
func TestMaterializeTiming(t *testing.T) {
	if os.Getenv(timingEnvVar) == "" {
		t.Skipf("set %s=1 to run the timing assertions (they generate a multi-hundred-MB fixture)", timingEnvVar)
	}
	d := timingFixture(t, timingRows)

	raw := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	prunedRaw := timeQuery(t, raw, prunedAggregate)
	wideRaw := timeQuery(t, raw, highCardinalityAggregate)

	start := time.Now()
	res, err := Materialize(context.Background(), d, MaterializeOptions{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(res.Written, "history") {
		t.Fatalf("the history view was not materialized: %+v", res)
	}
	t.Logf("materialized %d rows in %s", timingRows, time.Since(start).Round(time.Millisecond))

	mat := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	prunedMat := timeQuery(t, mat, prunedAggregate)
	wideMat := timeQuery(t, mat, highCardinalityAggregate)

	t.Logf("column-pruned aggregate: raw %s, materialized %s (%.1fx)",
		prunedRaw.Round(time.Millisecond), prunedMat.Round(time.Millisecond),
		float64(prunedRaw)/float64(prunedMat))
	t.Logf("high-cardinality aggregate: raw %s, materialized %s (%.1fx)",
		wideRaw.Round(time.Millisecond), wideMat.Round(time.Millisecond),
		float64(wideRaw)/float64(wideMat))

	// Measured at ~60x, so the threshold carries six times the headroom rather
	// than being tuned until it passed.
	if prunedMat*10 > prunedRaw {
		t.Errorf("column-pruned aggregate should be at least 10x faster materialized: raw %s, materialized %s",
			prunedRaw, prunedMat)
	}
	// Not-worse rather than faster: this shape is bounded by the aggregation, and
	// the check exists to catch a change that made materializing a pessimization.
	if wideMat > wideRaw {
		t.Errorf("high-cardinality aggregate got slower materialized: raw %s, materialized %s", wideRaw, wideMat)
	}
}

const (
	prunedAggregate          = `SELECT question_id, count(*) FROM history GROUP BY question_id`
	highCardinalityAggregate = `SELECT question_id, count(DISTINCT remote_endpoint) FROM history GROUP BY question_id`
)

func timeQuery(t *testing.T, e *Engine, query string) time.Duration {
	t.Helper()
	start := time.Now()
	rows, err := e.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

// timingFixture writes a history store directly, bypassing the segment merge,
// because the merge's identity pass is not what is being measured.
//
// The payloads have to vary. A first attempt used an identical blob per row: ZSTD
// dictionary encoding took the Parquet to 0.9% of the JSONL and the measured
// speedup collapses to single digits, because the cost moves entirely into the
// aggregation. A generator emitting a constant blob measures compression, not
// materialization.
func timingFixture(t *testing.T, rows int) *dataset.Dataset {
	t.Helper()
	root := t.TempDir()
	d, err := dataset.Create(root, dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "timing"}, "")
	if err != nil {
		t.Fatal(err)
	}
	file := "history.v1.jsonl"
	f, err := os.Create(d.Path(file))
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	w := bufio.NewWriterSize(f, 1<<20)
	for i := 0; i < rows; i++ {
		rec := map[string]any{
			"source_key":      fmt.Sprintf("activity-%d", i%50),
			"remote_endpoint": fmt.Sprintf("https://portal/dataservice/external_activity_data/%d", i%20000),
			"question_id":     fmt.Sprintf("q%d", i%400),
			"history_id":      fmt.Sprintf("h%d", i),
			"state":           randomState(rng, i),
			"_fetched_at":     "2026-09-01T00:00:00Z",
			"_run_id":         584,
		}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(line)
		w.Write([]byte("\n"))
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if fi, err := os.Stat(d.Path(file)); err == nil {
		t.Logf("fixture: %d rows, %.0f MB of JSONL", rows, float64(fi.Size())/(1<<20))
	}

	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	m.Stores["history"] = dataset.Store{
		File: file, Version: 1, Count: rows,
		Columns: map[string]string{
			"source_key": "VARCHAR", "remote_endpoint": "VARCHAR", "question_id": "VARCHAR",
			"history_id": "VARCHAR", "state": "VARCHAR", "_fetched_at": "TIMESTAMP", "_run_id": "BIGINT",
		},
	}
	if err := writeManifestUnderLock(d, m); err != nil {
		t.Fatal(err)
	}
	return d
}

// randomState builds a state blob whose values differ per row, so the Parquet
// cannot dictionary-encode the column away.
func randomState(rng *rand.Rand, i int) string {
	var b strings.Builder
	b.WriteString(`{"version":1,"cells":[`)
	for j := 0; j < 5; j++ {
		if j > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"c%d-%d","v":%0.6f,"label":"%x"}`, i%977, j, rng.Float64(), rng.Uint64())
	}
	b.WriteString(`]}`)
	return b.String()
}
