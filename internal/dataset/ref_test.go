package dataset

import (
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		raw     string
		def     string
		portal  string
		name    string
		wantErr bool
	}{
		{"learn.concord.org/wildfire", "", "learn.concord.org", "wildfire", false},
		{"https://learn.concord.org/wildfire", "", "learn.concord.org", "wildfire", false},
		{"wildfire", "learn.concord.org", "learn.concord.org", "wildfire", false},
		{"wildfire", "", "", "", true}, // no default portal
		{"localhost:8080/ds", "", "localhost:8080", "ds", false},
		{"", "x", "", "", true},
		{"portal/", "", "", "", true}, // empty name
	}
	for _, c := range cases {
		ref, err := ParseRef(c.raw, c.def)
		if c.wantErr {
			if err == nil {
				t.Fatalf("ParseRef(%q,%q) should error", c.raw, c.def)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseRef(%q,%q) error: %v", c.raw, c.def, err)
		}
		if ref.Portal != c.portal || ref.Name != c.name {
			t.Fatalf("ParseRef(%q) = %+v, want %s/%s", c.raw, ref, c.portal, c.name)
		}
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"a", "wildfire", "2026-07-16_wildfire", "a_b", "a-b", strings.Repeat("a", 63)}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Fatalf("ValidateName(%q) should pass: %v", n, err)
		}
	}
	invalid := []string{
		"",
		"-bad",                                                       // leading hyphen
		"_bad",                                                       // leading underscore
		"Bad",                                                        // uppercase
		"a b",                                                        // space
		strings.Repeat("a", 64),                                      // too long
		"main", "temp", "system", "information_schema", "pg_catalog", // reserved
	}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Fatalf("ValidateName(%q) should fail", n)
		}
	}
}

func TestRefDirEncoding(t *testing.T) {
	ref := Ref{Portal: "localhost:8080", Name: "ds"}
	dir := ref.Dir("/root")
	if !strings.Contains(dir, "localhost_8080") {
		t.Fatalf("dir should encode the port: %s", dir)
	}
}
