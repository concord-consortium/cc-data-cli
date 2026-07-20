package store

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// DuckDB column type names.
const (
	TypeBIGINT    = "BIGINT"
	TypeDOUBLE    = "DOUBLE"
	TypeBOOLEAN   = "BOOLEAN"
	TypeVARCHAR   = "VARCHAR"
	TypeJSON      = "JSON"
	TypeTIMESTAMP = "TIMESTAMP"
)

// pinnedColumns are the contract fields whose DuckDB type is fixed regardless of
// observed values.
var pinnedColumns = map[string]string{
	"source_key":            TypeVARCHAR,
	"remote_endpoint":       TypeVARCHAR,
	"question_id":           TypeVARCHAR,
	"history_id":            TypeVARCHAR,
	"_fetched_at":           TypeTIMESTAMP,
	"_run_id":               TypeBIGINT,
	"_decode_error":         TypeBOOLEAN,
	"report_state":          TypeJSON,
	"report_state_raw":      TypeVARCHAR,
	"interactive_state_raw": TypeVARCHAR,
	"answer":                TypeJSON,
}

// ColumnDetector accumulates a DuckDB column map by widening over every observed
// record. Contract fields are pinned; every other field widens over its values.
type ColumnDetector struct {
	types map[string]string
}

// NewColumnDetector returns a detector seeded with the pinned contract columns.
func NewColumnDetector() *ColumnDetector {
	types := make(map[string]string, len(pinnedColumns))
	for k, v := range pinnedColumns {
		types[k] = v
	}
	return &ColumnDetector{types: types}
}

// Observe widens the column map over one record's fields.
func (d *ColumnDetector) Observe(rec []byte) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rec, &obj); err != nil {
		return
	}
	for k, v := range obj {
		if _, pinned := pinnedColumns[k]; pinned {
			continue
		}
		d.types[k] = widen(d.types[k], valueType(v))
	}
}

// Map returns a copy of the accumulated column map, dropping fields that were
// only ever null.
func (d *ColumnDetector) Map() map[string]string {
	out := make(map[string]string, len(d.types))
	for k, v := range d.types {
		if v == "" {
			out[k] = TypeVARCHAR
			continue
		}
		out[k] = v
	}
	return out
}

// valueType classifies a JSON value into a DuckDB column category; "" for null.
func valueType(v json.RawMessage) string {
	t := bytes.TrimSpace(v)
	if len(t) == 0 {
		return ""
	}
	switch t[0] {
	case '{', '[':
		return TypeJSON
	case '"':
		return TypeVARCHAR
	case 't', 'f':
		return TypeBOOLEAN
	case 'n':
		return "" // null does not constrain the type
	default:
		if bytes.ContainsAny(t, ".eE") {
			return TypeDOUBLE
		}
		// An integer literal outside int64 range cannot be a DuckDB BIGINT and
		// would fail at query time; fall through to DOUBLE (mirrors csvValueType).
		if _, err := strconv.ParseInt(string(t), 10, 64); err != nil {
			return TypeDOUBLE
		}
		return TypeBIGINT
	}
}

// widen combines a current type with a newly observed one; incompatible scalar
// categories or any object/array collapse to JSON.
func widen(cur, next string) string {
	if next == "" {
		return cur
	}
	if cur == "" {
		return next
	}
	if cur == next {
		return cur
	}
	if (cur == TypeBIGINT && next == TypeDOUBLE) || (cur == TypeDOUBLE && next == TypeBIGINT) {
		return TypeDOUBLE
	}
	return TypeJSON
}
