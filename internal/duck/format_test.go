package duck

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func formatQuery(t *testing.T, e *Engine, query, format string) string {
	t.Helper()
	rows, err := e.Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var buf bytes.Buffer
	if err := RenderRows(&buf, rows, format); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestRenderFormats(t *testing.T) {
	d := newDS(t, "ds")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)

	q := "SELECT 1 AS n, 'hi' AS s, TIMESTAMP '2026-07-17 12:00:00' AS t"

	// JSON array.
	out := formatQuery(t, e, q, FormatJSON)
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("json not valid: %s", out)
	}
	if len(arr) != 1 || arr[0]["s"] != "hi" {
		t.Fatalf("json = %v", arr)
	}
	if arr[0]["t"] != "2026-07-17T12:00:00Z" {
		t.Fatalf("timestamp should render RFC3339, got %v", arr[0]["t"])
	}

	// JSONL: one object per line.
	out = formatQuery(t, e, q, FormatJSONL)
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("single-row jsonl should be one line: %q", out)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
		t.Fatalf("jsonl not valid: %s", out)
	}

	// CSV.
	out = formatQuery(t, e, q, FormatCSV)
	if !strings.HasPrefix(out, "n,s,t\n") {
		t.Fatalf("csv header wrong: %q", out)
	}

	// Table.
	out = formatQuery(t, e, q, FormatTable)
	if !strings.Contains(out, "hi") {
		t.Fatalf("table missing data: %q", out)
	}
}

func TestRenderHugeInt(t *testing.T) {
	d := newDS(t, "ds")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	// A HUGEINT beyond int64/float64 exact range should render as a string.
	out := formatQuery(t, e, "SELECT 170141183460469231731687303715884105727::HUGEINT AS big", FormatJSON)
	var arr []map[string]any
	json.Unmarshal([]byte(out), &arr)
	if _, ok := arr[0]["big"].(string); !ok {
		t.Fatalf("huge int should render as string, got %T (%v)", arr[0]["big"], arr[0]["big"])
	}
}

func TestRenderNull(t *testing.T) {
	d := newDS(t, "ds")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	out := formatQuery(t, e, "SELECT CAST(NULL AS VARCHAR) AS x", FormatCSV)
	if out != "x\n\n" {
		t.Fatalf("null CSV = %q", out)
	}
}

// TestCSVCleanWithStderrDiscarded mirrors the acceptance: format csv output is
// clean CSV (the renderer writes only rows, never prose).
func TestCSVCleanWithStderrDiscarded(t *testing.T) {
	d := newDS(t, "ds")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	var buf bytes.Buffer
	rows, _ := e.Query(context.Background(), "SELECT 1 AS a UNION ALL SELECT 2 ORDER BY a")
	defer rows.Close()
	RenderRows(&buf, rows, FormatCSV)
	if buf.String() != "a\n1\n2\n" {
		t.Fatalf("csv = %q", buf.String())
	}
	_ = io.Discard
}
