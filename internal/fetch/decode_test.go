package fetch

import (
	"encoding/json"
	"testing"
)

func decodeToMap(t *testing.T, in string) map[string]json.RawMessage {
	t.Helper()
	out := doubleDecode([]byte(in))
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output not JSON: %s", out)
	}
	return m
}

func TestDoubleDecodeSuccess(t *testing.T) {
	// report_state is a JSON string whose interactiveState is itself a JSON string.
	inner := `{"answer":"42"}`
	state := `{"interactiveState":` + jsonString(inner) + `}`
	rec := `{"question_id":"q1","report_state":` + jsonString(state) + `}`

	m := decodeToMap(t, rec)
	// report_state should now be a real object.
	var stateObj map[string]json.RawMessage
	if err := json.Unmarshal(m["report_state"], &stateObj); err != nil {
		t.Fatalf("report_state not an object: %s", m["report_state"])
	}
	var innerObj map[string]any
	if err := json.Unmarshal(stateObj["interactiveState"], &innerObj); err != nil {
		t.Fatalf("interactiveState not an object: %s", stateObj["interactiveState"])
	}
	if innerObj["answer"] != "42" {
		t.Fatalf("inner state wrong: %v", innerObj)
	}
	if _, hasErr := m["_decode_error"]; hasErr {
		t.Fatal("no decode error expected")
	}
}

func TestDoubleDecodeOuterFailure(t *testing.T) {
	// report_state is a JSON string that is not valid JSON inside.
	rec := `{"question_id":"q1","report_state":` + jsonString("not json{") + `}`
	m := decodeToMap(t, rec)
	if string(m["report_state"]) != "null" {
		t.Fatalf("report_state should be null on failure: %s", m["report_state"])
	}
	if _, ok := m["report_state_raw"]; !ok {
		t.Fatal("report_state_raw should carry the failing string")
	}
	if string(m["_decode_error"]) != "true" {
		t.Fatal("_decode_error should be set")
	}
}

func TestDoubleDecodeInnerFailure(t *testing.T) {
	state := `{"interactiveState":` + jsonString("bad json{") + `}`
	rec := `{"report_state":` + jsonString(state) + `}`
	m := decodeToMap(t, rec)
	var stateObj map[string]json.RawMessage
	json.Unmarshal(m["report_state"], &stateObj)
	if string(stateObj["interactiveState"]) != "null" {
		t.Fatalf("interactiveState should be null on inner failure: %s", stateObj["interactiveState"])
	}
	if _, ok := m["interactive_state_raw"]; !ok {
		t.Fatal("interactive_state_raw should carry the inner failing string")
	}
	if string(m["_decode_error"]) != "true" {
		t.Fatal("_decode_error should be set on inner failure")
	}
}

func TestDoubleDecodeNoReportState(t *testing.T) {
	rec := `{"question_id":"q1"}`
	m := decodeToMap(t, rec)
	if _, ok := m["report_state"]; ok {
		t.Fatal("should not invent report_state")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
