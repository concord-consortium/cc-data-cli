package duck

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"text/tabwriter"
	"time"
)

// Formats supported by query.
const (
	FormatTable = "table"
	FormatCSV   = "csv"
	FormatJSON  = "json"
	FormatJSONL = "jsonl"
)

// RenderRows writes rows to w in the chosen format.
func RenderRows(w io.Writer, rows *sql.Rows, format string) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	switch format {
	case FormatCSV:
		return renderCSV(w, rows, cols)
	case FormatJSON:
		return renderJSON(w, rows, cols, true)
	case FormatJSONL:
		return renderJSON(w, rows, cols, false)
	default:
		return renderTable(w, rows, cols)
	}
}

func scanRow(rows *sql.Rows, n int) ([]any, error) {
	raw := make([]any, n)
	ptrs := make([]any, n)
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	return raw, nil
}

// convert maps a driver value to a JSON-friendly Go value: timestamps render
// RFC3339, and out-of-float64-range HUGEINT/DECIMAL render as strings.
func convert(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case []byte:
		return string(t)
	case *big.Int:
		if t.IsInt64() {
			return t.Int64()
		}
		return t.String()
	case *big.Float:
		f, _ := t.Float64()
		if t.IsInt() || withinFloat64(f) {
			return f
		}
		return t.Text('f', -1)
	case float64:
		if math.IsInf(t, 0) || math.IsNaN(t) {
			return fmt.Sprintf("%v", t)
		}
		return t
	default:
		return v
	}
}

func withinFloat64(f float64) bool {
	return !math.IsInf(f, 0) && !math.IsNaN(f)
}

func cellString(v any) string {
	c := convert(v)
	if c == nil {
		return ""
	}
	switch t := c.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func renderTable(w io.Writer, rows *sql.Rows, cols []string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for i, c := range cols {
		if i > 0 {
			fmt.Fprint(tw, "\t")
		}
		fmt.Fprint(tw, c)
	}
	fmt.Fprintln(tw)
	for rows.Next() {
		vals, err := scanRow(rows, len(cols))
		if err != nil {
			return err
		}
		for i, v := range vals {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, cellString(v))
		}
		fmt.Fprintln(tw)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tw.Flush()
}

func renderCSV(w io.Writer, rows *sql.Rows, cols []string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(cols); err != nil {
		return err
	}
	for rows.Next() {
		vals, err := scanRow(rows, len(cols))
		if err != nil {
			return err
		}
		rec := make([]string, len(vals))
		for i, v := range vals {
			rec[i] = cellString(v)
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

func renderJSON(w io.Writer, rows *sql.Rows, cols []string, array bool) error {
	enc := json.NewEncoder(w)
	if array {
		if _, err := io.WriteString(w, "["); err != nil {
			return err
		}
	}
	first := true
	for rows.Next() {
		vals, err := scanRow(rows, len(cols))
		if err != nil {
			return err
		}
		obj := make(map[string]any, len(cols))
		for i, c := range cols {
			obj[c] = convert(vals[i])
		}
		if array {
			if !first {
				io.WriteString(w, ",")
			}
			b, err := json.Marshal(obj)
			if err != nil {
				return err
			}
			w.Write(b)
		} else {
			if err := enc.Encode(obj); err != nil {
				return err
			}
		}
		first = false
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if array {
		if _, err := io.WriteString(w, "]\n"); err != nil {
			return err
		}
	}
	return nil
}
