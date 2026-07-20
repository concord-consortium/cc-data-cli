package dataset

import (
	"encoding/json"
	"testing"
	"time"
)

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{
		Version:   CurrentManifestVersion,
		Name:      "wildfire",
		CreatedAt: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		Stores:    map[string]Store{"answers": {File: "answers.v1.jsonl", Version: 1, Count: 3, Columns: map[string]string{"answer": "JSON"}}},
	}
	m.SetMembershipRef("answers", 584, 1)
	if err := writeManifestFile(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifestFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "wildfire" || got.Stores["answers"].Count != 3 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	ref := got.Membership[MembershipKey("answers", 584)]
	if ref.File != "members_answers_584.v1.jsonl" || ref.Version != 1 {
		t.Fatalf("membership ref wrong: %+v", ref)
	}
}

func TestManifestFutureVersionRefused(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"version": CurrentManifestVersion + 1, "name": "x"})
	_, err := decodeManifest(data)
	if err == nil {
		t.Fatal("future manifest version should be refused")
	}
}

func TestManifestMigratesZeroVersion(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"name": "x"})
	m, err := decodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != CurrentManifestVersion {
		t.Fatalf("version should migrate to current, got %d", m.Version)
	}
}
