package packages

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// buildTime is every entry's modification time, so the same tree always zips to the same
// bytes and the same checksum.
var buildTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// Files is a package's contents: slash-separated paths relative to its directory, sorted,
// each with the mode it ships with.
type Files struct {
	Dir   string
	Paths []string
	Modes map[string]os.FileMode
}

// shipMode is the one mode a file keeps through build and unzip: 0755 when any execute bit is
// set, else 0644. unzip restores it on the VM, where a non-.py entrypoint is executed directly.
func shipMode(m fs.FileMode) os.FileMode {
	if m.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// Set is the paths as a lookup, the shape ReadManifest checks the entrypoint against.
func (f Files) Set() map[string]bool {
	set := make(map[string]bool, len(f.Paths))
	for _, p := range f.Paths {
		set[p] = true
	}
	return set
}

// excluded is what a build leaves out: any dot path (.git, .cc-data-run, .cc-data-build),
// __pycache__, *.pyc and local-data.
func excluded(rel string, isDir bool) bool {
	base := path.Base(rel)
	if strings.HasPrefix(base, ".") || base == "__pycache__" || (isDir && base == "local-data") {
		return true
	}
	return !isDir && strings.HasSuffix(base, ".pyc")
}

// Collect walks dir for the files a package is made of. A link or other non-regular file that
// build would ship is refused rather than skipped, since the runner refuses an archive holding
// one; under an excluded name it is skipped like any other file.
func Collect(dir string) (Files, error) {
	// The package directory itself may be a link; only what is inside it may not.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Files{}, err
	}
	files := Files{Dir: dir, Modes: map[string]os.FileMode{}}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if excluded(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link; a package may hold only regular files", rel)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files.Paths = append(files.Paths, rel)
		files.Modes[rel] = shipMode(info.Mode())
		return nil
	})
	sort.Strings(files.Paths)
	return files, err
}

// Zip writes a reproducible archive: sorted entries, Deflate, a fixed time and each file's ship
// mode. The size limits are report-server's.
func Zip(files Files) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, rel := range files.Paths {
		h := &zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: buildTime}
		h.SetMode(files.Modes[rel])
		dst, err := w.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		src, err := os.Open(filepath.Join(files.Dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(dst, src)
		src.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckEntrypointMode refuses a non-.py entrypoint that would not be executable once unzipped,
// since the runner executes it directly. Windows records no execute bit to check.
func (f Files) CheckEntrypointMode(entrypoint string) error {
	if runtime.GOOS == "windows" || strings.HasSuffix(entrypoint, ".py") || f.Modes[entrypoint]&0o111 != 0 {
		return nil
	}
	return fmt.Errorf("the entrypoint %s is not executable; the runner executes a non-.py entrypoint directly (chmod +x it)", entrypoint)
}

// Checksum is the catalog's spelling of an archive's digest.
func Checksum(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
