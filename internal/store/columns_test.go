package store

import "testing"

func TestColumnDetectorPinnedAndWidened(t *testing.T) {
	d := NewColumnDetector()
	d.Observe([]byte(`{"source_key":"s","score":5,"ratio":1.5,"flag":true,"note":"hi","report_state":{"a":1}}`))
	d.Observe([]byte(`{"score":7,"note":"there"}`))
	m := d.Map()

	if m["source_key"] != TypeVARCHAR {
		t.Fatalf("pinned source_key = %q", m["source_key"])
	}
	if m["report_state"] != TypeJSON {
		t.Fatalf("pinned report_state = %q", m["report_state"])
	}
	if m["score"] != TypeBIGINT {
		t.Fatalf("score should be BIGINT, got %q", m["score"])
	}
	if m["ratio"] != TypeDOUBLE {
		t.Fatalf("ratio should be DOUBLE, got %q", m["ratio"])
	}
	if m["flag"] != TypeBOOLEAN {
		t.Fatalf("flag should be BOOLEAN, got %q", m["flag"])
	}
	if m["note"] != TypeVARCHAR {
		t.Fatalf("note should be VARCHAR, got %q", m["note"])
	}
}

func TestColumnDetectorWideningRules(t *testing.T) {
	d := NewColumnDetector()
	d.Observe([]byte(`{"n":1}`))
	d.Observe([]byte(`{"n":2.5}`)) // int then double -> DOUBLE
	d.Observe([]byte(`{"mix":1}`))
	d.Observe([]byte(`{"mix":"text"}`)) // int then string -> JSON (mixed)
	m := d.Map()
	if m["n"] != TypeDOUBLE {
		t.Fatalf("int+double should widen to DOUBLE, got %q", m["n"])
	}
	if m["mix"] != TypeJSON {
		t.Fatalf("incompatible types should collapse to JSON, got %q", m["mix"])
	}
}

func TestColumnDetectorNullOnlyIsVarchar(t *testing.T) {
	d := NewColumnDetector()
	d.Observe([]byte(`{"maybe":null}`))
	if d.Map()["maybe"] != TypeVARCHAR {
		t.Fatalf("null-only field should default VARCHAR, got %q", d.Map()["maybe"])
	}
}
