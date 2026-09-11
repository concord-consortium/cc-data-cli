package dataset

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeInput(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	writeInput(t, dir, "a.csv", "one")
	first := Fingerprint(filepath.Join(dir, "a.csv"))

	writeInput(t, dir, "a.csv", "one more")
	if got := Fingerprint(filepath.Join(dir, "a.csv")); got == first {
		t.Fatalf("fingerprint unchanged after a rewrite: %s", got)
	}

	// A same-size rewrite has to move the fingerprint too, which is what mtime
	// is in it for.
	writeInput(t, dir, "b.csv", "same")
	before := Fingerprint(filepath.Join(dir, "b.csv"))
	later := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "b.csv"), later, later); err != nil {
		t.Fatal(err)
	}
	if got := Fingerprint(filepath.Join(dir, "b.csv")); got == before {
		t.Fatalf("fingerprint unchanged after an mtime change: %s", got)
	}
}

func TestFingerprintAbsentForMissingFile(t *testing.T) {
	if got := Fingerprint(filepath.Join(t.TempDir(), "nope.csv")); got != FingerprintAbsent {
		t.Fatalf("Fingerprint of a missing file = %q, want %q", got, FingerprintAbsent)
	}
}

func TestFreshTracksItsInputs(t *testing.T) {
	dir := t.TempDir()
	writeInput(t, dir, "one.csv", "a")
	writeInput(t, dir, "two.csv", "b")
	inputs := []string{"one.csv", "two.csv"}
	const sig = "view-signature"
	mat := Materialized{File: "materialized/reports.parquet", Inputs: FingerprintInputs(dir, inputs), Signature: sig}

	if !mat.Fresh(dir, inputs, sig) {
		t.Fatal("Fresh should hold while every input is unchanged")
	}

	writeInput(t, dir, "two.csv", "bb")
	if mat.Fresh(dir, inputs, sig) {
		t.Fatal("Fresh should fail after an input is rewritten")
	}

	writeInput(t, dir, "two.csv", "b")
	mat.Inputs = FingerprintInputs(dir, inputs)
	if err := os.Remove(filepath.Join(dir, "two.csv")); err != nil {
		t.Fatal(err)
	}
	if mat.Fresh(dir, inputs, sig) {
		t.Fatal("Fresh should fail once a recorded input is deleted")
	}

	writeInput(t, dir, "two.csv", "b")
	mat.Inputs = FingerprintInputs(dir, inputs)
	writeInput(t, dir, "three.csv", "c")
	if mat.Fresh(dir, append(inputs, "three.csv"), sig) {
		t.Fatal("Fresh should fail once the view declares an input the Parquet was not built from")
	}
	if !mat.Fresh(dir, inputs, sig) {
		t.Fatal("Fresh should still hold for the unchanged input set")
	}

	// The other direction: the view stops declaring an input the Parquet was
	// built from, which leaves that run's rows in the Parquet and nowhere else.
	if mat.Fresh(dir, inputs[:1], sig) {
		t.Fatal("Fresh should fail once the view stops declaring a recorded input")
	}

	// And the axis the fingerprints cannot see: the view's own definition moved,
	// which a cc-data upgrade does without touching an input byte.
	if mat.Fresh(dir, inputs, "a-different-view-definition") {
		t.Fatal("Fresh should fail once the view definition the Parquet was built from has changed")
	}
}

// TestFreshRoundTripsAnAbsentInput is why the sentinel exists: a view keeps
// declaring a CSV that is not on disk, so "absent then and absent now" has to
// read as fresh while "absent then, present now" reads as stale.
func TestFreshRoundTripsAnAbsentInput(t *testing.T) {
	dir := t.TempDir()
	writeInput(t, dir, "one.csv", "a")
	inputs := []string{"one.csv", "two.csv"}
	const sig = "view-signature"
	mat := Materialized{File: "materialized/reports.parquet", Inputs: FingerprintInputs(dir, inputs), Signature: sig}
	if mat.Inputs["two.csv"] != FingerprintAbsent {
		t.Fatalf("missing input recorded as %q, want %q", mat.Inputs["two.csv"], FingerprintAbsent)
	}
	if !mat.Fresh(dir, inputs, sig) {
		t.Fatal("Fresh should hold while the missing input is still missing")
	}

	writeInput(t, dir, "two.csv", "b")
	if mat.Fresh(dir, inputs, sig) {
		t.Fatal("Fresh should fail once the missing input is restored")
	}
}
