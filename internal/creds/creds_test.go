package creds

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/zalando/go-keyring"
)

func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// config.ConfigDir uses os.UserHomeDir; override via HOME on unix.
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	now = func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	return home
}

func TestSaveTokenRoundTripKeyring(t *testing.T) {
	setupHome(t)
	keyring.MockInit()

	var s Store
	if err := s.Save("learn.concord.org", "ccd_abc", "https://report-server.concord.org"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Token("learn.concord.org")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ccd_abc" {
		t.Fatalf("token = %q", got)
	}

	// The credentials.json must NOT carry the secret when keyring-backed.
	home, _ := os.UserHomeDir()
	data, _ := os.ReadFile(filepath.Join(home, ".config", "cc-data", "credentials.json"))
	if bytes.Contains(data, []byte("ccd_abc")) {
		t.Fatalf("keyring-backed token leaked into credentials.json: %s", data)
	}
}

func TestSaveFallbackToFile(t *testing.T) {
	setupHome(t)
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	origSet := keyringSet
	keyringSet = func(service, account, secret string) error { return errors.New("no secret service") }
	t.Cleanup(func() { keyringSet = origSet })

	var s Store
	if err := s.Save("localhost:8080", "ccd_file", "http://localhost:4000"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Token("localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ccd_file" {
		t.Fatalf("file token = %q", got)
	}
	if errb.Len() == 0 {
		t.Fatal("expected a one-line stderr note on fallback")
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].Backend != BackendFile {
		t.Fatalf("expected file backend, got %+v", list)
	}
}

func TestDelete(t *testing.T) {
	setupHome(t)
	keyring.MockInit()
	var s Store
	if err := s.Save("a.concord.org", "ccd_a", "https://report-server.concord.org"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a.concord.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Token("a.concord.org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestListOrdering(t *testing.T) {
	setupHome(t)
	keyring.MockInit()
	var s Store
	for _, p := range []string{"c.concord.org", "a.concord.org", "b.concord.org"} {
		if err := s.Save(p, "ccd_"+p, "https://report-server.concord.org"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.concord.org", "b.concord.org", "c.concord.org"}
	for i, w := range want {
		if list[i].Portal != w {
			t.Fatalf("List order[%d] = %q, want %q", i, list[i].Portal, w)
		}
	}
	if list[0].StoredAt.IsZero() || list[0].Server == "" {
		t.Fatalf("metadata missing: %+v", list[0])
	}
}

func TestCredFilePermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows stat mode is not POSIX permission bits")
	}
	setupHome(t)
	keyring.MockInit()
	var s Store
	if err := s.Save("learn.concord.org", "ccd_abc", "https://report-server.concord.org"); err != nil {
		t.Fatal(err)
	}
	dir, _ := config.ConfigDir()
	fi, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.json mode = %v, want 0600", fi.Mode().Perm())
	}
}
