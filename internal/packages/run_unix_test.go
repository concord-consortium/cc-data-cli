//go:build !windows

package packages

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid is a process that has not exited; a zombie awaiting its reaper
// counts as exited.
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	return len(fields) == 0 || fields[0] != "Z"
}

func TestRunLeavesNothingBehind(t *testing.T) {
	dir, files := shellPackage(t, `sleep 30 >/dev/null 2>&1 &
echo $! > "$RD_OUTPUT_DIR/../bg.pid"
echo ok > "$RD_OUTPUT_DIR/display.md"
`, nil)
	var stderr bytes.Buffer
	if _, err := Run(context.Background(), runOpts(t, dir, files, []string{"PATH=" + os.Getenv("PATH")}, &stderr)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, RunDirName, "bg.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	for deadline := time.Now().Add(5 * time.Second); alive(pid); {
		if time.Now().After(deadline) {
			t.Fatalf("the package's background process %d outlived the run", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
