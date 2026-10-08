# Implementation Plan: cc-data-cli: the package commands and the dataset views that replace `_lib`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-146
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Implementation Plan

The plan comes in six commits. Each one builds and passes `go test ./...` on its own, in this order. All of the code below was compiled and tested in a scratch copy of `main` (`58c9172`) while this plan was written: the full suite passes, `go vet` is clean for Linux, and `GOOS=windows go vet` is clean for `internal/packages` and `internal/api`. A table under each step names mutations its tests catch; every one listed was applied and made a test fail. The scratch copy has been deleted.

The package rules and applicability are report-server's (requirements, Dependencies). cc-data calls `POST /api/v1/packages/validate` and `POST /api/v1/packages/applies`, which the companion report-service story adds. A 404 from either means an older report-server, and is a warning, not a failure. So this plan ships and works before that story lands, and gains its checks when it does.

### The run-scoped views: `run_answers` and `learner_endpoints`

**Summary**: R1 to R5. Adds two static views to `internal/duck/views.go`, documents them where the drift guards check, and tests the counts on a dataset where each wrong way of counting gives a different number. Static views are derived everywhere else, so `cmd/query.go`'s help and the MCP `query` description pick both up with no edit.

**Files affected**:
- `internal/duck/views.go`: two builders and a helper, registered after `runMembershipView`.
- `internal/duck/run_views_test.go`: new.
- `internal/guidance/src/core.md`: two Views entries and the freshness query.
- `docs/researcher-guide.md`: two table rows.

**Estimated diff size**: ~230 lines

```diff
--- /tmp/o	2026-10-08 05:22:45.429149300 -0400
+++ internal/duck/views.go	2026-10-07 17:23:24.424211223 -0400
@@ -8,6 +8,7 @@
 	"io"
 	"os"
 	"path/filepath"
+	"regexp"
 	"sort"
 	"strings"
 	"time"
@@ -71,6 +72,8 @@
 	stmts = append(stmts, vs.storeView(store.TypeAnswers))
 	stmts = append(stmts, vs.storeView(store.TypeHistory))
 	stmts = append(stmts, vs.runMembershipView())
+	stmts = append(stmts, vs.runAnswersView())
+	stmts = append(stmts, vs.learnerEndpointsView())
 	stmts = append(stmts, vs.downloadsView())
 	stmts = append(stmts, vs.attachmentFilesView())
 	stmts = append(stmts, vs.attachmentStatesView())
@@ -360,6 +363,88 @@
 	return viewStmt{name: name, primary: primary, fallback: fallback, files: files}
 }
 
+// runAnswersView is every answer once per Student Answers run whose membership holds it: the
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
+// resEndpointColumn is an answers report's per-assignment endpoint column; N is the
+// assignment's position in that run's list.
+var resEndpointColumn = regexp.MustCompile(`^res_([0-9]+)_remote_endpoint$`)
+
+// learnerEndpointsView is one row per learner per assignment of every answers-type report CSV:
+// the Portal user, the assignment's offering and the endpoint that joins them to run_answers.
+// A student has one endpoint per assignment, which is why learners are counted by user_id.
+// Each CSV is scanned once, its assignment columns unnested as structs, because a real class
+// carries eight to fourteen of them.
+func (vs viewSet) learnerEndpointsView() viewStmt {
+	name := vs.prefix + `"learner_endpoints"`
+	standIn := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS BIGINT) AS run_id, CAST(NULL AS BIGINT) AS user_id, CAST(NULL AS BIGINT) AS offering_id, CAST(NULL AS VARCHAR) AS remote_endpoint WHERE false", name)
+	var members, files []string
+	for _, dl := range vs.m.Downloads {
+		if dl.Type != "report" || dl.ReportType != dataset.ReportTypeAnswers || len(dl.Files) == 0 || dl.Columns == nil {
+			continue
+		}
+		structs := learnerStructs(dl)
+		if _, ok := dl.Columns["user_id"]; !ok || len(structs) == 0 {
+			continue
+		}
+		if fileMissing(filepath.Join(vs.canonDir, dl.Files[0])) {
+			vs.warnf("report CSV %s is missing on disk; contributing zero rows to learner_endpoints (run cc-data dataset reindex)", dl.Files[0])
+			continue
+		}
+		members = append(members, fmt.Sprintf(
+			"SELECT CAST(run_id AS BIGINT) AS run_id, TRY_CAST(user_id AS BIGINT) AS user_id, unnest([%s]) AS a FROM (%s)",
+			strings.Join(structs, ", "), vs.csvScan(dl, true, nil)))
+		files = append(files, dl.Files...)
+	}
+	if len(members) == 0 {
+		return viewStmt{name: name, primary: standIn, fallback: standIn}
+	}
+	// A bare endpoint ending in "/" is a learner with no secure key, shared by every such
+	// learner, so it is withheld as in the dimension views rather than joined.
+	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT run_id, user_id, a.offering_id AS offering_id, "+
+		"CASE WHEN a.remote_endpoint LIKE %s THEN NULL ELSE a.remote_endpoint END AS remote_endpoint FROM (\n%s\n) WHERE a.remote_endpoint IS NOT NULL",
+		name, sqlStr("%/"), strings.Join(members, "\nUNION ALL\n"))
+	return viewStmt{name: name, primary: primary, fallback: standIn, files: files}
+}
+
+// learnerStructs is one {offering_id, remote_endpoint} struct per assignment the CSV records,
+// in assignment order. A CSV without an assignment's offering_id gives that struct a NULL one.
+func learnerStructs(dl dataset.Download) []string {
+	type res struct {
+		n   int
+		col string
+	}
+	var found []res
+	for col := range dl.Columns {
+		if m := resEndpointColumn.FindStringSubmatch(col); m != nil {
+			var n int
+			fmt.Sscanf(m[1], "%d", &n)
+			found = append(found, res{n, col})
+		}
+	}
+	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
+	structs := make([]string, 0, len(found))
+	for _, r := range found {
+		offering := "CAST(NULL AS BIGINT)"
+		if col := fmt.Sprintf("res_%d_offering_id", r.n); dl.Columns[col] != "" {
+			offering = fmt.Sprintf("TRY_CAST(%s AS BIGINT)", sqlIdent(col))
+		}
+		structs = append(structs, fmt.Sprintf("{'offering_id': %s, 'remote_endpoint': CAST(%s AS VARCHAR)}", offering, sqlIdent(r.col)))
+	}
+	return structs
+}
+
 // downloadsView is a VALUES dimension table from the manifest.
 //
 // hide_names separates the two meanings of a name column: where a run hid names, student_name
```

`learner_endpoints` scans each CSV once and unnests one `{offering_id, remote_endpoint}` struct per assignment. Scanning once per assignment column would read a real class's CSV eight to fourteen times. The view declares its CSVs as files, so `dataset materialize` writes it. `run_answers` declares none, so materializing reads it through the materialized `answers` and `run_membership`. Both behaviors were run against a copy of `staging-smoketest`.

```go
package duck

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// Each wrong way to count gives a different number here: counting endpoints instead of users
// (learner 1 answered in two assignments), joining the bare-endpoint learner (who would
// borrow learner 4's answer), counting an answer once instead of once per run (e3/q1 is in
// runs 584 and 585), leaking class 2's run into class 1's, and joining membership untyped
// (run 584's history holds e1/q1 too).
func TestRunAnswersAndLearnerEndpointsCountLikeLib(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 584, [][]byte{
		answerRec("s", "https://p/d/e1", "q1", "a"), answerRec("s", "https://p/d/e1b", "q1", "b"),
		answerRec("s", "https://p/d/e3", "q1", "c"), answerRec("s", "https://p/d/", "q1", "bare"),
	})
	hist, _ := json.Marshal(map[string]any{"source_key": "s", "remote_endpoint": "https://p/d/e1", "question_id": "q1", "history_id": "h1"})
	seg := store.OpenSegment(d.Dir, store.TypeHistory, 584)
	if err := seg.AppendPage([][]byte{hist}, time.Unix(584, 0).UTC(), 584); err != nil {
		t.Fatal(err)
	}
	seg.WriteCursor(&store.Cursor{Items: 1})
	if _, err := d.MergeCompact(store.TypeHistory, 584, seg); err != nil {
		t.Fatal(err)
	}
	buildStore(t, d, 585, [][]byte{answerRec("s", "https://p/d/e3", "q1", "c"), answerRec("s", "https://p/d/e9", "q1", "z")})
	addReportCSV(t, d, 584, "answers", "student_id,user_id,class_id,res_1_offering_id,res_1_remote_endpoint,res_2_offering_id,res_2_remote_endpoint\n"+
		"Prompt,,,,,,\nCorrect answer,,,,,,\n"+
		"10,1,1,7,https://p/d/e1,8,https://p/d/e1b\n"+
		"30,3,1,7,https://p/d/e3,8,\n"+
		"40,4,1,7,https://p/d/,8,\n")
	addReportCSV(t, d, 585, "answers", "student_id,user_id,class_id,res_1_offering_id,res_1_remote_endpoint\n"+
		"Prompt,,,,\nCorrect answer,,,,\n30,3,2,9,https://p/d/e3\n90,9,2,9,https://p/d/e9\n")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)

	if n := queryInt(t, e, "SELECT count(*) FROM run_answers WHERE run_id = 584"); n != 4 {
		t.Errorf("run 584 answers = %d, want 4", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM run_answers WHERE run_id = 585"); n != 2 {
		t.Errorf("run 585 answers = %d, want 2", n)
	}
	learners := "SELECT count(DISTINCT e.user_id) FROM learner_endpoints e JOIN run_answers a USING (run_id, remote_endpoint) WHERE run_id = %d"
	if n := queryInt(t, e, fmt.Sprintf(learners, 584)); n != 2 {
		t.Errorf("run 584 learners = %d, want 2 (users 1 and 3; user 4's endpoint is bare)", n)
	}
	if n := queryInt(t, e, fmt.Sprintf(learners, 585)); n != 2 {
		t.Errorf("run 585 learners = %d, want 2", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM learner_endpoints WHERE remote_endpoint IS NULL AND user_id = 4"); n != 1 {
		t.Errorf("the bare endpoint was not nulled")
	}
	if n := queryInt(t, e, "SELECT count(*) FROM learner_endpoints WHERE run_id = 584 AND offering_id = 8"); n != 1 {
		t.Errorf("assignment 2's offering_id did not follow its endpoint")
	}
	var types string
	rows, _ := e.Query(t.Context(), "SELECT string_agg(column_type, ',' ORDER BY column_name) FROM (DESCRIBE learner_endpoints)")
	rows.Next()
	rows.Scan(&types)
	rows.Close()
	if types != "BIGINT,VARCHAR,BIGINT,BIGINT" {
		t.Errorf("learner_endpoints types (offering_id, remote_endpoint, run_id, user_id) = %s", types)
	}
}

func TestRunViewsAreEmptyNotMissingOnAFreshDataset(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: newDS(t, "ds")}}, nil)
	for _, v := range []string{"run_answers", "learner_endpoints"} {
		if n := queryInt(t, e, "SELECT count(*) FROM "+v); n != 0 {
			t.Errorf("%s has %d rows", v, n)
		}
	}
	if n := queryInt(t, e, "SELECT count(*) FROM (DESCRIBE learner_endpoints)"); n != 4 {
		t.Errorf("learner_endpoints stand-in has %d columns, want 4", n)
	}
}
```

| Mutation | Test that fails |
|---|---|
| bare endpoints not nulled | learners for run 584 = 3 |
| `m.type = 'answers'` dropped | run 584 answers = 5 (the history membership joins) |
| offering taken from assignment 1 | "assignment 2's offering_id did not follow its endpoint" |
| `run_id` left uncast | the DESCRIBE type check (INTEGER) |

Documentation (the two guards in `internal/guidance/guard_test.go` fail until both files carry the names; the dashes in `core.md` match that list's existing entries):

```diff
--- /tmp/o	2026-10-08 05:22:45.441149321 -0400
+++ internal/guidance/src/core.md	2026-10-07 17:21:28.050911252 -0400
@@ -122,6 +122,23 @@
   **type-qualified**: `answers a JOIN run_membership m USING
   (source_key, remote_endpoint, question_id) WHERE m.run_id = 584 AND m.type =
   'answers'`. History joins add `history_id` to the USING list.
+- `run_answers` — every `answers` row with `run_id`, once for each Student Answers
+  run whose membership holds it, so one run's answers are `SELECT count(*) FROM
+  run_answers WHERE run_id = 584`. It is the type-qualified membership join above,
+  always present, so a run with no answers counts zero rather than failing to bind
+  as `answers_<run>` does. Its `run_id` is membership; the store's own `_run_id` is
+  only the run that last fetched the record, so filtering `answers` by `_run_id`
+  undercounts a run whose answers a later run re-fetched.
+- `learner_endpoints` — one row per learner per assignment in each answers-type
+  report CSV: `run_id`, `user_id` (the Portal user), `offering_id` and
+  `remote_endpoint`, built from every `res_<N>_remote_endpoint` column. A bare
+  endpoint ending in `/` (a learner with no secure key) is NULL, as in
+  `student_id_mapping`. Count a run's learners with an answer by `user_id`, never by
+  endpoint, since a student has one endpoint per assignment: `SELECT count(DISTINCT
+  e.user_id) FROM learner_endpoints e JOIN run_answers a USING (run_id,
+  remote_endpoint) WHERE run_id = 584`.
+- A run's log freshness is `SELECT count(*) AS logs, max(event_time) AS
+  log_freshness_at FROM logs WHERE run_id = <id>`.
 - Reports-to-stores join: `reports.res_<N>_remote_endpoint =
   answers.remote_endpoint`, with `res_<N>_<question_id>_*` pairing to
   `answers.question_id`.
--- /tmp/o	2026-10-08 05:22:45.444149326 -0400
+++ docs/researcher-guide.md	2026-10-07 17:21:28.050652765 -0400
@@ -346,6 +346,8 @@
 | `attachment_content` | The text/JSON content of every saved CODAP/SageModeler snapshot, queryable and diffable. |
 | `student_id_mapping` | One row per learner from Student ID Mapping runs, with the ids that join them to their answers and history. |
 | `student_metadata` | One row per learner from Student Metadata runs: name, username, class, school, teachers, permission forms. |
+| `run_answers` | Every answer with the `run_id` of each run that holds it: `SELECT count(*) FROM run_answers WHERE run_id = 584` counts one run's answers. |
+| `learner_endpoints` | One row per learner per assignment in each Student Answers report: `user_id`, `offering_id` and the `remote_endpoint` that joins them to `run_answers`. Count a run's learners by `user_id`, since one student has an endpoint per assignment. |
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
	"bytes"
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

// ReadManifest reads the fields package run needs to stage and start a package: the
// entrypoint, which must be one of files, the duration that sets the time bound, the patterns
// and clue_prepull. A wrongly typed field is refused here because the run cannot proceed
// without it; the full rules are report-server's.
func ReadManifest(data []byte, files map[string]bool) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest.json: %v", err)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(data, &raw)
	if !files[m.Entrypoint] || UnsafePath(m.Entrypoint) {
		return m, fmt.Errorf("manifest.json: entrypoint %q is not a file in the package", m.Entrypoint)
	}
	if v := raw["expected_duration_seconds"]; len(v) == 0 || v[0] == '"' || bytes.ContainsAny(v, ".eE") || m.ExpectedDurationSeconds < 1 {
		return m, fmt.Errorf("manifest.json: expected_duration_seconds must be a positive integer")
	}
	return m, nil
}

// UnsafePath is report-server's Archive.unsafe_path?: absolute, a drive letter, or a ".." segment.
func UnsafePath(name string) bool {
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

// excluded is what a build leaves behind: anything under a segment starting with ".",
// which covers this tool's own .cc-data-run and .cc-data-build as well as .git, and the
// three things cc-data-studies ignores.
func excluded(rel string, isDir bool) bool {
	base := path.Base(rel)
	if strings.HasPrefix(base, ".") || base == "__pycache__" || (isDir && base == "local-data") {
		return true
	}
	return !isDir && strings.HasSuffix(base, ".pyc")
}

// Collect walks dir for the files a package is made of. A symbolic link or any other
// non-regular file is refused rather than skipped, since the runner refuses an archive
// holding one and a link silently dropped would be a package that differs from its tree.
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

// Zip writes the files as a reproducible archive: sorted entries, Deflate, a fixed time, each
// file's ship mode, and no extra fields. The size limits are report-server's to apply.
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
// since the runner executes such an entrypoint directly. Windows records no execute bit, so
// there the check cannot be made.
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

// DisplayLimitBytes is the cap on display.md, counted in UTF-8 bytes. final-design.md
// section 10 is the only place this number is decided; the runner and this constant both
// cite it, because a local cap looser than the runner's is a check that passes here and a
// VM that then refuses.
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
- **`TestModesSurviveBuildAndStaging`**: `run.py` (0755 on disk) zips as 0755 and a 0600 `manifest.json` as 0644. A 0644 `run.sh` entrypoint fails `CheckEntrypointMode`, and a 0644 `run.py` passes it.
- **`TestDisplayCapEdges`**: 65,536 bytes pass and 65,537 are refused, and the constant equals 65,536.
- **`TestResultReadsOnlyTheRunnersKeys`**: the summary is the first line, trimmed, and `learners` in `counts.json` is dropped.
- **`TestResultRefusesALinkedDisplay`**.

| Mutation | Test that fails |
|---|---|
| `size >= DisplayLimitBytes` | `TestDisplayCapEdges` |
| `__pycache__` not excluded | `TestCollectLeavesOutWhatBuildMustNotShip` |
| every entry zipped 0644 | `TestModesSurviveBuildAndStaging` |
| a quoted duration accepted | `TestReadManifestNeedsWhatTheRunActsOn` |

---

### `package run`'s engine: scope, layout, environment, execution

**Summary**: R10 to R16.
- **Scope.** Parses the author's scope file.
- **Layout.** Rebuilds `.cc-data-run/` (`pkg/`, `in/`, `out/`, `data/` and a `.gitignore` of `*`) and writes the runner's `scope.json`.
- **Applicability.** Asks an injected `Applies` function, which the commands step wires to report-server.
- **Environment and interpreter.** Builds the runner's environment plus the passthrough list, and picks the interpreter.
- **Execution.** Runs the entrypoint from the staged copy in its own process group, and kills the group at the bound.
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

// Class and Assignment are scope.json's entries (final-design.md section 10).
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
// calls find the researcher's stored credential (keychain or credentials file) and run at
// all. The runner sets nothing beyond its own variables, so neither does this.
var passthrough = []string{
	"HOME", "PATH", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
	"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR",
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
		return Result{}, &Refused{Reason: "the package sets clue_prepull, and cc-data cannot fetch CLUE data yet (final-design.md section 13), so it runs only on the VM"}
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
	if err := cmd.Run(); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return Result{}, &Failed{Reason: fmt.Sprintf("the package ran past its %ds bound", int(timeout.Seconds()))}
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
```
```go
//go:build windows

package packages

import "os/exec"

// killGroup kills only the entrypoint on Windows, which has no release.
func killGroup(cmd *exec.Cmd) {}
```

Tests (the entrypoint is a `/bin/sh` script, so these skip on Windows):
- **`TestLocalScopeRefusesTheRunnersKeys`**: a scope file setting `dataset` is refused.
- **`TestRunGivesThePackageTheRunnersLayout`**:
  - The package dumps its environment, copies `scope.json` and lists its working directory.
  - The environment carries `RD_DATASET` (the full ref), `CC_DATA_PORTAL`, `RD_SCOPE_FILE` and a passed-through `LC_ALL`, and not an injected `AWS_SECRET_ACCESS_KEY`.
  - `scope.json` has the bare `dataset`, `clue_source: "firebase"` and the absolute `output_dir`.
  - The working directory has no `local-data/`, and `.gitignore` is `*`.
- **`TestRunRefusesBeforeStarting`**: when `Applies` says no, the error is the runner's prefix plus the reason, and the entrypoint never starts. A `clue_prepull` package is refused.
- **`TestRunWarnsAndRunsWhenApplicabilityIsUnconfirmed`**: an unconfirmed verdict prints the warning and runs.
- **`TestRunKillsAtTheBound`**: `sleep 30 & wait` under a 300 ms bound fails as "past its bound" within 5 seconds.
- **`TestRunRefusesALinkedRunDirectory`**: `.cc-data-run` linked to another directory is refused, and a file in that directory survives.

| Mutation | Test that fails |
|---|---|
| no `Setpgid` | `TestRunKillsAtTheBound` (the orphaned `sleep` holds the output pipe for 30 s) |
| link check on the run directory removed | `TestRunRefusesALinkedRunDirectory` |
| the whole environment inherited | `TestRunGivesThePackageTheRunnersLayout` |
| run from the source directory, not the staged copy | `TestRunGivesThePackageTheRunnersLayout` |
| a "does not apply" verdict ignored | `TestRunRefusesBeforeStarting` |
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
+// send is do with the body's media type named, for the one route that takes raw bytes.
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

// PublishedPackage is report-server's answer to a publish (REPORT-142).
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
	q := url.Values{}
	if origin != "" {
		q.Set("origin", origin)
	}
	if official {
		q.Set("official", "true")
	}
	data, err := c.send(ctx, http.MethodPost, "/api/v1/packages", q, archive, "application/zip")
	if err != nil {
		return nil, err
	}
	var out PublishedPackage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ValidatedPackage is what a publish of the archive would record, as report-server's validate
// route answers it without recording anything.
type ValidatedPackage struct {
	Identity   string `json:"identity"`
	Version    string `json:"version"`
	Checksum   string `json:"checksum"`
	Visibility string `json:"visibility"`
}

// ValidatePackage asks report-server to apply every publish check to the archive and stop
// before storing it. A refusal is the same coded error a publish would answer.
func (c *Client) ValidatePackage(ctx context.Context, archive []byte, origin string) (*ValidatedPackage, error) {
	q := url.Values{}
	if origin != "" {
		q.Set("origin", origin)
	}
	data, err := c.send(ctx, http.MethodPost, "/api/v1/packages/validate", q, archive, "application/zip")
	if err != nil {
		return nil, err
	}
	var out ValidatedPackage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppliesRequest names the patterns and the scope's URLs. AssignmentURLs are followed into
// their activities by report-service's deriver, so the URLs matched are the runner's.
type AppliesRequest struct {
	URLs           map[string][]string `json:"urls"`
	AssignmentURLs []string            `json:"assignment_urls,omitempty"`
	ScopeURLs      []string            `json:"scope_urls,omitempty"`
}

// AppliesAnswer is report-server's verdict, with what the deriver read and could not.
type AppliesAnswer struct {
	Applies         bool     `json:"applies"`
	Reason          string   `json:"reason"`
	InteractiveURLs []string `json:"interactive_urls"`
	Unread          []struct {
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
- **The report-server calls.** `validate` is shared by `build` and `run`. `appliesFor` gives `run` its `Applies` function, with a 5-minute timeout for the deriver.
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
			ctx := context.Background()
			archive, err := packages.Zip(files)
			if err != nil {
				return output.Internalf("%v", err)
			}
			if err := validate(ctx, client, archive, ""); err != nil {
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

// validate asks report-server whether a publish of the archive would be accepted. A
// report-server without the route is not a refusal: the archive is unchecked, and saying so
// is the whole answer.
func validate(ctx context.Context, client *api.Client, archive []byte, origin string) error {
	_, err := client.ValidatePackage(ctx, archive, origin)
	switch {
	case api.RouteMissing(err):
		output.Warnf("report-server has no validate route yet, so this package is not checked against the catalog's rules until publish")
		return nil
	case err != nil:
		return api.AsCLIError(err)
	}
	return nil
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
	switch {
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
		out = filepath.Join(dir, packages.BuildDirName, fmt.Sprintf("%s-%s.zip", m.Name, m.Version))
	} else if err := packages.CheckOutsidePackage(dir, out); err != nil {
		return output.Usagef("%v", err)
	}
	archive, err := packages.Zip(files)
	if err != nil {
		return output.Internalf("%v", err)
	}
	if err := validate(ctx, client, archive, f.origin); err != nil {
		return err
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
			if f.origin != "" && !strings.HasPrefix(f.origin, "projects/") {
				return output.Usagef("--origin must be projects/<id>; your own packages need no origin")
			}
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

Two limits to know before building on it:
- It creates a Student Answers run for the class the first time it meets one, on the server
  the dataset's portal names. Later runs reuse that run and re-pull it.
- It filters the whole class in one run. Athena fails a whole-class Student Answers run above
  about four assignments; chunking by scope["assignments"] is the fix.
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


def answers_run(dataset, class_id):
    """The class's Student Answers run: one already in the dataset, else a new one."""
    # downloads first: until a report is pulled, the reports view has no class_id to filter on.
    pulled = query(dataset, "SELECT run_id FROM downloads WHERE type = 'report' AND slug = 'student-answers'")
    if pulled:
        ids = ", ".join(str(int(row["run_id"])) for row in pulled)
        found = query(dataset, f"SELECT max(run_id) AS run_id FROM reports WHERE run_id IN ({ids}) AND class_id = {int(class_id)}")
        if found and found[0]["run_id"] is not None:
            return int(found[0]["run_id"])
    created = json.loads(cc_data(
        "reports", "create", "--portal", os.environ["CC_DATA_PORTAL"],
        "--report-slug", "student-answers",
        "--report-filter", json.dumps({"class": [int(class_id)]}), "--json"))
    return int(created["run"]["run_id"])


def main():
    with open(os.environ["RD_SCOPE_FILE"], encoding="utf-8") as handle:
        scope = json.load(handle)
    # RD_DATASET is the full <portal>/<name> ref; scope["dataset"] is the bare name.
    dataset = os.environ["RD_DATASET"]
    class_id = scope["classes"][0]["class_id"]

    try:
        cc_data("dataset", "create", dataset)
    except RuntimeError as err:
        if "exists" not in str(err).lower():
            raise
    run = answers_run(dataset, class_id)
    cc_data("get", "report", str(run), "--dataset", dataset, "--refresh")
    cc_data("get", "answers", str(run), "--dataset", dataset)

    rows = query(dataset, f"SELECT count(*) AS n FROM report_{run}")[0]["n"]
    if rows == 0:
        raise RuntimeError("no report-service access to this class, or the class has no data")
    answers = query(dataset, f"SELECT count(*) AS n FROM run_answers WHERE run_id = {run}")[0]["n"]
    learners = query(dataset, f"""
        SELECT count(DISTINCT e.user_id) AS n FROM learner_endpoints e
        JOIN run_answers a USING (run_id, remote_endpoint) WHERE run_id = {run}""")[0]["n"]

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

The stub was run through `Run` against a copy of `staging-smoketest`, using a wrapper `cc-data` that forwards `dataset` and `query` to the scratch build and turns `get` into a no-op.
- **The result.** It wrote "5 answers / 2 learners with at least one answer" and `counts.json` `{"answers": 5}`.
- **On an empty dataset** it called `reports create --report-slug student-answers --report-filter {"class": [6]}`.
- **Why it reads `downloads` first.** An earlier draft queried `reports.class_id` before any report existed, and the empty `reports` stand-in has no such column.

Tests (against an `httptest` server that answers 404 for any route it was not given):
- **`TestPackageInitRefusesASecondInit`**.
- **`TestPackageBuildValidatesThenWrites`**:
  - Exactly one validate request is made, carrying the bytes that were written, as `application/zip`.
  - The zip is `wildfire-responses-0.1.0.zip` with two files, and the printed checksum is the archive's own.
  - A second build from inside the package gives the same checksum.
- **`TestPackageBuildWritesNothingTheServerRefuses`**: a 422 `UNPROCESSABLE` keeps its code, exits 5, and no zip is written.
- **`TestPackageBuildOnAServerWithoutTheRouteStillBuilds`**: a 404 is a warning.
- **`TestPackageBuildRefusesAnOutputItWouldCollect`**.
- **`TestPackageRunAsksTheServersMatcher`**: the request carries the patterns and the assignment URLs, and a "does not apply" answer comes back as the verdict.
- **`TestPackageRunOnAServerWithoutTheRouteIsUnconfirmed`**.
- **`TestPackagePublishSendsTheZipAsTheBody`**: checks POST, `application/zip`, `Content-Length`, `origin=projects/20` present, `official` absent, and the bearer.
- **`TestPackagePublishRefusesAChecksumItDidNotSend`**.
- **`TestPackagePublishPassesTheServersCodeThrough`**: a 409 `ALREADY_EXISTS` keeps its code, exits 5, and is sent once.

| Mutation | Test that fails |
|---|---|
| a 404 from validate treated as a refusal | `TestPackageBuildOnAServerWithoutTheRouteStillBuilds` |
| `build` skips validate | `TestPackageBuildValidatesThenWrites`, `...WritesNothingTheServerRefuses` |
| assignment URLs not sent to applies | `TestPackageRunAsksTheServersMatcher` |

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
