package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

// IdentityError is a record whose identity column is unusable for its kind.
type IdentityError struct {
	Column string
	Reason string // "is missing", "is null", "is not a string", "is empty"
}

func (e *IdentityError) Error() string { return e.Column + " " + e.Reason }

// IdentityFromRecord reads a record's identity for its kind. It reads top-level keys
// only, skipping every nested value whole, and stops once each identity column has
// been seen. For a non-strict kind an absent or null value is "", and a non-string is
// an error; a strict kind refuses all four of absent, null, non-string and empty.
func IdentityFromRecord(k Kind, rec []byte) (Identity, error) {
	id := make(Identity, len(k.Identity))
	seen := make([]bool, len(k.Identity))
	remaining := len(k.Identity)

	dec := json.NewDecoder(bytes.NewReader(rec))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("record is not a JSON object")
	}
	for remaining > 0 && dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		i := slices.Index(k.Identity, key)
		if i < 0 || seen[i] {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		seen[i] = true
		remaining--
		if bytes.Equal(raw, []byte("null")) {
			if k.Strict {
				return nil, &IdentityError{Column: key, Reason: "is null"}
			}
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, &IdentityError{Column: key, Reason: "is not a string"}
		}
		if s == "" && k.Strict {
			return nil, &IdentityError{Column: key, Reason: "is empty"}
		}
		id[i] = s
	}
	if k.Strict {
		for i, ok := range seen {
			if !ok {
				return nil, &IdentityError{Column: k.Identity[i], Reason: "is missing"}
			}
		}
	}
	return id, nil
}
