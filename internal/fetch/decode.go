package fetch

import "encoding/json"

// doubleDecode parses report_state (a JSON string whose interactiveState is
// itself a JSON string) into real JSON objects. A record that fails to decode
// leaves the object field null and stores the failing raw string in a sibling
// field with a _decode_error marker; data is never dropped and no field ever
// mixes object and string types.
func doubleDecode(record []byte) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(record, &obj); err != nil {
		return record
	}
	raw, ok := obj["report_state"]
	if !ok {
		return record
	}

	var stateStr string
	if err := json.Unmarshal(raw, &stateStr); err != nil {
		// report_state is not a JSON string; leave it as-is.
		return record
	}

	var stateObj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stateStr), &stateObj); err != nil {
		obj["report_state"] = jsonNull
		obj["report_state_raw"] = mustMarshal(stateStr)
		obj["_decode_error"] = jsonTrue
		return remarshal(record, obj)
	}

	if inner, ok := stateObj["interactiveState"]; ok {
		var innerStr string
		if err := json.Unmarshal(inner, &innerStr); err == nil {
			// Require the decoded value to be a JSON object, symmetric with the
			// report_state handling above; a scalar/array/null is not merged in.
			var innerObj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(innerStr), &innerObj); err == nil {
				stateObj["interactiveState"] = mustMarshal(innerObj)
			} else {
				stateObj["interactiveState"] = jsonNull
				obj["interactive_state_raw"] = mustMarshal(innerStr)
				obj["_decode_error"] = jsonTrue
			}
		}
	}

	obj["report_state"] = mustMarshal(stateObj)
	return remarshal(record, obj)
}

var (
	jsonNull = json.RawMessage("null")
	jsonTrue = json.RawMessage("true")
)

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return jsonNull
	}
	return b
}

func remarshal(original []byte, obj map[string]json.RawMessage) []byte {
	b, err := json.Marshal(obj)
	if err != nil {
		return original
	}
	return b
}
