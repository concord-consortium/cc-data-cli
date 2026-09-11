package duck

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// MaterializeOptions controls one materialize run.
type MaterializeOptions struct {
	Force        bool // rebuild a view whose recorded inputs are unchanged
	AllowPartial bool // build a view from what is on disk when a declared input is missing
	Progress     func(view string, i, n int)
}

// MaterializeResult reports what happened per view. Refused is keyed by view
// name and carries the reason for the caller to render.
type MaterializeResult struct {
	Written []string
	Skipped []string
	Refused map[string]string
}

// Refusals reports whether any view was refused, which is what makes the command
// exit non-zero.
func (r MaterializeResult) Refusals() bool { return len(r.Refused) > 0 }

// Materialize writes each materializable view to a ZSTD Parquet under the
// dataset's materialized folder. A view that cannot be copied is refused like
// any other, so one failure costs its own view rather than the whole run; only
// a cancelled context stops the run outright.
//
// The run holds the dataset's materialize guard from start to finish, so two
// runs never work the same dataset at once. The mutation locks are a different
// matter: they are held only to read the manifest at the start and repoint it at
// the end, so a concurrent get does not fail as busy for the whole run. A view
// whose inputs moved in between is discarded rather than recorded, because its
// Parquet describes a state that is already gone.
func Materialize(ctx context.Context, d *dataset.Dataset, opts MaterializeOptions, warnOut io.Writer) (MaterializeResult, error) {
	if warnOut == nil {
		warnOut = io.Discard
	}
	res := MaterializeResult{Refused: map[string]string{}}

	releaseRun, err := d.LockMaterialize()
	if err != nil {
		return res, err
	}
	defer releaseRun()

	m, err := readManifestLocked(d)
	if err != nil {
		return res, err
	}
	canon, err := canonicalize(d.Dir)
	if err != nil {
		return res, err
	}
	vs := viewSet{canonDir: canon, m: m}
	stmts := vs.statements()
	views := materializableFrom(stmts, vs.prefix)
	sort.Strings(views)
	filesByView := map[string][]string{}
	for _, st := range stmts {
		filesByView[bareViewName(st.name, vs.prefix)] = st.files
	}
	warnIncompleteDownloads(m, views, filesByView, warnOut)

	if err := os.MkdirAll(d.Path(dataset.MaterializedDir), 0o700); err != nil {
		return res, err
	}
	if err := sweepStaleTemps(d); err != nil {
		return res, err
	}
	e, degraded, err := OpenRaw(ctx, d, warnOut)
	if err != nil {
		return res, err
	}
	defer e.Close()

	built := map[string]builtView{}
	defer func() {
		for _, b := range built {
			os.Remove(d.Path(b.tmp))
		}
	}()

	for i, view := range views {
		if opts.Progress != nil {
			opts.Progress(view, i+1, len(views))
		}
		files := filesByView[view]
		if degraded[view] {
			res.Refused[view] = "registered from its typed-empty fallback, so every declared input is unreadable: " +
				strings.Join(files, ", ")
			continue
		}
		if missing := missingInputs(d.Dir, files); len(missing) > 0 {
			if !opts.AllowPartial {
				res.Refused[view] = missingInputReason(d, m, files, missing)
				continue
			}
			fmt.Fprintf(warnOut, "--allow-partial given; building %s from %d of %d declared inputs\n",
				view, len(files)-len(missing), len(files))
		}
		if !opts.Force {
			if rec, ok := m.Materialized[view]; ok && rec.Fresh(d.Dir, files) {
				res.Skipped = append(res.Skipped, view)
				continue
			}
		}
		// Fingerprinted before the copy, so the re-check at the repoint can see an
		// input that moved while the copy was running.
		inputs := dataset.FingerprintInputs(d.Dir, files)
		tmp, err := copyViewToParquet(ctx, e, d, view)
		if err != nil {
			if ctx.Err() != nil {
				return res, err
			}
			res.Refused[view] = err.Error()
			continue
		}
		if st, isStore := m.Stores[view]; isStore {
			n, err := parquetRowCount(ctx, e, d.Path(tmp))
			if err != nil {
				os.Remove(d.Path(tmp))
				if ctx.Err() != nil {
					return res, err
				}
				res.Refused[view] = err.Error()
				continue
			}
			if n != st.Count {
				os.Remove(d.Path(tmp))
				res.Refused[view] = fmt.Sprintf("copied %d rows but the manifest records %d in the %s store", n, st.Count, view)
				continue
			}
		}
		built[view] = builtView{tmp: tmp, inputs: inputs}
	}

	testHookBeforeRepoint()
	if err := repointManifest(d, built, &res); err != nil {
		return res, err
	}
	sort.Strings(res.Written)
	sort.Strings(res.Skipped)
	return res, nil
}

// testHookBeforeRepoint is a seam for driving the window between the copies and
// the repoint, matching the hook the merge path uses.
var testHookBeforeRepoint = func() {}

// builtView is one finished copy waiting to be committed: its temp file, and the
// input fingerprints taken just before it was made.
type builtView struct {
	tmp    string
	inputs map[string]string
}

func readManifestLocked(d *dataset.Dataset) (*dataset.Manifest, error) {
	release, err := d.LockMutation()
	if err != nil {
		return nil, err
	}
	defer release()
	return d.ReadManifest()
}

// repointManifest takes the locks back, keeps only the copies whose inputs still
// fingerprint as they did when the copy started, and renames those into place.
// The manifest write is the commit point, so a discarded copy leaves nothing
// behind and a committed one is already on disk under its final name.
func repointManifest(d *dataset.Dataset, built map[string]builtView, res *MaterializeResult) error {
	if len(built) == 0 {
		return nil
	}
	release, err := d.LockMutation()
	if err != nil {
		return err
	}
	defer release()

	m, err := d.ReadManifest()
	if err != nil {
		return err
	}
	current := declaredFiles(m)
	now := time.Now().UTC()
	for view, b := range built {
		files, stillAView := current[view]
		if !stillAView || !sameFingerprints(b.inputs, dataset.FingerprintInputs(d.Dir, files)) {
			res.Skipped = append(res.Skipped, view)
			continue
		}
		rec := dataset.Materialized{
			File:    filepath.ToSlash(filepath.Join(dataset.MaterializedDir, view+".parquet")),
			Inputs:  b.inputs,
			BuiltAt: now,
		}
		if err := fsutil.RenameAtomic(d.Path(b.tmp), d.Path(rec.File)); err != nil {
			return err
		}
		delete(built, view)
		m.Materialized[view] = rec
		res.Written = append(res.Written, view)
	}
	return d.WriteManifest(m)
}

// declaredFiles maps each materializable view to the files it currently reads.
func declaredFiles(m *dataset.Manifest) map[string][]string {
	vs := viewSet{m: m}
	stmts := vs.statements()
	out := map[string][]string{}
	eligible := map[string]bool{}
	for _, view := range materializableFrom(stmts, vs.prefix) {
		eligible[view] = true
	}
	for _, st := range stmts {
		if bare := bareViewName(st.name, vs.prefix); eligible[bare] {
			out[bare] = st.files
		}
	}
	return out
}

func sameFingerprints(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// tempParquetInfix marks an in-flight copy. The writer and the sweep share it so
// they cannot disagree about which files are unfinished work.
const tempParquetInfix = ".parquet.tmp-"

// sweepStaleTemps removes the temp files of a run that died before its own
// cleanup could run, which a hard interrupt does since nothing here installs a
// signal handler. The materialize guard is held, so a temp file present now
// belongs to no living run.
func sweepStaleTemps(d *dataset.Dataset) error {
	entries, err := os.ReadDir(d.Path(dataset.MaterializedDir))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), tempParquetInfix) {
			continue
		}
		if err := os.Remove(d.Path(filepath.Join(dataset.MaterializedDir, entry.Name()))); err != nil {
			return err
		}
	}
	return nil
}

// copyViewToParquet writes the view to a temp name in the derived folder, unique
// per call so the name cannot collide with a concurrent copy of another view.
func copyViewToParquet(ctx context.Context, e *Engine, d *dataset.Dataset, view string) (string, error) {
	f, err := os.CreateTemp(d.Path(dataset.MaterializedDir), view+tempParquetInfix+"*")
	if err != nil {
		return "", err
	}
	f.Close()
	tmp := filepath.ToSlash(filepath.Join(dataset.MaterializedDir, filepath.Base(f.Name())))
	stmt := fmt.Sprintf("COPY (SELECT * FROM %s) TO %s (FORMAT parquet, COMPRESSION zstd, KV_METADATA {view: %s, built_at: %s})",
		sqlIdent(view), sqlStr(d.Path(tmp)), sqlStr(view), sqlStr(time.Now().UTC().Format(time.RFC3339)))
	if err := e.exec(ctx, stmt); err != nil {
		os.Remove(d.Path(tmp))
		return "", fmt.Errorf("materializing %s: %w", view, err)
	}
	return tmp, nil
}

func parquetRowCount(ctx context.Context, e *Engine, path string) (int, error) {
	rows, err := e.Query(ctx, fmt.Sprintf("SELECT count(*) FROM read_parquet(%s)", sqlStr(path)))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

func missingInputs(dir string, files []string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, rel := range files {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if dataset.Fingerprint(filepath.Join(dir, rel)) == dataset.FingerprintAbsent {
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	return missing
}

// missingInputReason names both remedies, because the point of refusing is to
// surface them rather than to leave a flag to be typed on every run afterwards.
func missingInputReason(d *dataset.Dataset, m *dataset.Manifest, files, missing []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "declares %d inputs and %d %s missing: %s", len(files), len(missing),
		plural(len(missing), "is", "are"), strings.Join(missing, ", "))
	if runs := runsForFiles(m, missing); len(runs) > 0 {
		fmt.Fprintf(&b, "\n  re-fetch %s, or run cc-data dataset reindex %s to drop the entry so the file stops being expected",
			runList(runs), d.Ref.String())
	} else {
		fmt.Fprintf(&b, "\n  re-fetch the run, or run cc-data dataset reindex %s to drop the entry so the file stops being expected",
			d.Ref.String())
	}
	b.WriteString("\n  to build from what is on disk anyway, pass --allow-partial")
	return b.String()
}

func runsForFiles(m *dataset.Manifest, files []string) []int {
	want := map[string]bool{}
	for _, f := range files {
		want[f] = true
	}
	seen := map[int]bool{}
	var runs []int
	for _, dl := range m.Downloads {
		for _, f := range dl.Files {
			if want[f] && !seen[dl.RunID] {
				seen[dl.RunID] = true
				runs = append(runs, dl.RunID)
			}
		}
	}
	sort.Ints(runs)
	return runs
}

func runList(runs []int) string {
	parts := make([]string, len(runs))
	for i, r := range runs {
		parts[i] = fmt.Sprintf("%d", r)
	}
	return plural(len(runs), "run ", "runs ") + strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// warnIncompleteDownloads names the runs whose download is marked incomplete.
// It is a warning rather than a refusal: only a store fetch can set the flag,
// and it merges once at the end, so an incomplete store download leaves the
// Parquet byte-identical to one built if the run had never been requested.
//
// The view-to-download mapping is three rules because no single rule covers it.
// Report-backed views map by file intersection. The store views map by download
// type, since a store is one merged file across every run and carries no per-run
// identity. run_membership maps by type too: its filenames do encode (type, run),
// but an incomplete run has no membership file at all, so a per-run mapping would
// never see the run the warning is about.
func warnIncompleteDownloads(m *dataset.Manifest, views []string, filesByView map[string][]string, warnOut io.Writer) {
	for _, view := range views {
		var runs []int
		seen := map[int]bool{}
		add := func(run int) {
			if !seen[run] {
				seen[run] = true
				runs = append(runs, run)
			}
		}
		for _, dl := range m.Downloads {
			if dl.Complete {
				continue
			}
			if isStoreType(dl.Type) {
				if view == dl.Type || view == "run_membership" {
					add(dl.RunID)
				}
				continue
			}
			if intersects(dl.Files, filesByView[view]) {
				add(dl.RunID)
			}
		}
		if len(runs) > 0 {
			sort.Ints(runs)
			fmt.Fprintf(warnOut, "warning: %s reads %s marked incomplete; materializing it anyway\n", view, runList(runs))
		}
	}
}

func isStoreType(t string) bool { return t == store.TypeAnswers || t == store.TypeHistory }

func intersects(a, b []string) bool {
	seen := map[string]bool{}
	for _, f := range a {
		seen[f] = true
	}
	for _, f := range b {
		if seen[f] {
			return true
		}
	}
	return false
}

// StaleMaterializedViews names the views whose recorded Parquet no longer matches
// the inputs. It lives here, not in dataset, because a view's input list is a
// property of the view set.
func StaleMaterializedViews(d *dataset.Dataset) ([]string, error) {
	m, err := d.ReadManifest()
	if err != nil {
		return nil, err
	}
	if len(m.Materialized) == 0 {
		return nil, nil
	}
	current := declaredFiles(m)
	var stale []string
	for view, rec := range m.Materialized {
		files, ok := current[view]
		if !ok || !rec.Fresh(d.Dir, files) {
			stale = append(stale, view)
		}
	}
	sort.Strings(stale)
	return stale, nil
}

// AnnotateShowJSON appends the warnings only the view set can decide, and
// re-sorts. Both surfaces call it, so the same documented ShowJSON contract
// cannot carry different warning sets on the CLI and over MCP.
func AnnotateShowJSON(d *dataset.Dataset, s *dataset.ShowJSON) error {
	stale, err := StaleMaterializedViews(d)
	if err != nil {
		return err
	}
	for _, view := range stale {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"STALE_MATERIALIZED: view %s was materialized from inputs that have since changed; run cc-data dataset materialize %s to refresh it",
			view, d.Ref.String()))
	}
	sort.Strings(s.Warnings)
	return nil
}
