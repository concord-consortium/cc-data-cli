package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

var answersKind, historyKind = MustKind(TypeAnswers), MustKind(TypeHistory)

func TestIdentityEncodingInjective(t *testing.T) {
	// Distinct tuples must encode distinctly, including hostile field contents.
	pairs := [][2]Identity{
		{{"a", "b", "c"}, {"ab", "", "c"}},
		{{"a:b", "c", "d"}, {"a", "b:c", "d"}},
		{{"a\x1fb", "c", "d"}, {"a", "\x1fb", "cd"}},
		{{"", "", ""}, {"", "", "x"}},
	}
	for i, p := range pairs {
		if answersKind.Key(p[0]) == answersKind.Key(p[1]) {
			t.Fatalf("pair %d collided: %q", i, answersKind.Key(p[0]))
		}
	}
}

func TestIdentityHistoryIncludesHistoryID(t *testing.T) {
	a := Identity{"s", "r", "q", "h1"}
	b := Identity{"s", "r", "q", "h2"}
	if historyKind.Key(a) == historyKind.Key(b) {
		t.Fatal("history identities differing only in history_id must not collide")
	}
}

func TestIdentityFromRecord(t *testing.T) {
	rec := []byte(`{"source_key":"s","remote_endpoint":"r","question_id":"q","history_id":"h","other":1}`)
	id, err := IdentityFromRecord(historyKind, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(id, Identity{"s", "r", "q", "h"}) {
		t.Fatalf("identity = %q", id)
	}
	idA, _ := IdentityFromRecord(answersKind, rec)
	if !slices.Equal(idA, Identity{"s", "r", "q"}) {
		t.Fatalf("answers identity must not carry history_id: %q", idA)
	}
}

func TestIdentityFromRecordReadsTopLevelKeysOnly(t *testing.T) {
	rec := []byte(`{"_fetched_at":"t","history_id":"h","report_state":{"interactiveState":{"history_id":"decoy","question_id":"decoy","source_key":"decoy"}},"question_id":"q","remote_endpoint":"r","source_key":"real.host"}`)
	for k, want := range map[string]Identity{TypeHistory: {"real.host", "r", "q", "h"}, TypeAnswers: {"real.host", "r", "q"}} {
		id, err := IdentityFromRecord(MustKind(k), rec)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(id, want) {
			t.Fatalf("%s identity = %q, want the top-level values %q", k, id, want)
		}
	}
	owner := Kind{Name: "owner", Identity: []string{"context_id", "uid"}, Strict: true}
	doc := []byte(`{"doc_id":"d","firestore_metadata":{"context_id":"decoy","uid":"999"},"context_id":"c","uid":"123"}`)
	got, err := IdentityFromRecord(owner, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, Identity{"c", "123"}) {
		t.Fatalf("owner = %q, want the top-level values", got)
	}
}

func TestIdentityFromRecordLenientAndStrict(t *testing.T) {
	lenient, err := IdentityFromRecord(answersKind, []byte(`{"source_key":null,"question_id":"q"}`))
	if err != nil || !slices.Equal(lenient, Identity{"", "", "q"}) {
		t.Fatalf("lenient = %q, %v; absent and null read as empty", lenient, err)
	}
	if _, err := IdentityFromRecord(answersKind, []byte(`{"source_key":1}`)); err == nil {
		t.Fatal("a non-string identity value must be an error")
	}
	strict := Kind{Name: "strict", Identity: []string{"doc_id", "entry_id"}, Strict: true}
	for rec, want := range map[string]string{
		`{"doc_id":"d"}`:                 "entry_id is missing",
		`{"doc_id":null,"entry_id":"e"}`: "doc_id is null",
		`{"doc_id":7,"entry_id":"e"}`:    "doc_id is not a string",
		`{"doc_id":"d","entry_id":""}`:   "entry_id is empty",
	} {
		_, err := IdentityFromRecord(strict, []byte(rec))
		var ie *IdentityError
		if !errors.As(err, &ie) || ie.Error() != want {
			t.Fatalf("%s: err = %v, want %q", rec, err, want)
		}
	}
}

var hostile = []string{"", "a", "<&>", `q"uote\`, "\x1f\x00", "héllo 🎉", "\xff\xfe", "\u2028\u2029", "12:3"}

func TestKeyDecodeRoundTrips(t *testing.T) {
	for _, a := range hostile {
		for _, b := range hostile {
			id := Identity{a, b, a + b, b}
			if got := historyKind.Decode(historyKind.Key(id)); !slices.Equal(got, id) {
				t.Fatalf("Decode(Key(%q)) = %q", id, got)
			}
		}
	}
}

// legacyIdentity is the struct earlier releases encoded membership lines from.
type legacyIdentity struct {
	SourceKey      string `json:"source_key"`
	RemoteEndpoint string `json:"remote_endpoint"`
	QuestionID     string `json:"question_id"`
	HistoryID      string `json:"history_id,omitempty"`
}

func TestMembershipLineMatchesTheLegacyEncoder(t *testing.T) {
	for _, a := range hostile {
		for _, b := range hostile {
			for _, k := range []Kind{answersKind, historyKind} {
				legacy := legacyIdentity{SourceKey: a, RemoteEndpoint: b, QuestionID: a + b}
				id := Identity{a, b, a + b}
				if k.Name == TypeHistory {
					legacy.HistoryID = b
					id = append(id, b)
				}
				var want, got bytes.Buffer
				json.NewEncoder(&want).Encode(legacy)
				appendMembershipLine(&got, k, columnNames(k), id)
				if !bytes.Equal(want.Bytes(), got.Bytes()) {
					t.Fatalf("%s line differs:\n%q\n%q", k.Name, want.Bytes(), got.Bytes())
				}
			}
		}
	}
}

func TestStreamedMembershipMatchesTheLegacyEncoder(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	var want []legacyIdentity
	// Invalid UTF-8 is left out: the segment stores it as U+FFFD before membership sees it.
	valid := slices.DeleteFunc(slices.Clone(hostile), func(s string) bool { return !utf8.ValidString(s) })
	for i, a := range valid {
		for j, b := range valid {
			rec, _ := json.Marshal(map[string]any{"source_key": a, "remote_endpoint": b, "question_id": fmt.Sprint(i, ".", j), "history_id": b})
			lines = append(lines, string(rec))
			want = append(want, legacyIdentity{SourceKey: a, RemoteEndpoint: b, QuestionID: fmt.Sprint(i, ".", j), HistoryID: b})
		}
	}
	idx := writeSegment(t, dir, TypeHistory, 1, lines...)
	path := filepath.Join(dir, "members.jsonl")
	if err := idx.WriteMembership(path); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(want, func(x, y legacyIdentity) int {
		return strings.Compare(historyKind.Key(Identity{x.SourceKey, x.RemoteEndpoint, x.QuestionID, x.HistoryID}),
			historyKind.Key(Identity{y.SourceKey, y.RemoteEndpoint, y.QuestionID, y.HistoryID}))
	})
	var legacy bytes.Buffer
	enc := json.NewEncoder(&legacy)
	for _, id := range want {
		enc.Encode(id)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, legacy.Bytes()) {
		t.Fatalf("streamed membership differs from the legacy encoder:\n%s\nwant\n%s", got, legacy.Bytes())
	}
}

func TestMembershipReaderRefusesKeysOutOfOrder(t *testing.T) {
	b := `{"source_key":"s","remote_endpoint":"r","question_id":"b"}`
	a := `{"source_key":"s","remote_endpoint":"r","question_id":"a"}`
	for name, body := range map[string]string{"descending": b + "\n" + a + "\n", "repeated": b + "\n" + b + "\n"} {
		path := filepath.Join(t.TempDir(), "members.jsonl")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := OpenMembership(path, answersKind)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.Next(); err != nil {
			t.Fatalf("%s: first key: %v", name, err)
		}
		if _, _, err := r.Next(); !errors.Is(err, ErrMembershipOrder) {
			t.Fatalf("%s: err = %v, want a membership order error", name, err)
		}
		r.Close()
	}
}
