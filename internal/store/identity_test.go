package store

import "testing"

func TestIdentityEncodingInjective(t *testing.T) {
	// Distinct tuples must encode distinctly, including hostile field contents.
	pairs := [][2]Identity{
		{{SourceKey: "a", RemoteEndpoint: "b", QuestionID: "c"}, {SourceKey: "ab", RemoteEndpoint: "", QuestionID: "c"}},
		{{SourceKey: "a:b", RemoteEndpoint: "c", QuestionID: "d"}, {SourceKey: "a", RemoteEndpoint: "b:c", QuestionID: "d"}},
		{{SourceKey: "a\x1fb", RemoteEndpoint: "c", QuestionID: "d"}, {SourceKey: "a", RemoteEndpoint: "\x1fb", QuestionID: "cd"}},
		{{SourceKey: "", RemoteEndpoint: "", QuestionID: ""}, {SourceKey: "", RemoteEndpoint: "", QuestionID: "x"}},
	}
	for i, p := range pairs {
		if p[0].Key(TypeAnswers) == p[1].Key(TypeAnswers) {
			t.Fatalf("pair %d collided: %q", i, p[0].Key(TypeAnswers))
		}
	}
}

func TestIdentityHistoryIncludesHistoryID(t *testing.T) {
	a := Identity{SourceKey: "s", RemoteEndpoint: "r", QuestionID: "q", HistoryID: "h1"}
	b := Identity{SourceKey: "s", RemoteEndpoint: "r", QuestionID: "q", HistoryID: "h2"}
	if a.Key(TypeHistory) == b.Key(TypeHistory) {
		t.Fatal("history identities differing only in history_id must not collide")
	}
	// For answers the history_id is ignored.
	if a.Key(TypeAnswers) != b.Key(TypeAnswers) {
		t.Fatal("answers key must ignore history_id")
	}
}

func TestIdentityFromRecord(t *testing.T) {
	rec := []byte(`{"source_key":"s","remote_endpoint":"r","question_id":"q","history_id":"h","other":1}`)
	id, err := IdentityFromRecord(TypeHistory, rec)
	if err != nil {
		t.Fatal(err)
	}
	if id.SourceKey != "s" || id.RemoteEndpoint != "r" || id.QuestionID != "q" || id.HistoryID != "h" {
		t.Fatalf("identity = %+v", id)
	}
	idA, _ := IdentityFromRecord(TypeAnswers, rec)
	if idA.HistoryID != "" {
		t.Fatalf("answers identity must not carry history_id: %+v", idA)
	}
}
