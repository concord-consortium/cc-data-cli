package packages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DisplayLimitBytes is the runner's cap on display.md in UTF-8 bytes, decided only in
// final-design.md section 10.
const DisplayLimitBytes = 64 * 1024

// countKeys are the counts.json keys the runner reads; anything else a package writes there
// is ignored, as it is on the VM.
var countKeys = []string{"answers", "logs", "log_freshness_at"}

// Result is what the runner keeps of a run: the display, the summary line and the counts.
type Result struct {
	DisplayPath  string         `json:"display"`
	DisplayBytes int64          `json:"display_bytes"`
	Summary      *string        `json:"summary"`
	Counts       map[string]any `json:"counts"`
}

// OutputRefused is a result the runner would refuse: a missing, linked or oversized
// display.md, a linked summary.txt, or a linked output directory.
type OutputRefused struct{ Reason string }

func (e *OutputRefused) Error() string { return e.Reason }

// ReadResult reads the output directory as the runner does, following no link: the
// directory must be itself, and each file must be a regular file.
func ReadResult(outDir string) (Result, error) {
	var res Result
	if err := RealDir(outDir); err != nil {
		return res, &OutputRefused{Reason: "the package replaced its output directory with a link"}
	}
	_, size, err := readOwnFile(outDir, "display.md", DisplayLimitBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return res, &OutputRefused{Reason: "the package wrote no display.md"}
	case err != nil:
		return res, err
	case size > DisplayLimitBytes:
		return res, &OutputRefused{Reason: fmt.Sprintf("the package's display.md is %d bytes, over the %d-byte limit", size, DisplayLimitBytes)}
	}
	res.DisplayPath = filepath.Join(outDir, "display.md")
	// The runner reads summary.txt and counts.json under the display cap too, truncating them.
	res.DisplayBytes = size
	if text, _, err := readOwnFile(outDir, "summary.txt", DisplayLimitBytes); err == nil {
		line := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(text, "\r\n", "\n"), "\n", 2)[0])
		if line != "" {
			res.Summary = &line
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	res.Counts = map[string]any{}
	if text, _, err := readOwnFile(outDir, "counts.json", DisplayLimitBytes); err == nil {
		var parsed map[string]any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			for _, k := range countKeys {
				if v, ok := parsed[k]; ok {
					res.Counts[k] = v
				}
			}
		}
	}
	return res, nil
}

// readOwnFile returns up to limit+1 bytes of a regular, unlinked file and its full size. The
// Lstat-then-SameFile pair stands in for O_NOFOLLOW, which Windows lacks.
func readOwnFile(dir, name string, limit int64) (string, int64, error) {
	p := filepath.Join(dir, name)
	before, err := os.Lstat(p)
	if err != nil {
		return "", 0, err
	}
	if before.Mode()&fs.ModeSymlink != 0 {
		return "", 0, &OutputRefused{Reason: fmt.Sprintf("the package's %s is a link, not a file", name)}
	}
	if !before.Mode().IsRegular() {
		return "", 0, &OutputRefused{Reason: fmt.Sprintf("the package's %s is not a file", name)}
	}
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return "", 0, &OutputRefused{Reason: fmt.Sprintf("the package's %s changed while it was read", name)}
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	return string(b), after.Size(), err
}

// RealDir reports an error unless p is a directory reached without following a link.
func RealDir(p string) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is a link or not a directory", p)
	}
	return nil
}
