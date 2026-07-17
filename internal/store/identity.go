package store

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Store types.
const (
	TypeAnswers = "answers"
	TypeHistory = "history"
)

// Identity is a record's identity tuple. HistoryID is empty for answers.
type Identity struct {
	SourceKey      string `json:"source_key"`
	RemoteEndpoint string `json:"remote_endpoint"`
	QuestionID     string `json:"question_id"`
	HistoryID      string `json:"history_id,omitempty"`
}

// Key returns the length-prefixed encoded key for a type. The encoding
// (<decimal byte length>:<raw bytes> per field, concatenated) is injective for
// arbitrary field contents, and byte comparison of keys gives a total order. It
// never leaves memory; stores and query surfaces carry the raw fields.
func (id Identity) Key(typ string) string {
	if typ == TypeHistory {
		return encodeFields(id.SourceKey, id.RemoteEndpoint, id.QuestionID, id.HistoryID)
	}
	return encodeFields(id.SourceKey, id.RemoteEndpoint, id.QuestionID)
}

func encodeFields(fields ...string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}

// IdentityFromRecord reads a record's identity fields for a type.
func IdentityFromRecord(typ string, rec []byte) (Identity, error) {
	var fields struct {
		SourceKey      string `json:"source_key"`
		RemoteEndpoint string `json:"remote_endpoint"`
		QuestionID     string `json:"question_id"`
		HistoryID      string `json:"history_id"`
	}
	if err := json.Unmarshal(rec, &fields); err != nil {
		return Identity{}, err
	}
	id := Identity{
		SourceKey:      fields.SourceKey,
		RemoteEndpoint: fields.RemoteEndpoint,
		QuestionID:     fields.QuestionID,
	}
	if typ == TypeHistory {
		id.HistoryID = fields.HistoryID
	}
	return id, nil
}
