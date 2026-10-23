package store

import (
	"fmt"
	"strconv"
	"strings"
)

// Store types.
const (
	TypeAnswers       = "answers"
	TypeHistory       = "history"
	TypeClueDocuments = "clue_documents"
	TypeClueHistory   = "clue_history"
	TypeClueContent   = "clue_content"
)

// Kind declares one store type.
type Kind struct {
	Name         string            // store type, file stem and view name
	Identity     []string          // identity columns, in key order
	Optional     map[string]bool   // identity columns written to membership only when non-empty
	Strict       bool              // an absent, null, non-string or empty identity value is an error
	Pinned       map[string]string // DuckDB types fixed regardless of observed values
	Route        string            // path under /api/v1/reports/<run>/
	DoubleDecode bool              // double-decode report_state (LARA answers and history)
}

// laraPins are the answers and history contract fields.
var laraPins = withStamps(map[string]string{
	"source_key":            TypeVARCHAR,
	"remote_endpoint":       TypeVARCHAR,
	"question_id":           TypeVARCHAR,
	"history_id":            TypeVARCHAR,
	"_decode_error":         TypeBOOLEAN,
	"report_state":          TypeJSON,
	"report_state_raw":      TypeVARCHAR,
	"interactive_state_raw": TypeVARCHAR,
	"answer":                TypeJSON,
})

// withStamps adds the _fetched_at and _run_id stamps every store record carries.
func withStamps(fields map[string]string) map[string]string {
	pins := map[string]string{"_fetched_at": TypeTIMESTAMP, "_run_id": TypeBIGINT}
	for c, t := range fields {
		pins[c] = t
	}
	return pins
}

var kinds = []Kind{
	{
		Name:         TypeAnswers,
		Identity:     []string{"source_key", "remote_endpoint", "question_id"},
		Pinned:       laraPins,
		Route:        "answers",
		DoubleDecode: true,
	},
	{
		Name:         TypeHistory,
		Identity:     []string{"source_key", "remote_endpoint", "question_id", "history_id"},
		Optional:     map[string]bool{"history_id": true},
		Pinned:       laraPins,
		Route:        "history",
		DoubleDecode: true,
	},
	// Fields the export builds have strict types; fields it passes through from CLUE are
	// VARCHAR or JSON, so one stray stored value cannot fail every query on the view.
	{
		Name:     TypeClueDocuments,
		Identity: []string{"doc_id"},
		Strict:   true,
		Route:    "clue/documents",
		Pinned: withStamps(map[string]string{
			"doc_id":             TypeVARCHAR,
			"uid":                TypeVARCHAR,
			"context_id":         TypeVARCHAR,
			"portal_class_id":    TypeBIGINT,
			"owner_kind":         TypeVARCHAR,
			"type":               TypeVARCHAR,
			"visibility":         TypeVARCHAR,
			"title":              TypeVARCHAR,
			"unit":               TypeVARCHAR,
			"investigation":      TypeVARCHAR,
			"problem":            TypeVARCHAR,
			"group_id":           TypeVARCHAR,
			"network":            TypeVARCHAR,
			"offering_id":        TypeBIGINT,
			"tools":              TypeJSON,
			"created_at":         TypeJSON,
			"metadata_sources":   TypeJSON,
			"firestore_metadata": TypeJSON,
			"rtdb_metadata":      TypeJSON,
		}),
	},
	{
		Name:     TypeClueHistory,
		Identity: []string{"doc_id", "entry_id"},
		Strict:   true,
		Route:    "clue/history",
		Pinned: withStamps(map[string]string{
			"doc_id":         TypeVARCHAR,
			"entry_id":       TypeVARCHAR,
			"idx":            TypeBIGINT,
			"prev_entry_id":  TypeVARCHAR,
			"server_created": TypeTIMESTAMP,
			"created":        TypeTIMESTAMP,
			"model":          TypeVARCHAR,
			"action_raw":     TypeVARCHAR,
			"undoable":       TypeJSON,
			"state":          TypeVARCHAR,
			"n_records":      TypeBIGINT,
			"is_revert":      TypeBOOLEAN,
			"entry_uid":      TypeVARCHAR,
			"parse_ok":       TypeBOOLEAN,
			"entry_json":     TypeVARCHAR,
		}),
	},
	{
		Name:     TypeClueContent,
		Identity: []string{"doc_id"},
		Strict:   true,
		Route:    "clue/content",
		Pinned: withStamps(map[string]string{
			"doc_id":                TypeVARCHAR,
			"uid":                   TypeVARCHAR,
			"context_id":            TypeVARCHAR,
			"found":                 TypeBOOLEAN,
			"type":                  TypeVARCHAR,
			"version":               TypeVARCHAR,
			"change_count":          TypeJSON,
			"last_history_entry_id": TypeVARCHAR,
			"self":                  TypeJSON,
			"content_json":          TypeVARCHAR,
		}),
	},
}

// Kinds returns the registered store types in registry order.
func Kinds() []Kind {
	out := make([]Kind, len(kinds))
	copy(out, kinds)
	return out
}

// KindOf returns the registered store type with the given name.
func KindOf(name string) (Kind, bool) {
	for _, k := range kinds {
		if k.Name == name {
			return k, true
		}
	}
	return Kind{}, false
}

// MustKind returns the registered store type with the given name and panics on an
// unknown one, which is a programming error.
func MustKind(name string) Kind {
	k, ok := KindOf(name)
	if !ok {
		panic("store: unknown store type " + name)
	}
	return k
}

// Names returns the registered store type names in registry order.
func Names() []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.Name
	}
	return out
}

// IdentityColumns returns every registered identity column once, in registry order.
func IdentityColumns() []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range kinds {
		for _, c := range k.Identity {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// Identity is a record's identity: its values for the kind's identity columns, in order.
type Identity []string

// Key returns the length-prefixed encoded key of an identity. The encoding
// (<decimal byte length>:<raw bytes> per field, concatenated) is injective for
// arbitrary field contents, and byte comparison of keys gives a total order. It
// never leaves memory; stores and query surfaces carry the raw fields.
func (k Kind) Key(id Identity) string {
	return encodeFields(id...)
}

// Decode is the inverse of Key. It panics only on a key this package did not encode.
func (k Kind) Decode(key string) Identity {
	id := make(Identity, 0, len(k.Identity))
	rest := key
	for len(rest) > 0 {
		i := strings.IndexByte(rest, ':')
		n, err := strconv.Atoi(rest[:max(i, 0)])
		if i < 0 || err != nil || n < 0 || i+1+n > len(rest) {
			panic(fmt.Sprintf("store: malformed %s key %q", k.Name, key))
		}
		id = append(id, rest[i+1:i+1+n])
		rest = rest[i+1+n:]
	}
	if len(id) != len(k.Identity) {
		panic(fmt.Sprintf("store: %s key %q has %d fields, want %d", k.Name, key, len(id), len(k.Identity)))
	}
	return id
}

// Field returns the identity's value for an identity column, or "" when col is not
// one of the kind's identity columns.
func (k Kind) Field(id Identity, col string) string {
	for i, c := range k.Identity {
		if c == col && i < len(id) {
			return id[i]
		}
	}
	return ""
}

func encodeFields(fields ...string) string {
	n := 0
	for _, f := range fields {
		n += len(strconv.Itoa(len(f))) + 1 + len(f)
	}
	var b strings.Builder
	b.Grow(n)
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}
