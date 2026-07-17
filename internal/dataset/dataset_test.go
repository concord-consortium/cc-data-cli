package dataset

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedClock() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }

func TestCRUD(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Portal: "learn.concord.org", Name: "wildfire"}

	d, err := Create(root, ref, "Wildfire study")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Exists() {
		t.Fatal("dataset should exist after create")
	}
	m, _ := d.ReadManifest()
	if m.Description != "Wildfire study" {
		t.Fatalf("description = %q", m.Description)
	}

	// Edit.
	if err := d.Edit("new desc"); err != nil {
		t.Fatal(err)
	}
	m, _ = d.ReadManifest()
	if m.Description != "new desc" {
		t.Fatalf("edit failed: %q", m.Description)
	}

	// Rename.
	newD, err := d.Rename(root, "wildfire2")
	if err != nil {
		t.Fatal(err)
	}
	if !newD.Exists() || d.Exists() {
		t.Fatal("rename should move the folder")
	}
	m, _ = newD.ReadManifest()
	if m.Name != "wildfire2" {
		t.Fatalf("manifest name not renamed: %q", m.Name)
	}

	// Delete.
	if err := newD.Delete(); err != nil {
		t.Fatal(err)
	}
	if newD.Exists() {
		t.Fatal("dataset should be gone after delete")
	}
}

func TestCreateRejectsReservedName(t *testing.T) {
	root := t.TempDir()
	_, err := Create(root, Ref{Portal: "learn.concord.org", Name: "main"}, "")
	if err == nil {
		t.Fatal("create main should be rejected")
	}
}

func TestPurgeKeepsShell(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Portal: "learn.concord.org", Name: "ds"}
	d, err := Create(root, ref, "desc")
	if err != nil {
		t.Fatal(err)
	}
	// Plant artifacts and a lock file.
	os.WriteFile(d.Path("answers.v1.jsonl"), []byte("{}\n"), 0o600)
	os.WriteFile(d.Path("report_584.csv"), []byte("a\n"), 0o600)
	os.MkdirAll(d.Path("segments"), 0o700)
	os.WriteFile(d.Path("segments/seg_answers_584.jsonl"), []byte("{}\n"), 0o600)
	os.MkdirAll(d.Path("attachments"), 0o700)
	os.WriteFile(d.Path(".dataset.lock"), nil, 0o600)

	if err := d.Purge(); err != nil {
		t.Fatal(err)
	}
	if !d.Exists() {
		t.Fatal("purge must keep the dataset shell (manifest)")
	}
	if _, err := os.Stat(d.Path("answers.v1.jsonl")); !os.IsNotExist(err) {
		t.Fatal("purge should delete the store")
	}
	if _, err := os.Stat(d.Path("report_584.csv")); !os.IsNotExist(err) {
		t.Fatal("purge should delete the CSV")
	}
	if _, err := os.Stat(d.Path("segments")); !os.IsNotExist(err) {
		t.Fatal("purge should delete segments")
	}
	// The lock file must survive.
	if _, err := os.Stat(d.Path(".dataset.lock")); err != nil {
		t.Fatal("purge must not delete lock files")
	}
	m, _ := d.ReadManifest()
	if len(m.Downloads) != 0 || len(m.Stores) != 0 || m.Description != "desc" {
		t.Fatalf("purge should clear holdings but keep identity: %+v", m)
	}
}

func TestMutationBusyWhenActivityHeld(t *testing.T) {
	root := t.TempDir()
	ref := Ref{Portal: "learn.concord.org", Name: "ds"}
	d, err := Create(root, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a live fetch holding the shared activity lock.
	ok, err := d.Activity().TryRLock()
	if err != nil || !ok {
		t.Fatalf("could not take activity read lock: %v", err)
	}
	defer d.Activity().RUnlock()

	if err := d.Edit("x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("edit under fetch should be busy, got %v", err)
	}
	if err := d.Purge(); !errors.Is(err, ErrBusy) {
		t.Fatalf("purge under fetch should be busy, got %v", err)
	}
	if _, err := d.Rename(root, "other"); !errors.Is(err, ErrBusy) {
		t.Fatalf("rename under fetch should be busy, got %v", err)
	}
	if err := d.Delete(); !errors.Is(err, ErrBusy) {
		t.Fatalf("delete under fetch should be busy, got %v", err)
	}
}

func TestAutoName(t *testing.T) {
	clock = fixedClock
	defer func() { clock = defaultClock }()
	root := t.TempDir()
	name, err := AutoName(root, "learn.concord.org", "Wildfire Study!")
	if err != nil {
		t.Fatal(err)
	}
	if name != "2026-07-17_wildfire-study" {
		t.Fatalf("auto name = %q", name)
	}
	if err := ValidateName(name); err != nil {
		t.Fatalf("auto name must be valid: %v", err)
	}

	// No description: counter.
	os.MkdirAll(filepath.Join(PortalDatasetsDir(root, "learn.concord.org"), "2026-07-17_1"), 0o700)
	name2, _ := AutoName(root, "learn.concord.org", "")
	if name2 != "2026-07-17_2" {
		t.Fatalf("counter name = %q", name2)
	}
}
