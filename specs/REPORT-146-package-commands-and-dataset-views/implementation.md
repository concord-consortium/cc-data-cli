# Implementation Plan: cc-data-cli: the package commands and the dataset views that replace `_lib`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-146
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Implementation Plan

The plan comes in seven commits. Each one builds and passes `go test ./...` on its own, in this order. All of the code below was compiled and tested in a scratch copy of `main` (`58c9172`) while this plan was written: the full suite passes, `go vet` is clean for Linux, and `GOOS=windows go vet` is clean for `internal/packages` and `internal/api`. A table under each step names mutations its tests catch; every one listed was applied and made a test fail. The scratch copy has been deleted.

The package rules and applicability are report-server's (requirements, Dependencies). cc-data calls `POST /api/v1/packages/validate` and `POST /api/v1/packages/applies`, which REPORT-167 adds. A 404 from either means an older report-server, and is a warning, not a failure. So this plan ships and works before that story lands, and gains its checks when it does.

### The run-scoped view `run_answers`, and the documented counts

**Summary**: R1, R3 to R5. Adds the static view `run_answers` to `internal/duck/views.go`, and documents it with the learner count through the existing `student_id_mapping` view and the log-freshness query, where the drift guards check. It tests both counts on a dataset where each wrong way of counting gives a different number. Static views are derived everywhere else, so `cmd/query.go`'s help and the MCP `query` description pick it up with no edit.

**Files affected**:
- `internal/duck/views.go`: one builder, registered after `runMembershipView`.
- `internal/duck/run_views_test.go`: new.
- `internal/guidance/src/core.md`: the `run_answers` entry, the learner count, and the freshness query.
- `docs/researcher-guide.md`: one table row.

**Estimated diff size**: ~170 lines

```diff
--- /tmp/o	2026-10-08 09:00:24.920084911 -0400
+++ internal/duck/views.go	2026-10-08 09:00:14.224272861 -0400
@@ -71,6 +71,7 @@
 	stmts = append(stmts, vs.storeView(store.TypeAnswers))
 	stmts = append(stmts, vs.storeView(store.TypeHistory))
 	stmts = append(stmts, vs.runMembershipView())
+	stmts = append(stmts, vs.runAnswersView())
 	stmts = append(stmts, vs.downloadsView())
 	stmts = append(stmts, vs.attachmentFilesView())
 	stmts = append(stmts, vs.attachmentStatesView())
@@ -360,6 +361,20 @@
 	return viewStmt{name: name, primary: primary, fallback: fallback, files: files}
 }
 
+// runAnswersView is every answer once per run whose answers fetch holds it in membership: the
+// type-qualified membership join, registered on every dataset so a run with no answers counts
+// zero rather than failing to bind the way answers_<run> does. It must follow both views it
+// reads, and it declares no files, so materializing reads it through theirs.
+func (vs viewSet) runAnswersView() viewStmt {
+	name := vs.prefix + `"run_answers"`
+	answers := vs.prefix + sqlIdent(store.TypeAnswers)
+	membership := vs.prefix + `"run_membership"`
+	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT m.run_id, s.* FROM %s s JOIN %s m USING (source_key, remote_endpoint, question_id) WHERE m.type = %s",
+		name, answers, membership, sqlStr(store.TypeAnswers))
+	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS BIGINT) AS run_id, s.* FROM %s s WHERE false", name, answers)
+	return viewStmt{name: name, primary: primary, fallback: fallback}
+}
+
 // downloadsView is a VALUES dimension table from the manifest.
 //
 // hide_names separates the two meanings of a name column: where a run hid names, student_name
```

`run_answers` declares no files, so `dataset materialize` leaves it a join over the materialized `answers` and `run_membership` (checked against a copy of `staging-smoketest`). The learner count is tested here even though it adds no view, because the guidance presents it as the way to count, and a guidance query that silently stopped counting correctly is the failure this test exists to catch.

```go
package duck

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// The documented learner count, run-scoped on both sides, as the guidance gives it.
const learnersOfRun = "SELECT count(DISTINCT m.user_id) FROM student_id_mapping m JOIN run_answers a ON a.remote_endpoint = m.run_remote_endpoint WHERE a.run_id = %d AND m.run_id = %d"

// Each wrong way to count gives a different number here: counting endpoints instead of users
// (user 1 answered in two assignments), joining the bare-endpoint learner (who would borrow
// another bare answer), counting an answer once instead of once per run (e3/q1 is in runs 700
// and 800), and joining membership untyped (run 700's history holds e1a/q1 too).
func TestRunAnswersAndTheDocumentedLearnerCount(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 700, [][]byte{
		answerRec("s", "https://p/d/e1a", "q1", "a"), answerRec("s", "https://p/d/e1b", "q1", "b"),
		answerRec("s", "https://p/d/e3", "q1", "c"), answerRec("s", "https://p/d/", "q1", "bare"),
	})
	hist, _ := json.Marshal(map[string]any{"source_key": "s", "remote_endpoint": "https://p/d/e1a", "question_id": "q1", "history_id": "h1"})
	seg := store.OpenSegment(d.Dir, store.TypeHistory, 700)
	if err := seg.AppendPage([][]byte{hist}, time.Unix(700, 0).UTC(), 700); err != nil {
		t.Fatal(err)
	}
	seg.WriteCursor(&store.Cursor{Items: 1})
	if _, err := d.MergeCompact(store.TypeHistory, 700, seg); err != nil {
		t.Fatal(err)
	}
	buildStore(t, d, 800, [][]byte{answerRec("s", "https://p/d/e3", "q1", "c"), answerRec("s", "https://p/d/e9", "q1", "z")})
	row := func(learner, user, class, offering int, endpoint string) string {
		return fmt.Sprintf("%d,%d,%d,s%d,%d,%d,https://act/1,%s", learner, user, user, user, class, offering, endpoint)
	}
	addDimensionCSV(t, d, dimFixture{run: 700, slug: "student-id-mapping", fetchedAt: at(1), filter: `{}`, csv: mappingCSV(
		row(101, 1, 1, 70, "https://p/d/e1a"), row(102, 1, 1, 71, "https://p/d/e1b"),
		row(103, 3, 1, 70, "https://p/d/e3"), row(104, 4, 1, 70, "https://p/d/"))})
	addDimensionCSV(t, d, dimFixture{run: 800, slug: "student-id-mapping", fetchedAt: at(2), filter: `{}`, csv: mappingCSV(
		row(109, 9, 2, 90, "https://p/d/e9"))})
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)

	if n := queryInt(t, e, "SELECT count(*) FROM run_answers WHERE run_id = 700"); n != 4 {
		t.Errorf("run 700 answers = %d, want 4", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM run_answers WHERE run_id = 800"); n != 2 {
		t.Errorf("run 800 answers = %d, want 2", n)
	}
	if n := queryInt(t, e, fmt.Sprintf(learnersOfRun, 700, 700)); n != 2 {
		t.Errorf("run 700 learners = %d, want 2 (users 1 and 3; user 4's endpoint is bare)", n)
	}
	if n := queryInt(t, e, fmt.Sprintf(learnersOfRun, 800, 800)); n != 1 {
		t.Errorf("run 800 learners = %d, want 1", n)
	}
}

func TestRunAnswersIsEmptyNotMissingOnAFreshDataset(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: newDS(t, "ds")}}, nil)
	if n := queryInt(t, e, "SELECT count(*) FROM run_answers"); n != 0 {
		t.Errorf("run_answers has %d rows", n)
	}
	if n := queryInt(t, e, fmt.Sprintf(learnersOfRun, 1, 1)); n != 0 {
		t.Errorf("the learner count binds to %d on a fresh dataset, want 0", n)
	}
}
```

| Mutation | Test that fails |
|---|---|
| `m.type = 'answers'` dropped | run 700 answers = 5 (the history membership joins) |
| the documented count by endpoint instead of `user_id` | run 700 learners = 3 |

Documentation (the two guards in `internal/guidance/guard_test.go` fail until both files carry the name; the dashes in `core.md` match that list's existing entries):

```diff
--- /tmp/o	2026-10-08 09:00:24.902084889 -0400
+++ internal/guidance/src/core.md	2026-10-08 09:00:14.224676445 -0400
@@ -122,6 +122,24 @@
   **type-qualified**: `answers a JOIN run_membership m USING
   (source_key, remote_endpoint, question_id) WHERE m.run_id = 584 AND m.type =
   'answers'`. History joins add `history_id` to the USING list.
+- `run_answers` — every `answers` row with `run_id`, once for each run whose
+  answers fetch holds it (a Student Answers or a Student ID Mapping run), so one
+  run's answers are `SELECT count(*) FROM
+  run_answers WHERE run_id = 584`. It is the type-qualified membership join above,
+  always present, so a run with no answers counts zero rather than failing to bind
+  as `answers_<run>` does. Its `run_id` is membership; the store's own `_run_id` is
+  only the run that last fetched the record, so filtering `answers` by `_run_id`
+  undercounts a run whose answers a later run re-fetched.
+- A run's learners with at least one answer come from its Student ID Mapping
+  report: `SELECT count(DISTINCT m.user_id) FROM student_id_mapping m JOIN
+  run_answers a ON a.remote_endpoint = m.run_remote_endpoint WHERE a.run_id = 584
+  AND m.run_id = 584`, where 584 is a Student ID Mapping run whose answers were
+  fetched. Count by `user_id`, never by endpoint, since a student has one endpoint
+  per assignment. A Student ID Mapping run is computed live, so re-reading it with
+  `--refresh` picks up learners who joined since; a Student Answers run is fixed
+  when its query ran, and a whole-class one fails above three or four assignments.
+- A run's log freshness is `SELECT count(*) AS logs, max(event_time) AS
+  log_freshness_at FROM logs WHERE run_id = <id>`.
 - Reports-to-stores join: `reports.res_<N>_remote_endpoint =
   answers.remote_endpoint`, with `res_<N>_<question_id>_*` pairing to
   `answers.question_id`.
--- /tmp/o	2026-10-08 09:00:24.914084903 -0400
+++ docs/researcher-guide.md	2026-10-08 08:57:22.784965017 -0400
@@ -346,6 +346,7 @@
 | `attachment_content` | The text/JSON content of every saved CODAP/SageModeler snapshot, queryable and diffable. |
 | `student_id_mapping` | One row per learner from Student ID Mapping runs, with the ids that join them to their answers and history. |
 | `student_metadata` | One row per learner from Student Metadata runs: name, username, class, school, teachers, permission forms. |
+| `run_answers` | Every answer with the `run_id` of each run that holds it: `SELECT count(*) FROM run_answers WHERE run_id = 584` counts one run's answers. Join it to `student_id_mapping` on `remote_endpoint = run_remote_endpoint` to count learners by `user_id`. |
 | `run_membership`, `downloads` | Provenance: which run's fetch covered which records, and what each download was, including whether its run hid names. |
 
 ### Speeding up a large dataset
```

---

### A package's files: the manifest fields, collecting and zipping, the result

**Summary**: R6 (cc-data's part), R8, R14, R17, R18. Adds `internal/packages`:
- **Reading the manifest.** It reads only the fields the runner acts on.
- **Collecting the package.** It gathers the file set `build` ships and `run` stages, each file with the mode it ships with, so an executable entrypoint stays executable after `unzip`.
- **Zipping.** It writes a reproducible zip.
- **Reading the result.** It reads `display.md`, `summary.txt` and `counts.json` as the runner does.

Links are checked with `Lstat` then `os.SameFile` rather than `O_NOFOLLOW`, which Windows (CI only) lacks. The package has no callers yet and is independently reviewable.

**Files affected**:
- `internal/packages/manifest.go`: new.
- `internal/packages/archive.go`: new.
- `internal/packages/result.go`: new.
- `internal/packages/files_test.go`: new; holds the shared helpers `goodManifest` and `writeTree`.

**Estimated diff size**: ~430 lines

```go
// Package packages is the local side of a Researcher Dashboard package: reading the manifest
// fields the runner acts on, collecting and zipping the package, staging and running it the
// way the runner does, and reading the result files. The package rules themselves, and
// applicability, are report-server's, which cc-data asks rather than copies.
package packages

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName reports whether name fits the catalog's name grammar, which init needs before
// report-server has seen anything.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// URLs is a manifest's applicability patterns, passed to report-server as written.
type URLs struct {
	All  []string `json:"all"`
	Any  []string `json:"any"`
	None []string `json:"none"`
}

// DeclaresPatterns reports whether any sense holds a pattern; a package that declares none
// applies to every scope.
func (u URLs) DeclaresPatterns() bool {
	return len(u.All)+len(u.Any)+len(u.None) > 0
}

// Manifest is the part of manifest.json the runner acts on. Everything else, and every limit,
// is report-server's to check (cmd's validate call).
type Manifest struct {
	Name                    string `json:"name"`
	Title                   string `json:"title"`
	Version                 string `json:"version"`
	Description             string `json:"description,omitempty"`
	URLs                    URLs   `json:"urls"`
	CluePrepull             bool   `json:"clue_prepull"`
	Entrypoint              string `json:"entrypoint"`
	ExpectedDurationSeconds int    `json:"expected_duration_seconds"`
}

// ReadManifest reads the fields a run acts on, refusing an entrypoint outside files and a
// duration that is not a positive integer; every other rule is report-server's.
func ReadManifest(data []byte, files map[string]bool) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest.json: %v", err)
	}
	if !files[m.Entrypoint] || unsafePath(m.Entrypoint) {
		return m, fmt.Errorf("manifest.json: entrypoint %q is not a file in the package", m.Entrypoint)
	}
	if m.ExpectedDurationSeconds < 1 {
		return m, fmt.Errorf("manifest.json: expected_duration_seconds must be a positive integer")
	}
	return m, nil
}

// unsafePath is report-server's Archive.unsafe_path?: absolute, a drive letter, or a ".." segment.
func unsafePath(name string) bool {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return true
	}
	if len(name) >= 2 && name[1] == ':' && unicode.IsLetter(rune(name[0])) {
		return true
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}
```
```go
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
	Bytes int64
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

// Collect walks dir for the files a package is made of. A link or other non-regular file is
// refused rather than skipped, since the runner refuses an archive holding one.
func Collect(dir string) (Files, error) {
	files := Files{Dir: dir, Modes: map[string]os.FileMode{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
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
		files.Bytes += info.Size()
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
```
```go
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

// OutputRefused is a result the runner would refuse: no display.md, one that is a link or
// not a file, or one over the cap.
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
```

Tests (the link and mode tests skip on Windows, where links need privileges and files have no execute bit):
- **`TestReadManifestNeedsWhatTheRunActsOn`**: a missing entrypoint is refused, and so are `"300"`, `1.5` and `0` for the duration.
- **`TestCollectLeavesOutWhatBuildMustNotShip`**: the tree holds `.git/`, `.cc-data-run/`, `.cc-data-build/`, `__pycache__/cache.txt`, `lib/y.pyc`, `local-data/` and `.DS_Store`. Exactly `lib/q.sql,manifest.json,run.py` is collected. `__pycache__` holds a non-`.pyc` file so the directory rule is tested on its own.
- **`TestCollectRefusesALink`**.
- **`TestZipIsReproducible`**: a touched mtime leaves the checksum unchanged.
- **`TestModesSurviveTheZip`**: `run.py` (0755 on disk) zips as 0755 and a 0600 `manifest.json` as 0644. A 0644 `run.sh` entrypoint fails `CheckEntrypointMode`, and a 0644 `run.py` passes it.
- **`TestDisplayCapEdges`**: 65,536 bytes pass and 65,537 are refused, and the constant equals 65,536.
- **`TestResultReadsOnlyTheRunnersKeys`**: the summary is the first line, trimmed, and `learners` in `counts.json` is dropped.
- **`TestResultRefusesALinkedDisplay`**.

| Mutation | Test that fails |
|---|---|
| `size >= DisplayLimitBytes` | `TestDisplayCapEdges` |
| `__pycache__` not excluded | `TestCollectLeavesOutWhatBuildMustNotShip` |
| every entry zipped 0644 | `TestModesSurviveTheZip` |
| a duration of 0 accepted | `TestReadManifestNeedsWhatTheRunActsOn` |

---

### `package run`'s engine: scope, layout, environment, execution

**Summary**: R10 to R16.
- **Scope.** Parses the author's scope file.
- **Layout.** Rebuilds `.cc-data-run/` (`pkg/`, `in/`, `out/`, `data/` and a `.gitignore` of `*`) and writes the runner's `scope.json`.
- **Applicability.** Asks an injected `Applies` function, which the commands step wires to report-server.
- **Environment and interpreter.** Builds the runner's environment plus the passthrough list, and picks the interpreter.
- **Execution.** Runs the entrypoint from the staged copy in its own process group, kills the group at the bound or on an interrupt, and kills what is left of it once the entrypoint exits.
- **Result.** Reads it with `ReadResult`.

**Files affected**:
- `internal/packages/scope.go`: new.
- `internal/packages/run.go`: new.
- `internal/packages/proc_unix.go` and `proc_windows.go`: new.
- `internal/packages/run_test.go`: new; holds `scopeJSON`, `runOpts` and `shellPackage`.

**Estimated diff size**: ~520 lines

```go
package packages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

var classHashRe = regexp.MustCompile(`^[0-9a-f]{48}$`)

// Class and Assignment are scope.json's entries.
type Class struct {
	ClassHash string `json:"class_hash"`
	ClassID   int64  `json:"class_id"`
}

type Assignment struct {
	OfferingID int64   `json:"offering_id"`
	RunnableID int64   `json:"runnable_id"`
	Name       *string `json:"name"`
	URL        string  `json:"url"`
}

// LocalScope is the four keys rigse supplies on the VM, which an author writes for a laptop.
type LocalScope struct {
	Kind        string       `json:"kind"`
	ID          string       `json:"id"`
	Classes     []Class      `json:"classes"`
	Assignments []Assignment `json:"assignments"`
}

// ScopeFile is scope.json as the runner writes it. ClueSource is always "firebase" today, the
// runner's constant; Dataset is the bare dataset name, as the runner writes pkg-<id>, while
// RD_DATASET carries the full ref.
type ScopeFile struct {
	LocalScope
	ClueSource string `json:"clue_source"`
	Dataset    string `json:"dataset"`
	OutputDir  string `json:"output_dir"`
}

// MarshalJSON keeps the runner's key order, so a diff between a local and a VM scope.json is
// only the values.
func (s ScopeFile) MarshalJSON() ([]byte, error) {
	type ordered struct {
		Kind        string       `json:"kind"`
		ID          string       `json:"id"`
		Classes     []Class      `json:"classes"`
		Assignments []Assignment `json:"assignments"`
		ClueSource  string       `json:"clue_source"`
		Dataset     string       `json:"dataset"`
		OutputDir   string       `json:"output_dir"`
	}
	return json.Marshal(ordered{s.Kind, s.ID, s.Classes, s.Assignments, s.ClueSource, s.Dataset, s.OutputDir})
}

// ParseLocalScope reads an author's scope file, refusing any key the four do not name (the
// other three are the runner's to set) and any shape the runner would never send.
func ParseLocalScope(data []byte) (LocalScope, error) {
	var s LocalScope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("scope file: %v (it holds exactly kind, id, classes and assignments)", err)
	}
	switch {
	case s.Kind == "":
		return s, fmt.Errorf("scope file: kind is required")
	case s.ID == "":
		return s, fmt.Errorf("scope file: id is required")
	case len(s.Classes) == 0:
		return s, fmt.Errorf("scope file: classes must name at least one class")
	case s.Assignments == nil:
		return s, fmt.Errorf("scope file: assignments is required (an empty list is allowed)")
	}
	for i, c := range s.Classes {
		if !classHashRe.MatchString(c.ClassHash) || c.ClassID < 1 {
			return s, fmt.Errorf("scope file: classes[%d] needs a 48-hex class_hash and a positive class_id", i)
		}
	}
	for i, a := range s.Assignments {
		if a.OfferingID < 1 || a.RunnableID < 1 {
			return s, fmt.Errorf("scope file: assignments[%d] needs a positive offering_id and runnable_id", i)
		}
	}
	return s, nil
}

// AssignmentURLs is the scope's half of the URLs a package's patterns are matched against.
func (s LocalScope) AssignmentURLs() []string {
	var urls []string
	for _, a := range s.Assignments {
		if a.URL != "" {
			urls = append(urls, a.URL)
		}
	}
	return urls
}
```
```go
package packages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TimeoutMargin is what the runner adds to expected_duration_seconds before it kills a run.
const TimeoutMargin = 10 * time.Minute

// RunDirName is the working tree package run keeps inside the package directory. It starts
// with "." so Collect, and so build, never ships it.
const RunDirName = ".cc-data-run"

// passthrough is what a laptop adds to the runner's environment so the package's own cc-data
// calls find the researcher's stored credential (keychain or credentials file), reach the
// server, and run at all. The runner sets HTTPS_PROXY and HTTP_PROXY when it has a proxy, and
// NO_PROXY keeps a laptop's exemptions with them. Matching is case-insensitive, so the
// lower-case spellings pass too.
var passthrough = []string{
	"HOME", "PATH", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
	"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
	// Windows has no release, but CI builds and tests there.
	"SYSTEMROOT", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATHEXT", "COMSPEC",
}

// Verdict is report-server's answer to whether a package's patterns match a scope. Unconfirmed
// means the report-server answering has no applicability route yet, so nothing was checked.
type Verdict struct {
	Applies     bool
	Reason      string
	Unconfirmed bool
}

// Refused is a package the runner would not start: it does not apply, or it needs the CLUE
// pre-pull, which cc-data cannot do.
type Refused struct{ Reason string }

func (e *Refused) Error() string { return e.Reason }

// Failed is a package that started and did not finish cleanly.
type Failed struct{ Reason string }

func (e *Failed) Error() string { return e.Reason }

// RunOptions is everything package run decides before the entrypoint starts.
type RunOptions struct {
	Dir       string                                       // the package directory
	Manifest  Manifest                                     // validated against Files
	Files     Files                                        // what build would ship
	Scope     LocalScope                                   // the author's scope file
	Applies   func(context.Context, URLs) (Verdict, error) // report-server's applicability answer
	Dataset   string                                       // the full ref, <portal>/<name>
	Name      string                                       // the ref's bare name
	Portal    string                                       // the ref's portal host
	DataRoot  string                                       // CC_DATA_ROOT as package run resolved it
	ReportSrv string                                       // the stored credential's server, or ""
	Stderr    io.Writer                                    // where the package's own output goes
	Environ   func() []string
	LookPath  func(string) (string, error)
	Timeout   time.Duration // zero means expected_duration_seconds + TimeoutMargin
}

// Run prepares the runner's layout, runs the entrypoint and reads its result.
func Run(ctx context.Context, o RunOptions) (Result, error) {
	if o.Manifest.CluePrepull {
		return Result{}, &Refused{Reason: "the package sets clue_prepull, and cc-data cannot fetch CLUE data yet, so it runs only on the VM"}
	}
	if o.Manifest.URLs.DeclaresPatterns() {
		v, err := o.Applies(ctx, o.Manifest.URLs)
		switch {
		case err != nil:
			return Result{}, err
		case v.Unconfirmed:
			fmt.Fprintln(o.Stderr, "warning: report-server cannot check applicability yet; the package runs, and the VM's runner is the check")
		case !v.Applies:
			return Result{}, &Refused{Reason: "the package does not apply to this class: " + v.Reason}
		}
	}
	run := filepath.Join(o.Dir, RunDirName)
	paths, err := prepare(run, o.Files)
	if err != nil {
		return Result{}, err
	}
	scope := ScopeFile{LocalScope: o.Scope, ClueSource: "firebase", Dataset: o.Name, OutputDir: paths.out}
	b, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return Result{}, err
	}
	scopePath := filepath.Join(paths.in, "scope.json")
	if err := os.WriteFile(scopePath, b, 0o644); err != nil {
		return Result{}, err
	}

	env := map[string]string{
		"CC_DATA_ROOT":   o.DataRoot,
		"CC_DATA_PORTAL": o.Portal,
		"CC_DATA_LOCAL":  paths.data,
		"RD_DATASET":     o.Dataset,
		"RD_SCOPE_FILE":  scopePath,
		"RD_DATA_DIR":    paths.data,
		"RD_OUTPUT_DIR":  paths.out,
	}
	if o.ReportSrv != "" {
		env["RD_REPORT_SERVER_URL"] = o.ReportSrv
	}
	command, args, note, err := entrypointCommand(paths.pkg, o.Manifest.Entrypoint, o.LookPath)
	if err != nil {
		return Result{}, err
	}
	if note != "" {
		fmt.Fprintln(o.Stderr, note)
	}

	timeout := o.Timeout
	if timeout == 0 {
		timeout = time.Duration(o.Manifest.ExpectedDurationSeconds)*time.Second + TimeoutMargin
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = paths.pkg
	cmd.Env = environment(o.Environ(), env)
	cmd.Stdout = o.Stderr
	cmd.Stderr = o.Stderr
	killGroup(cmd)
	err = cmd.Run()
	// As on the VM, nothing the package started outlives it or writes after it.
	reapGroup(cmd)
	if err != nil {
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			return Result{}, &Failed{Reason: fmt.Sprintf("the package ran past its %ds bound", int(timeout.Seconds()))}
		case ctx.Err() != nil:
			return Result{}, &Failed{Reason: "the package was interrupted"}
		}
		return Result{}, &Failed{Reason: fmt.Sprintf("the package exited unsuccessfully: %v", err)}
	}
	return ReadResult(paths.out)
}

type runPaths struct{ pkg, in, out, data string }

// prepare empties and rebuilds the run tree. Every directory it removes or creates under the
// package must be a real directory, so a link left there can never aim the removal outside.
func prepare(run string, files Files) (runPaths, error) {
	p := runPaths{
		pkg:  filepath.Join(run, "pkg"),
		in:   filepath.Join(run, "in"),
		out:  filepath.Join(run, "out"),
		data: filepath.Join(run, "data"),
	}
	if err := ensureRealDir(run); err != nil {
		return p, err
	}
	if err := os.WriteFile(filepath.Join(run, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return p, err
	}
	for _, d := range []string{p.pkg, p.in, p.out} {
		if _, err := os.Lstat(d); err == nil {
			if err := RealDir(d); err != nil {
				return p, fmt.Errorf("refusing to empty %s: it is a link or not a directory", d)
			}
			if err := os.RemoveAll(d); err != nil {
				return p, err
			}
		}
		if err := os.Mkdir(d, 0o755); err != nil {
			return p, err
		}
	}
	if err := ensureRealDir(p.data); err != nil {
		return p, err
	}
	abs, err := filepath.Abs(run)
	if err != nil {
		return p, err
	}
	p = runPaths{filepath.Join(abs, "pkg"), filepath.Join(abs, "in"), filepath.Join(abs, "out"), filepath.Join(abs, "data")}
	for _, rel := range files.Paths {
		if err := copyFile(filepath.Join(files.Dir, filepath.FromSlash(rel)), filepath.Join(p.pkg, filepath.FromSlash(rel)), files.Modes[rel]); err != nil {
			return p, err
		}
	}
	return p, nil
}

func ensureRealDir(d string) error {
	if _, err := os.Lstat(d); errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(d, 0o755)
	}
	if err := RealDir(d); err != nil {
		return fmt.Errorf("refusing to use %s: it is a link or not a directory", d)
	}
	return nil
}

// copyFile stages one file with the mode build gives it, so the staged package is the one
// unzip produces on the VM.
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// entrypointCommand is the runner's choice of interpreter: python3.11 for a .py entrypoint,
// otherwise the entrypoint itself. A laptop without python3.11 falls back to python3, saying so.
func entrypointCommand(pkgDir, entrypoint string, lookPath func(string) (string, error)) (string, []string, string, error) {
	path := filepath.Join(pkgDir, filepath.FromSlash(entrypoint))
	if !strings.HasSuffix(entrypoint, ".py") {
		return path, nil, "", nil
	}
	if p, err := lookPath("python3.11"); err == nil {
		return p, []string{path}, "", nil
	}
	if p, err := lookPath("python3"); err == nil {
		return p, []string{path}, "note: python3.11, the VM's interpreter, is not on PATH; running with python3", nil
	}
	return "", nil, "", fmt.Errorf("neither python3.11 nor python3 is on PATH")
}

// environment is the passthrough taken from the caller's environment (LC_* included), then
// the runner's variables over it; nothing else is inherited.
func environment(from []string, set map[string]string) []string {
	keep := map[string]string{}
	for _, kv := range from {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(k, "LC_") || contains(passthrough, strings.ToUpper(k)) {
			keep[k] = v
		}
	}
	for k, v := range set {
		keep[k] = v
	}
	out := make([]string, 0, len(keep))
	for k, v := range keep {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
```
```go
//go:build !windows

package packages

import (
	"os/exec"
	"syscall"
)

// killGroup runs the package in its own process group and kills the whole group on timeout,
// so a child the entrypoint started (cc-data, a shell) does not outlive the bound.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// reapGroup kills whatever is left of the package's process group once the entrypoint has
// exited. The group outlives its leader while any member does, so the kill still finds it.
func reapGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
```
```go
//go:build windows

package packages

import "os/exec"

// killGroup and reapGroup kill only the entrypoint on Windows, which has no release.
func killGroup(cmd *exec.Cmd) {}

func reapGroup(cmd *exec.Cmd) {}
```

Tests (the entrypoint is a `/bin/sh` script, so these skip on Windows):
- **`TestLocalScopeRefusesTheRunnersKeys`**: a scope file setting `dataset` is refused.
- **`TestRunGivesThePackageTheRunnersLayout`**:
  - The package dumps its environment, copies `scope.json` and lists its working directory.
  - The environment carries `RD_DATASET` (the full ref), `CC_DATA_PORTAL`, `RD_SCOPE_FILE` a passed-through `LC_ALL` and lower-case `https_proxy`, and not an injected `AWS_SECRET_ACCESS_KEY`.
  - `scope.json` has the bare `dataset`, `clue_source: "firebase"` and the absolute `output_dir`.
  - The working directory holds exactly `manifest.json` and `run.sh` (no `local-data/`), the staged `run.sh` keeps mode 0755, and `.gitignore` is `*`.
- **`TestRunRefusesBeforeStarting`**: when `Applies` says no, the error is the runner's prefix plus the reason, and the entrypoint never starts. A `clue_prepull` package is refused.
- **`TestRunWarnsAndRunsWhenApplicabilityIsUnconfirmed`**: an unconfirmed verdict prints the warning and runs.
- **`TestRunKillsAtTheBound`**: `sleep 30 & wait` under a 300 ms bound fails as "past its bound" within 5 seconds.
- **`TestRunLeavesNothingBehind`**: a package that backgrounds `(sleep 1; echo late > "$RD_OUTPUT_DIR/late.txt")` and exits 0 succeeds, and 1.5 seconds later `late.txt` does not exist.
- **`TestRunKillsTheGroupWhenInterrupted`**: the same package, with the parent context canceled after 300 ms, fails as "interrupted" within 5 seconds.
- **`TestRunRefusesALinkedRunDirectory`**: `.cc-data-run` linked to another directory is refused, and a file in that directory survives.

| Mutation | Test that fails |
|---|---|
| no `Setpgid` | `TestRunKillsAtTheBound` (the orphaned `sleep` holds the output pipe for 30 s) |
| link check on the run directory removed | `TestRunRefusesALinkedRunDirectory` |
| the whole environment inherited | `TestRunGivesThePackageTheRunnersLayout` |
| run from the source directory, not the staged copy | `TestRunGivesThePackageTheRunnersLayout` |
| a "does not apply" verdict ignored | `TestRunRefusesBeforeStarting` |
| a canceled context reported as an ordinary failure | `TestRunKillsTheGroupWhenInterrupted` |
| no `reapGroup` after the entrypoint exits | `TestRunLeavesNothingBehind` (the background job writes `late.txt`) |
| an unconfirmed verdict treated as a refusal | `TestRunWarnsAndRunsWhenApplicabilityIsUnconfirmed` |

---

### The API client: publish, validate and applies

**Summary**: R12, R19, R21 to R23, client side.
- **`send`.** `Client.do` keeps its signature and delegates to a new `send` that names the body's media type; `attempt` takes it as a parameter.
- **`PublishPackage` and `ValidatePackage`** post the raw zip. A POST is already never retried (`isIdempotent`), and `net/http` sets `Content-Length` from the `*bytes.Reader`, which report-server requires.
- **`PackageApplies`** posts JSON.
- **`RouteMissing`** recognizes the 404 an older report-server gives.

**Files affected**:
- `internal/api/client.go`: `send`, plus a content-type parameter on `attempt`.
- `internal/api/packages.go`: new.

**Estimated diff size**: ~140 lines; the requests' shapes are tested at the command level below

```diff
--- /tmp/o	2026-10-08 05:22:45.439149318 -0400
+++ internal/api/client.go	2026-10-07 17:17:44.313590304 -0400
@@ -95,6 +95,11 @@
 // do runs a request under the retry policy and returns the 2xx body, or a typed
 // *APIError (contract) / *TransientError (budget exhausted).
 func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
+	return c.send(ctx, method, path, query, body, "application/json")
+}
+
+// send is do with the body's media type named, for the routes that take raw bytes.
+func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) ([]byte, error) {
 	u := c.BaseURL + path
 	if len(query) > 0 {
 		u += "?" + query.Encode()
@@ -110,7 +115,7 @@
 			return nil, err
 		}
 
-		data, status, err := c.attempt(ctx, method, u, body)
+		data, status, err := c.attempt(ctx, method, u, body, contentType)
 		if err != nil {
 			last = err
 			// A non-idempotent request (POST) may have reached the server, so
@@ -139,7 +144,7 @@
 
 // attempt performs one HTTP request under a per-attempt deadline and returns the
 // body and status; a transport error (including deadline) is returned as err.
-func (c *Client) attempt(ctx context.Context, method, u string, body []byte) ([]byte, int, error) {
+func (c *Client) attempt(ctx context.Context, method, u string, body []byte, contentType string) ([]byte, int, error) {
 	reqCtx := ctx
 	var cancel context.CancelFunc
 	if c.RequestTimeout > 0 {
@@ -160,7 +165,7 @@
 	}
 	req.Header.Set("Accept", "application/json")
 	if body != nil {
-		req.Header.Set("Content-Type", "application/json")
+		req.Header.Set("Content-Type", contentType)
 	}
 
 	resp, err := c.HTTP.Do(req)
```
```go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// PublishedPackage is report-server's answer to a publish.
type PublishedPackage struct {
	CatalogID      int64  `json:"catalog_id"`
	Identity       string `json:"identity"`
	Version        string `json:"version"`
	Checksum       string `json:"checksum"`
	Visibility     string `json:"visibility"`
	Official       bool   `json:"official"`
	CurrentVersion string `json:"current_version"`
}

// PublishPackage posts a package archive as the raw request body. origin is "" for the
// caller's own users/<id>, or projects/<id>; official asks the server to mark it official,
// which only a publisher may. Like every POST it is never retried.
func (c *Client) PublishPackage(ctx context.Context, archive []byte, origin string, official bool) (*PublishedPackage, error) {
	data, err := c.send(ctx, http.MethodPost, "/api/v1/packages", packageQuery(origin, official), archive, "application/zip")
	if err != nil {
		return nil, err
	}
	var out PublishedPackage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ValidatedPackage is what a publish of the archive would record. The last two are not
// refusals: each says only a publish would be refused now, with publish's own message for the
// second.
type ValidatedPackage struct {
	Identity              string  `json:"identity"`
	Version               string  `json:"version"`
	Checksum              string  `json:"checksum"`
	Visibility            string  `json:"visibility"`
	AlreadyPublished      bool    `json:"already_published"`
	PublishingUnavailable *string `json:"publishing_unavailable"`
}

// ValidatePackage asks report-server to apply every publish check to the archive and stop
// before storing it, with the same origin and official a publish would send. A refusal is the
// same coded error a publish would answer.
func (c *Client) ValidatePackage(ctx context.Context, archive []byte, origin string, official bool) (*ValidatedPackage, error) {
	data, err := c.send(ctx, http.MethodPost, "/api/v1/packages/validate", packageQuery(origin, official), archive, "application/zip")
	if err != nil {
		return nil, err
	}
	var out ValidatedPackage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppliesRequest names the patterns and the scope's assignment URLs, which report-service's
// deriver follows into their activities, so the URLs matched are the runner's.
type AppliesRequest struct {
	URLs           map[string][]string `json:"urls"`
	AssignmentURLs []string            `json:"assignment_urls,omitempty"`
}

// AppliesAnswer is report-server's verdict, with what the deriver read and could not.
type AppliesAnswer struct {
	Applies bool   `json:"applies"`
	Reason  string `json:"reason"`
	Unread  []struct {
		URL    string `json:"url"`
		Reason string `json:"reason"`
	} `json:"unread"`
	Truncated bool `json:"truncated"`
}

// PackageApplies asks report-server's one matcher whether the patterns match the scope.
func (c *Client) PackageApplies(ctx context.Context, req AppliesRequest) (*AppliesAnswer, error) {
	var out AppliesAnswer
	if err := c.postJSON(ctx, "/api/v1/packages/applies", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func packageQuery(origin string, official bool) url.Values {
	q := url.Values{}
	if origin != "" {
		q.Set("origin", origin)
	}
	if official {
		q.Set("official", "true")
	}
	return q
}

// RouteMissing reports a 404 from a route this cc-data expects and the server does not have
// yet: a report-server older than the validate and applies routes.
func RouteMissing(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
```

---

### The `cc-data package` commands and the `init` stub

**Summary**: R9, R12, R16, R19 to R23.
- **Wiring.** Adds the four subcommands to the root, and `Init` with the embedded stub.
- **The report-server calls.** `validate` is shared by `build` and `run` and returns report-server's answer: `build` warns when `already_published` is true or `publishing_unavailable` names a reason, and `run` ignores both, since running a package needs neither a new version nor a bucket. `appliesFor` gives `run` its `Applies` function, with a 5-minute timeout for the deriver.
- **Testability.** `build` and `publish` carry a `run(ctx, client, ...)` method, as `reportCreateFlags` does, so both are tested against `httptest` with no stored credential.

**Files affected**:
- `cmd/package.go`: new.
- `cmd/root.go`: `root.AddCommand(newPackageCmd())` after `newQueryCmd()`.
- `internal/packages/init.go`: new.
- `internal/packages/template/run.py`: new, embedded.
- `cmd/package_test.go`: new.

**Estimated diff size**: ~600 lines

```go
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/packages"
	"github.com/spf13/cobra"
)

const packageLong = `Develop, test and publish a Researcher Dashboard package.

A package is a directory holding manifest.json and an entrypoint. init writes both; run
executes the package against your own dataset under the runner's rules; build zips it;
publish registers the zip in the catalog with your cc-data token.

What run cannot reproduce from the VM: the package runs as you, with no network sandbox,
and with HOME, PATH and the locale passed through so its own cc-data calls find your
login. A .py entrypoint runs with python3.11 when it is on PATH, as on the VM, else
python3.`

func newPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Develop, test and publish a Researcher Dashboard package",
		Long:  packageLong,
	}
	cmd.AddCommand(newPackageInitCmd(), newPackageRunCmd(), newPackageBuildCmd(), newPackagePublishCmd())
	return cmd
}

// packageDir is the optional [dir] argument, defaulting to the current directory.
func packageDir(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}

// loadPackage collects a package directory and validates its manifest against what it holds.
func loadPackage(dir string) (packages.Manifest, packages.Files, error) {
	files, err := packages.Collect(dir)
	if err != nil {
		return packages.Manifest{}, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_PACKAGE", Message: err.Error()}
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return packages.Manifest{}, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_MANIFEST", Message: fmt.Sprintf("no manifest.json in %s", dir)}
	}
	m, err := packages.ReadManifest(data, files.Set())
	if err != nil {
		return m, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_MANIFEST", Message: err.Error()}
	}
	if err := files.CheckEntrypointMode(m.Entrypoint); err != nil {
		return m, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_PACKAGE", Message: err.Error()}
	}
	return m, files, nil
}

func newPackageInitCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Write a manifest.json skeleton and a run.py stub",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := packageDir(args)
			written, err := packages.Init(dir, name)
			if err != nil {
				return output.Usagef("%v", err)
			}
			return output.ResultLine(map[string]any{"dir": dir, "written": written})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the package name (default: the directory's name, when it fits ^[a-z0-9][a-z0-9-]{0,62}$)")
	return cmd
}

func newPackageRunCmd() *cobra.Command {
	var datasetRef, scopePath string
	cmd := &cobra.Command{
		Use:   "run [dir] --dataset <ref> --scope <file>",
		Short: "Run a package locally exactly as the dashboard's runner will",
		Long: "Run a package against your own dataset under the runner's rules: the same scope.json,\n" +
			"environment variables, output files, display.md cap and applicability check.\n\n" +
			"--scope names a file holding the scope's kind, id, classes and assignments.\n" +
			"Results are written under <dir>/.cc-data-run/, which build never ships.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if datasetRef == "" || scopePath == "" {
				return output.Usagef("--dataset and --scope are required")
			}
			dir := packageDir(args)
			m, files, err := loadPackage(dir)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(scopePath)
			if err != nil {
				return output.Usagef("reading the scope file: %v", err)
			}
			scope, err := packages.ParseLocalScope(raw)
			if err != nil {
				return &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_SCOPE", Message: err.Error()}
			}
			cfg, dataRoot, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveRef(cfg, datasetRef)
			if err != nil {
				return err
			}
			client, err := api.ForPortal(ref.Portal)
			if err != nil {
				return err
			}
			// The package runs in its own process group, out of reach of the terminal's Ctrl-C,
			// so an interrupt has to cancel the run for the group to be killed with it.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			archive, err := packages.Zip(files)
			if err != nil {
				return output.Internalf("%v", err)
			}
			// A version that is already published, or a portal that cannot store packages yet,
			// still runs; only a publish is refused.
			if _, err := validate(ctx, client, archive, "", false); err != nil {
				return err
			}
			start := time.Now()
			res, err := packages.Run(ctx, packages.RunOptions{
				Dir: dir, Manifest: m, Files: files, Scope: scope, Applies: appliesFor(client, scope),
				Dataset: ref.String(), Name: ref.Name, Portal: ref.Portal.Host(), DataRoot: dataRoot,
				ReportSrv: client.BaseURL, Stderr: output.Stderr(), Environ: os.Environ, LookPath: exec.LookPath,
			})
			if err != nil {
				return packageRunError(err)
			}
			return output.ResultLine(map[string]any{
				"display": res.DisplayPath, "display_bytes": res.DisplayBytes, "summary": res.Summary,
				"counts": res.Counts, "elapsed_seconds": int(time.Since(start).Seconds()),
			})
		},
	}
	cmd.Flags().StringVar(&datasetRef, "dataset", "", "the dataset the package pulls into and reads: <portal>/<name>")
	cmd.Flags().StringVar(&scopePath, "scope", "", "a JSON file holding the scope's kind, id, classes and assignments")
	return cmd
}

// derivationTimeout covers report-service's deriver, which may spend up to 240 seconds
// fetching a scope's activities; the client's 60-second default would cut it off.
const derivationTimeout = 5 * time.Minute

// validate asks report-server whether a publish of the archive would be accepted, and returns
// its answer. A report-server without the route is not a refusal: the archive is unchecked,
// the answer is nil, and saying so is the whole result.
func validate(ctx context.Context, client *api.Client, archive []byte, origin string, official bool) (*api.ValidatedPackage, error) {
	ans, err := client.ValidatePackage(ctx, archive, origin, official)
	switch {
	case api.RouteMissing(err):
		output.Warnf("report-server has no validate route yet, so this package is not checked against the catalog's rules until publish")
		return nil, nil
	case err != nil:
		return nil, api.AsCLIError(err)
	}
	return ans, nil
}

// appliesFor is package run's applicability check: report-server's one matcher, against the
// scope's assignment URLs and the interactive URLs report-service's deriver finds in them.
func appliesFor(client *api.Client, scope packages.LocalScope) func(context.Context, packages.URLs) (packages.Verdict, error) {
	return func(ctx context.Context, urls packages.URLs) (packages.Verdict, error) {
		long := *client
		long.RequestTimeout = derivationTimeout
		ans, err := long.PackageApplies(ctx, api.AppliesRequest{
			URLs:           map[string][]string{"all": urls.All, "any": urls.Any, "none": urls.None},
			AssignmentURLs: scope.AssignmentURLs(),
		})
		switch {
		case api.RouteMissing(err):
			return packages.Verdict{Unconfirmed: true}, nil
		case err != nil:
			return packages.Verdict{}, api.AsCLIError(err)
		}
		for _, u := range ans.Unread {
			output.Warnf("the class profile could not read %s (%s), so its interactives are not matched", u.URL, u.Reason)
		}
		if ans.Truncated {
			output.Warnf("the class profile was truncated, so not every interactive URL was matched")
		}
		return packages.Verdict{Applies: ans.Applies, Reason: ans.Reason}, nil
	}
}

func packageRunError(err error) error {
	var refused *packages.Refused
	var failed *packages.Failed
	var out *packages.OutputRefused
	var cliErr *output.CLIError
	switch {
	case errors.As(err, &cliErr):
		return cliErr
	case errors.As(err, &refused):
		return &output.CLIError{ExitCode: output.ExitInternal, Code: "PACKAGE_REFUSED", Message: err.Error()}
	case errors.As(err, &failed):
		return &output.CLIError{ExitCode: output.ExitInternal, Code: "PACKAGE_FAILED", Message: err.Error()}
	case errors.As(err, &out):
		return &output.CLIError{ExitCode: output.ExitInternal, Code: "PACKAGE_OUTPUT_REFUSED", Message: err.Error()}
	}
	return output.Internalf("%v", err)
}

type packageBuildFlags struct {
	out, portal, origin string
	official            bool
}

// run builds, validates and writes; it takes the client so it can be exercised against a test
// server without a stored credential.
func (f packageBuildFlags) run(ctx context.Context, client *api.Client, dir string) error {
	m, files, err := loadPackage(dir)
	if err != nil {
		return err
	}
	out := f.out
	if out == "" {
		// The manifest is unchecked until validate answers, and unchecked for good on a server
		// without the route, so its name and version must not be able to steer the path.
		name := fmt.Sprintf("%s-%s.zip", m.Name, m.Version)
		if strings.ContainsAny(name, `/\`) {
			return &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_MANIFEST", Message: fmt.Sprintf("the manifest's name and version make %q, which is not a file name; fix them or pass --out", name)}
		}
		out = filepath.Join(dir, packages.BuildDirName, name)
	} else if err := packages.CheckOutsidePackage(dir, out); err != nil {
		return output.Usagef("%v", err)
	}
	archive, err := packages.Zip(files)
	if err != nil {
		return output.Internalf("%v", err)
	}
	ans, err := validate(ctx, client, archive, f.origin, f.official)
	if err != nil {
		return err
	}
	if ans != nil && ans.AlreadyPublished {
		output.Warnf("%s %s is already published; publish will refuse it until the version changes", ans.Identity, ans.Version)
	}
	if ans != nil && ans.PublishingUnavailable != nil {
		output.Warnf("this package cannot be published yet: %s", *ans.PublishingUnavailable)
	}
	if err := packages.WriteBuild(out, archive); err != nil {
		return output.Internalf("%v", err)
	}
	return output.ResultLine(map[string]any{
		"path": out, "checksum": packages.Checksum(archive), "bytes": len(archive), "files": len(files.Paths),
	})
}

func newPackageBuildCmd() *cobra.Command {
	var f packageBuildFlags
	cmd := &cobra.Command{
		Use:   "build [dir]",
		Short: "Zip a package reproducibly, check it against the catalog's rules and print its checksum",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := reportsClient(f.portal)
			if err != nil {
				return err
			}
			return f.run(context.Background(), client, packageDir(args))
		},
	}
	cmd.Flags().StringVar(&f.out, "out", "", "where to write the zip (default: <dir>/.cc-data-build/<name>-<version>.zip)")
	cmd.Flags().StringVar(&f.portal, "portal", "", "portal whose catalog rules to check against: an environment alias or a hostname")
	cmd.Flags().StringVar(&f.origin, "origin", "", "projects/<id>, when the package will be published as a project's")
	cmd.Flags().BoolVar(&f.official, "official", false, "check it as an official publish will be checked (publisher role only)")
	return cmd
}

type packagePublishFlags struct {
	portal, origin   string
	official, asJSON bool
}

// run publishes an archive already read and checked; it takes the client so it can be
// exercised against a test server without a stored credential.
func (f packagePublishFlags) run(ctx context.Context, client *api.Client, archive []byte) error {
	got, err := client.PublishPackage(ctx, archive, f.origin, f.official)
	if err != nil {
		return api.AsWriteCLIError(err, "Publishing again is safe: ALREADY_EXISTS then means this attempt landed")
	}
	if want := packages.Checksum(archive); got.Checksum != want {
		return output.Internalf("report-server recorded checksum %s, but the archive sent is %s", got.Checksum, want)
	}
	if f.asJSON {
		return output.ResultLine(got)
	}
	fmt.Fprintf(output.Stdout(), "identity         %s\nversion          %s\nvisibility       %s\nofficial         %t\ncurrent version  %s\nchecksum         %s\n",
		got.Identity, got.Version, got.Visibility, got.Official, got.CurrentVersion, got.Checksum)
	return nil
}

func newPackagePublishCmd() *cobra.Command {
	var f packagePublishFlags
	cmd := &cobra.Command{
		Use:   "publish <zip> [--portal <portal|env>]",
		Short: "Publish a built package to the catalog with your cc-data token",
		Long: "POST the zip to report-server's catalog, which creates the package private on its first\n" +
			"publish and records the version. --origin projects/<id> publishes a project package;\n" +
			"--official needs the publisher role. The request is never retried.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archive, err := os.ReadFile(args[0])
			if err != nil {
				return output.Usagef("reading %s: %v", args[0], err)
			}
			client, err := reportsClient(f.portal)
			if err != nil {
				return err
			}
			return f.run(context.Background(), client, archive)
		},
	}
	cmd.Flags().StringVar(&f.portal, "portal", "", "portal whose catalog to publish to: an environment alias or a hostname")
	cmd.Flags().StringVar(&f.origin, "origin", "", "projects/<id> to publish a project's package")
	cmd.Flags().BoolVar(&f.official, "official", false, "mark the package official (publisher role only)")
	cmd.Flags().BoolVar(&f.asJSON, "json", false, "emit the catalog's answer as JSON")
	return cmd
}
```
```go
package packages

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildDirName is where build writes by default: inside the package, and skipped by
// Collect like every dot path, so one build's zip never reaches the next.
const BuildDirName = ".cc-data-build"

//go:embed template/run.py
var runStub []byte

// Init writes manifest.json and run.py into dir, creating it if needed. It reads nothing in
// dir except to refuse when either file already exists, in which case it writes neither.
func Init(dir, name string) ([]string, error) {
	if name == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		name = filepath.Base(abs)
		if !ValidName(name) {
			return nil, fmt.Errorf("the directory name %q is not a package name; pass --name (^[a-z0-9][a-z0-9-]{0,62}$)", name)
		}
	} else if !ValidName(name) {
		return nil, fmt.Errorf("--name %q must match ^[a-z0-9][a-z0-9-]{0,62}$", name)
	}
	for _, f := range []string{"manifest.json", "run.py"} {
		if _, err := os.Lstat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s already exists in %s; init writes only into a package that has neither file", f, dir)
		}
	}
	manifest := Manifest{
		Name: name, Title: name, Version: "0.1.0",
		Description: "One line saying what this package shows.",
		URLs:        URLs{All: []string{}, Any: []string{}, None: []string{}},
		Entrypoint:  "run.py", ExpectedDurationSeconds: 300,
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "run.py"), runStub, 0o755); err != nil {
		return nil, err
	}
	return []string{"manifest.json", "run.py"}, nil
}

// CheckOutsidePackage refuses an explicit build output that a later build would collect.
func CheckOutsidePackage(dir, out string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absDir, absOut)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	if excluded(filepath.ToSlash(rel), false) || excludedAncestor(filepath.ToSlash(rel)) {
		return nil
	}
	return fmt.Errorf("--out %s is inside the package, where the next build would include it", out)
}

func excludedAncestor(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if excluded(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return false
}

// WriteBuild writes the archive, creating its directory, with the .gitignore every working
// directory this tool makes inside a package carries.
func WriteBuild(out string, archive []byte) error {
	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if filepath.Base(dir) == BuildDirName {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(out, archive, 0o644)
}
```
```python
#!/usr/bin/env python3
"""A Researcher Dashboard package: counts a class's answers and the learners who wrote them.

It runs the same way under `cc-data package run` on a laptop and on the dashboard's VM. It
reads the scope from RD_SCOPE_FILE, pulls its own data into RD_DATASET with cc-data, counts
through cc-data's views, and writes display.md, summary.txt and counts.json to the scope's
output_dir. Stdlib only: the VM installs no Python packages.

It reads the class through a Student ID Mapping run, which the portal computes on every
download, so re-reading it picks up learners who joined since, and which has no limit on how
many assignments a class has. It reuses the researcher's own run for the class when there is
one and otherwise creates one, on the server the dataset's portal names.
"""
import json
import os
import subprocess
import sys


def cc_data(*args):
    result = subprocess.run(["cc-data", *args], capture_output=True, text=True)
    if result.returncode != 0:
        detail = " ".join(p for p in (result.stderr.strip(), result.stdout.strip()) if p)
        raise RuntimeError(f"cc-data {' '.join(args[:2])} exited {result.returncode}: {detail or '(no output)'}")
    return result.stdout


def query(dataset, sql):
    return json.loads(cc_data("query", "--dataset", dataset, sql, "--format", "json"))


def mapping_run(portal, class_id):
    """The newest Student ID Mapping run filtered to exactly this class, else a new one."""
    runs = json.loads(cc_data("reports", "list", "--portal", portal, "--json"))["runs"]
    mine = [r["run_id"] for r in runs
            if r.get("slug") == "student-id-mapping"
            and (r.get("report_filter") or {}).get("filters") == ["class"]
            and (r.get("report_filter") or {}).get("class") == [class_id]]
    if mine:
        return max(int(run) for run in mine)
    created = json.loads(cc_data(
        "reports", "create", "--portal", portal, "--report-slug", "student-id-mapping",
        "--report-filter", json.dumps({"class": [class_id]}), "--json"))
    return int(created["run"]["run_id"])


def main():
    with open(os.environ["RD_SCOPE_FILE"], encoding="utf-8") as handle:
        scope = json.load(handle)
    # RD_DATASET is the full <portal>/<name> ref; scope["dataset"] is the bare name.
    dataset = os.environ["RD_DATASET"]
    class_id = int(scope["classes"][0]["class_id"])

    try:
        cc_data("dataset", "create", dataset)
    except RuntimeError as err:
        if "exists" not in str(err).lower():
            raise
    run = mapping_run(os.environ["CC_DATA_PORTAL"], class_id)
    cc_data("get", "report", str(run), "--dataset", dataset, "--refresh")
    cc_data("get", "answers", str(run), "--dataset", dataset)

    rows = query(dataset, f"SELECT count(*) AS n FROM student_id_mapping WHERE run_id = {run}")[0]["n"]
    if rows == 0:
        raise RuntimeError("no report-service access to this class, or the class has no data")
    answers = query(dataset, f"SELECT count(*) AS n FROM run_answers WHERE run_id = {run}")[0]["n"]
    # By user_id, never by endpoint: a student has one endpoint per assignment.
    learners = query(dataset, f"""
        SELECT count(DISTINCT m.user_id) AS n FROM student_id_mapping m
        JOIN run_answers a ON a.remote_endpoint = m.run_remote_endpoint
        WHERE a.run_id = {run} AND m.run_id = {run}""")[0]["n"]

    out = scope["output_dir"]
    with open(os.path.join(out, "display.md"), "w", encoding="utf-8") as handle:
        handle.write(f"# Class {class_id}\n\n- {answers} answers\n- {learners} learners with at least one answer\n")
    with open(os.path.join(out, "summary.txt"), "w", encoding="utf-8") as handle:
        handle.write(f"{answers} answers from {learners} learners\n")
    with open(os.path.join(out, "counts.json"), "w", encoding="utf-8") as handle:
        json.dump({"answers": answers}, handle)


if __name__ == "__main__":
    try:
        main()
    except Exception as err:
        print(str(err), file=sys.stderr)
        sys.exit(1)
```

The stub was run through `Run` against a synthetic dataset holding a Student ID Mapping run and its answers, with a wrapper `cc-data` that fakes `reports list`, `reports create` and `get`, and forwards `dataset` and `query` to the scratch build.
- **The run it picks.** Out of a `reports list` holding a Student ID Mapping run filtered by class and school, one filtered by the class alone, a Student Answers run and another class's run, it took the class-only Student ID Mapping run.
- **The result.** It wrote "3 answers / 2 learners with at least one answer" and `counts.json` `{"answers": 3}`: a learner in two assignments counted once, and a learner with no answers not at all.
- **With no matching run** it called `reports create --report-slug student-id-mapping --report-filter {"class": [6]}`. With nothing pulled, it stopped at the empty-scope guard with its message.

Tests (against an `httptest` server that answers 404 for any route it was not given):
- **`TestPackageInitRefusesASecondInit`**.
- **`TestPackageBuildValidatesThenWrites`**:
  - Exactly one validate request is made, carrying the bytes that were written, as `application/zip`.
  - The zip is `wildfire-responses-0.1.0.zip` with two files, and the printed checksum is the archive's own.
  - A second build from inside the package gives the same checksum.
- **`TestPackageBuildWritesNothingTheServerRefuses`**: a 422 `UNPROCESSABLE` keeps its code, exits 5, and no zip is written.
- **`TestPackageBuildWarnsOnAPublishedVersionAndStillWrites`**: `already_published: true` prints "users/7/p 0.1.0 is already published; publish will refuse it until the version changes", and the zip is still written. **`TestPackageBuildIsQuietOnANewVersion`** checks the converse.
- **`TestPackageBuildValidatesAsThePublishWillBe`**: `--origin projects/20 --official` reach validate's query, and `official` is absent without the flag.
- **`TestValidateReturnsAPublishedVersionAsAnAnswerNotAnError`**: the shared `validate` returns the answer, so `run` proceeds on a published version.
- **`TestPackageBuildWarnsWhenThePortalCannotPublishAndStillWrites`**: `publishing_unavailable: "publishing is not configured for x"` prints "this package cannot be published yet: publishing is not configured for x", and the zip is still written. **`TestPackageBuildIsQuietWhenPublishingIsAvailable`** checks `null`.
- **`TestValidateReturnsAnUnavailablePortalAsAnAnswerNotAnError`**: so `run` proceeds on a portal with no bucket yet, such as staging before `PackageBuckets` is set.
- **`TestPackageBuildOnAServerWithoutTheRouteStillBuilds`**: a 404 is a warning.
- **`TestPackageBuildRefusesAnOutputItWouldCollect`**.
- **`TestPackageBuildRefusesANameThatIsAPath`**: on a server without the validate route, a manifest named `../../escaped` is `INVALID_MANIFEST`, and no zip is written anywhere.
- **`TestPackageRunKeepsTheServersCode`**: `packageRunError` given applies' 503 `SERVICE_UNAVAILABLE` returns that code with exit 5, and given `NOT_AUTHENTICATED` exit 3.
- **`TestPackageRunAsksTheServersMatcher`**: the request carries the patterns and the assignment URLs, and a "does not apply" answer comes back as the verdict.
- **`TestPackageRunOnAServerWithoutTheRouteIsUnconfirmed`**.
- **`TestPackageRunAlwaysSendsURLs`**: the applies body always carries `urls`, which REPORT-167 requires (a missing or `null` `urls` is 400 there).
- **`TestPackagePublishSendsTheZipAsTheBody`**: checks POST, `application/zip`, `Content-Length`, `origin=projects/20` present, `official` absent, and the bearer.
- **`TestPackagePublishRefusesAChecksumItDidNotSend`**.
- **`TestPackagePublishPassesTheServersCodeThrough`**: a 409 `ALREADY_EXISTS` keeps its code, exits 5, and is sent once.

| Mutation | Test that fails |
|---|---|
| a 404 from validate treated as a refusal | `TestPackageBuildOnAServerWithoutTheRouteStillBuilds` |
| `build` skips validate | `TestPackageBuildValidatesThenWrites`, `...WritesNothingTheServerRefuses` |
| assignment URLs not sent to applies | `TestPackageRunAsksTheServersMatcher` |
| no already-published warning | `TestPackageBuildWarnsOnAPublishedVersionAndStillWrites` |
| no unavailable-portal warning | `TestPackageBuildWarnsWhenThePortalCannotPublishAndStillWrites` |
| the warning printed when `publishing_unavailable` is `null` | `TestPackageBuildIsQuietWhenPublishingIsAvailable` |
| `publishing_unavailable` read under the wrong name | `TestPackageBuildWarnsWhenThePortalCannotPublishAndStillWrites`, `TestValidateReturnsAnUnavailablePortalAsAnAnswerNotAnError` |
| `official` not sent to validate | `TestPackageBuildValidatesAsThePublishWillBe` |
| `packageRunError` without its `CLIError` case | `TestPackageRunKeepsTheServersCode` (exit 1, `INTERNAL`) |
| the default output name not checked | `TestPackageBuildRefusesANameThatIsAPath` (the zip lands two directories up) |
| `already_published` read under the wrong name | `TestPackageBuildWarnsOnAPublishedVersionAndStillWrites`, `TestValidateReturnsAPublishedVersionAsAnAnswerNotAnError` |

---

### Documentation

**Summary**: R24. The README's "Commands (sketch)" gains the four `package` lines. The researcher guide gains a section, "Developing and publishing a package", with these parts:
- the loop (`init`, edit, `run`, `build`, `publish`);
- the scope file's four keys, with an example, kept outside the package or under a dot name such as `.scope.json` so `build` never ships a class hash;
- what `run` reproduces and what it cannot (the sandbox, the passthrough variables, python3.11);
- that `build` and `run` check against report-server, and the warning an older report-server gives;
- `.cc-data-run/` and `.cc-data-build/` (gitignored, and holding student data);
- the 422 "publishing is not configured" answer a portal gives before its bucket exists.

`skill_header.md` gains a short "Packages" list naming the four commands. The `--help` texts above already state the differences between a local run and a VM.

**Files affected**:
- `README.md`.
- `docs/researcher-guide.md`.
- `internal/guidance/src/skill_header.md`.

**Estimated diff size**: ~90 lines

---

### Pre-release tags in the release workflow

**Summary**: R25. `.github/workflows/release.yml` classifies the tag once, and the release and formula steps both read that answer: a pre-release tag (a `-` after the version, as in `v0.3.0-pre.1`) creates a GitHub pre-release with every archive and checksum, and skips the Homebrew formula. The archives upload inside `gh release create` itself, before the formula step, so skipping the formula cannot lose them. A test in `release_test.go`, beside the workflow's other guards, pins both uses.

**Files affected**:
- `.github/workflows/release.yml`: one step, one flag, one condition.
- `release_test.go`: one test.
- `README.md`: one sentence in the release paragraph.

**Estimated diff size**: ~40 lines

```diff
       - name: Create GitHub release
         env:
           GH_TOKEN: ${{ github.token }}
         run: |
           # Attach the checksum manifests alongside the tarballs so consumers can
           # verify downloads independently.
           gh release create "${GITHUB_REF_NAME}" dist/*.tar.gz dist/*.sha256 \
-            --title "${GITHUB_REF_NAME}" --generate-notes
+            --title "${GITHUB_REF_NAME}" --generate-notes \
+            --prerelease=${{ steps.tag.outputs.prerelease }}
 
       - name: Render and push the Homebrew formula
+        if: steps.tag.outputs.prerelease == 'false'
         env:
```

and, before "Create GitHub release":

```yaml
      # A tag with a semver pre-release suffix (v0.3.0-pre.1) is published for pinning,
      # by the runner image and package workflows, and never offered as the current
      # release: GitHub does not mark it latest and Homebrew never sees it.
      - name: Classify the tag
        id: tag
        run: |
          if [[ "${GITHUB_REF_NAME}" == *-* ]]; then
            echo "prerelease=true" >> "${GITHUB_OUTPUT}"
          else
            echo "prerelease=false" >> "${GITHUB_OUTPUT}"
          fi
```

```go
// A pre-release tag exists to be pinned, so it must not become GitHub's latest release or
// reach researchers through brew upgrade. Both steps read the one classification.
func TestReleaseWorkflowKeepsPreReleasesBack(t *testing.T) {
	rel := readFile(t, ".github/workflows/release.yml")
	for _, want := range []string{
		`if [[ "${GITHUB_REF_NAME}" == *-* ]]; then`,
		"--prerelease=${{ steps.tag.outputs.prerelease }}",
		"if: steps.tag.outputs.prerelease == 'false'",
	} {
		if !strings.Contains(rel, want) {
			t.Fatalf("release workflow must hold a pre-release tag back from latest and from Homebrew; missing %q", want)
		}
	}
}
```

README, after the sentence naming the Homebrew formula: "A tag with a pre-release suffix, such as `v0.3.0-pre.1`, publishes a GitHub pre-release with the same archives and no formula, for pinning by the runner image and package workflows."

Checked in a scratch copy: the workflow still parses, with the new step between "Verify checksums" and "Create GitHub release"; the classification writes `prerelease=true` for `v0.3.0-pre.1` and `false` for `v0.3.0`; and `gh release create` (2.100.0) parses `--prerelease=false` as a boolean, where `--prerelease=bogus` is refused.

| Mutation | Test that fails |
|---|---|
| the formula step's condition removed | `TestReleaseWorkflowKeepsPreReleasesBack` |

## Open Questions

<!-- Implementation-focused questions only. Requirements questions go in requirements.md. -->

### RESOLVED: Judgment call: how a raw zip body reaches the server
**Options considered**:
- A) Generalize `Client.do` into `send` with a content type.
- B) Build a separate `http.Request` for the zip routes.

**Decision**: A. Publish and validate then share the bearer, the cross-origin redirect refusal, the no-retry rule for POST and the error-envelope decoding with every other call. B would re-implement each of them.

### RESOLVED: Judgment call: the exit class for a refused or failed package
**Options considered**:
- A) Exit 1 with codes `PACKAGE_REFUSED`, `PACKAGE_FAILED` and `PACKAGE_OUTPUT_REFUSED`.
- B) A new exit code 7.

**Decision**: A. The root help defines exit 1 as "internal/other error", and the JSON envelope's code already distinguishes the cases. A new exit class is a contract change for every script that reads cc-data's exit codes.

### RESOLVED: Judgment call: following no link without `O_NOFOLLOW`
**Options considered**:
- A) `Lstat`, then open, then `os.SameFile`.
- B) `syscall.O_NOFOLLOW`.

**Decision**: A. CI tests on `windows-2022`, where `syscall.O_NOFOLLOW` does not exist. On a laptop the race A leaves open would need the author's own package to swap a file mid-read, and the runner, which runs as root, is the boundary that matters.

### RESOLVED: Judgment call: what a 404 from the new routes means
**Options considered**:
- A) Any 404 from validate or applies means the route is not deployed: warn and proceed.
- B) Require a specific error code before treating it as "not deployed".

**Decision**: A. report-server's router has no route either path could match today (`/packages/validate` and `/packages/applies` are one segment, and the only other `POST /packages/...` route takes four). So a 404 can only mean the route is missing, and B would need a code that an older server, by definition, cannot send.

### RESOLVED: Low confidence: the process-group kill on macOS
**Context**: `TestRunKillsAtTheBound` ran on Linux only.

**Decision**: `Setpgid` and `kill(-pgid, SIGKILL)` are POSIX and behave the same on Darwin, and `GOOS=darwin go build ./internal/packages/` compiles. CI's `macos-15` and `macos-15-intel` jobs run the test, so a Darwin difference would fail there.

## Self-Review

Roles: commit reviewer, test writer, senior engineer, operator, security engineer. Each finding was checked against the scratch build before it was written down.

### Senior Engineer

#### RESOLVED: A shell entrypoint would run locally and be refused on the VM
The zip originally gave every entry mode 0644, and the runner executes a non-`.py` entrypoint directly. A Go zip with a 0644 `plain.sh` and a 0755 `exec.sh`, unpacked with `unzip`, gives "Permission denied" for the first and runs the second, so `unzip` restores the zip's modes. Locally, the staged copy kept the source's 0755, so `package run` passed. Fixed:
- `Files` records a ship mode per file (0755 if any execute bit is set, else 0644).
- `Zip` and the staging copy both use it.
- `CheckEntrypointMode` refuses a non-executable non-`.py` entrypoint in `run` and `build`.

R17 states the rule.

#### RESOLVED: `RunOptions.Now` was never read
The field and its assignments were dead, so they are removed. Elapsed time is measured in `cmd/package.go`, where it is reported.

### Commit Reviewer

#### RESOLVED: Shared test helpers would not compile step by step
The scratch build kept every test in one file. The plan places each helper in the first step whose tests use it: `goodManifest` and `writeTree` in `files_test.go`, and the run helpers in `run_test.go`.

### Operator

#### RESOLVED: A scope file inside the package ships with it
An author naming the file `scope.json` in the package directory would publish a class hash in every archive. The documentation step says to keep it outside the package or under a dot name, which `Collect` already excludes.

#### RESOLVED: The applicability call would time out before the deriver finishes
The deriver may spend up to 240 seconds on a scope's activities, and the client's per-attempt timeout for JSON calls is 60 seconds. `appliesFor` copies the client with a 5-minute `RequestTimeout`, which also keeps the shared client's default for every other call.

### Second round

Roles: senior engineer, package author, security engineer, operator, and a reviewer for one source of truth. Every finding was confirmed against `main`, against the plan's code extracted into a scratch copy, or against the runner and REPORT-167 sources, and each had one defensible fix, which has been applied above. The scratch copy built and passed `go test ./...` on Linux with the fixes, and `go vet` was clean for `internal/packages` under `GOOS=windows` and `GOOS=darwin`.

#### RESOLVED: An applies failure lost its code and exit class (Senior Engineer)
`Run` returns `appliesFor`'s `*output.CLIError` unchanged, and `packageRunError` matched only the three package error types, so it fell through to `Internalf`. A throwaway test fed it applies' 503 `SERVICE_UNAVAILABLE` and `NOT_AUTHENTICATED`, and both came back exit 1 `INTERNAL`, against R16. `packageRunError` now returns a `CLIError` as it is, and `TestPackageRunKeepsTheServersCode` pins it.

#### RESOLVED: Ctrl-C left the package running (Package Author)
`Setpgid` takes the package out of the terminal's foreground group, and cc-data had no interrupt handling outside `dataset materialize`. Sending `SIGINT` to cc-data's group, as a terminal does, ended cc-data while the package's `sleep 30` kept running. `package run` now cancels on `os.Interrupt` (the `materialize` idiom), which kills the group through the existing `cmd.Cancel`, and `Run` reports "the package was interrupted". Rerun with the fix, the package was gone a second after the signal.

#### RESOLVED: `build`'s default path came from unchecked manifest fields (Security Engineer)
On a server without the validate route, a manifest named `../../escaped` built to `escaped-1.0.0.zip` two directories above the package, outside `.cc-data-build` and its `.gitignore`. `build` now refuses a name and version that make a path. It is a path check, not a copy of report-server's name grammar.

#### RESOLVED: The proxy variables were not passed through (Operator)
cc-data's client uses Go's default transport, which reads `HTTPS_PROXY`, and the runner sets the proxy variables when it has a proxy (`package-env.js`). Behind a proxy, `package run`'s own requests would succeed while the package's `cc-data` calls failed. They are now on the passthrough list.

#### RESOLVED: "No extra fields" in the zip was untrue (Senior Engineer)
`archive/zip` writes `Modified` as an extended-timestamp extra field (`5554...` on every entry of a scratch build). It is as fixed as the time, so R18 holds; the comment and R18 now say so.

#### RESOLVED: `publish` kept a local copy of the origin rule (One Source of Truth)
`publish` refused an `--origin` without the `projects/` prefix, `build` did not, and report-server answers the same case with 422 "origin must be projects/<id>; a user origin is always your own". The local check is deleted, so both commands give report-server's answer.

Checked and found sound: the R3 test (dropping `m.run_id = <run>` fails it at run 800, 2 learners against 1); a second `get answers` on a fetched run re-fetches and merges new answers, so the stub stays current; `reports list --json`'s `report_filter` carries `filters` and `class` as the stub reads them; `query --format json` answers an array of objects; report-server's 270-second wait sits between the deriver's 240 and cc-data's 300; an old report-server answers both new routes with a JSON 404 even without a token; and no publish check refuses an ordinary user's own origin, so `run`'s validate call cannot lock out a researcher who may use the API.

