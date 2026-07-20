package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

func TestReplExecuteAndDotTables(t *testing.T) {
	root := t.TempDir()
	d, err := dataset.Create(root, dataset.Ref{Portal: "learn.concord.org", Name: "ds"}, "")
	if err != nil {
		t.Fatal(err)
	}
	e, err := duck.Open(context.Background(), []duck.DatasetSpec{{DS: d}}, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	var out, errw bytes.Buffer
	executeReplStatement(e, "SELECT 40 + 2 AS answer", &out, &errw)
	if !strings.Contains(out.String(), "42") {
		t.Fatalf("query output = %q", out.String())
	}
	if errw.Len() != 0 {
		t.Fatalf("no error expected: %q", errw.String())
	}

	out.Reset()
	executeReplStatement(e, ".tables", &out, &errw)
	if !strings.Contains(out.String(), "reports") || !strings.Contains(out.String(), "answers") {
		t.Fatalf(".tables should list the views: %q", out.String())
	}

	// A bad statement writes to stderr, never stdout.
	out.Reset()
	errw.Reset()
	executeReplStatement(e, "SELECT * FROM no_such_view", &out, &errw)
	if out.Len() != 0 {
		t.Fatalf("error must not touch stdout: %q", out.String())
	}
	if errw.Len() == 0 {
		t.Fatal("expected an error on stderr")
	}
}

func TestReplHistoryFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows stat mode is not POSIX permission bits")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "cc-data", "repl_history")
	if err := fsutil.PreCreate0600(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("repl history mode = %v, want 0600", fi.Mode().Perm())
	}
}
