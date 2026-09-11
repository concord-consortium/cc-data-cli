package duck

import (
	"context"
	"errors"
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

// MaterializeStatus is what became of one view in a run. A view has exactly one.
type MaterializeStatus string

const (
	// StatusWritten: copied and now recorded in the manifest.
	StatusWritten MaterializeStatus = "written"
	// StatusFresh: the recorded Parquet still matches its inputs, so nothing was done.
	StatusFresh MaterializeStatus = "fresh"
	// StatusDiscarded: copied, then dropped because its inputs moved while the
	// copy ran. Nothing is wrong with the dataset and running again picks it up.
	StatusDiscarded MaterializeStatus = "discarded"
	// StatusRefused: deliberately not materialized, for the reason it carries.
	StatusRefused MaterializeStatus = "refused"
)

// ViewOutcome is one view's result. Reason says why a view was refused or
// discarded; on a written or fresh view it carries a caveat when there is one,
// a copy built from fewer inputs than the view declares or one that reads a
// download marked incomplete, and is empty otherwise. The caveat travels in the
// outcome rather than as a progress message because the MCP tool returns the
// outcome and may have nowhere to send progress.
type ViewOutcome struct {
	View   string            `json:"view"`
	Status MaterializeStatus `json:"status"`
	Reason string            `json:"reason,omitempty"`
}

// MaterializeResult carries one outcome per materializable view, in view order.
//
// One list rather than a list per status, so a view cannot land in two of them
// or none, and so a caller rendering the result cannot quietly stop reporting a
// status that is added later: the MCP tool returns this slice rather than a
// projection of it that someone has to remember to extend.
type MaterializeResult struct {
	Views []ViewOutcome `json:"views"`
}

// Names returns the views with one status, in view order.
func (r MaterializeResult) Names(status MaterializeStatus) []string {
	var out []string
	for _, o := range r.Views {
		if o.Status == status {
			out = append(out, o.View)
		}
	}
	return out
}

// Written, Fresh and Discarded name the views that ended with each status.
func (r MaterializeResult) Written() []string   { return r.Names(StatusWritten) }
func (r MaterializeResult) Fresh() []string     { return r.Names(StatusFresh) }
func (r MaterializeResult) Discarded() []string { return r.Names(StatusDiscarded) }

// Refused maps each refused view to its reason.
func (r MaterializeResult) Refused() map[string]string {
	out := map[string]string{}
	for _, o := range r.Views {
		if o.Status == StatusRefused {
			out[o.View] = o.Reason
		}
	}
	return out
}

// Refusals reports whether any view was refused, which is what makes the command
// exit non-zero.
func (r MaterializeResult) Refusals() bool { return len(r.Names(StatusRefused)) > 0 }

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
// Parquet describes a state that is already gone. The repoint waits for a get
// that is still running, since the copies are worth more than the wait.
func Materialize(ctx context.Context, d *dataset.Dataset, opts MaterializeOptions, warnOut io.Writer) (MaterializeResult, error) {
	if warnOut == nil {
		warnOut = io.Discard
	}
	var res MaterializeResult
	outcomes := map[string]ViewOutcome{}
	record := func(view string, status MaterializeStatus, reason string) {
		outcomes[view] = ViewOutcome{View: view, Status: status, Reason: reason}
	}

	releaseRun, err := d.LockMaterialize()
	if err != nil {
		return res, err
	}
	defer releaseRun()

	m, err := readManifestLocked(d)
	if err != nil {
		return res, err
	}
	declared, err := declaredViews(d.Dir, m)
	if err != nil {
		return res, err
	}
	views := make([]string, 0, len(declared))
	filesByView := map[string][]string{}
	for view, decl := range declared {
		views = append(views, view)
		filesByView[view] = decl.files
	}
	sort.Strings(views)

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
			removeCopyArtifacts(d, b.tmp)
		}
	}()

	for i, view := range views {
		if opts.Progress != nil {
			opts.Progress(view, i+1, len(views))
		}
		files := filesByView[view]
		if degraded[view] {
			record(view, StatusRefused, "registered from its typed-empty fallback, so every declared input is unreadable: "+
				strings.Join(files, ", "))
			continue
		}
		missing := missingInputs(d.Dir, files)
		if len(missing) > 0 && !opts.AllowPartial {
			record(view, StatusRefused, missingInputReason(d, m, files, missing))
			continue
		}
		caveat := viewCaveat(m, view, files, missing)
		if !opts.Force {
			// The recorded copy has to still be readable, not merely recorded. The
			// folder is documented as safe to delete at any time, and without this
			// a deleted or truncated Parquet reads as fresh forever while queries
			// quietly fall back to the raw artifacts.
			if rec, ok := m.Materialized[view]; ok && rec.Fresh(d.Dir, files, declared[view].signature) &&
				parquetReadable(ctx, e, d.Path(rec.File)) {
				record(view, StatusFresh, caveat)
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
			record(view, StatusRefused, err.Error())
			continue
		}
		if st, isStore := m.Stores[view]; isStore {
			n, err := parquetRowCount(ctx, e, d.Path(tmp))
			if err != nil {
				removeCopyArtifacts(d, tmp)
				if ctx.Err() != nil {
					return res, err
				}
				record(view, StatusRefused, err.Error())
				continue
			}
			if n != st.Count {
				removeCopyArtifacts(d, tmp)
				record(view, StatusRefused, fmt.Sprintf("copied %d rows but the manifest records %d in the %s store", n, st.Count, view))
				continue
			}
		}
		built[view] = builtView{tmp: tmp, inputs: inputs, signature: declared[view].signature, caveat: caveat}
	}

	testHookBeforeRepoint()
	if err := repointManifest(ctx, d, built, record, warnOut); err != nil {
		return res, err
	}
	// Assembled from the view list rather than appended as we go, so the order is
	// the view order and a view that somehow got no outcome is visible as absent
	// rather than as an arbitrary default.
	for _, view := range views {
		if o, ok := outcomes[view]; ok {
			res.Views = append(res.Views, o)
		}
	}
	return res, nil
}

// testHookBeforeRepoint is a seam for driving the window between the copies and
// the repoint, matching the hook the merge path uses.
var testHookBeforeRepoint = func() {}

// builtView is one finished copy waiting to be committed: its temp file, the
// input fingerprints taken just before it was made, and the caveat its outcome
// will carry.
type builtView struct {
	tmp       string
	inputs    map[string]string
	signature string
	caveat    string
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
//
// The locks are waited for rather than tried once. A fetch holds the activity
// lock shared for its whole run, and failing busy here would throw away every
// finished copy because someone started a get; only the caller's context ends
// the wait. A copy whose rename fails is refused on its own, like a copy that
// failed, so the views already renamed still reach the manifest.
func repointManifest(ctx context.Context, d *dataset.Dataset, built map[string]builtView,
	record func(view string, status MaterializeStatus, reason string), warnOut io.Writer) error {
	if len(built) == 0 {
		return nil
	}
	release, err := d.LockMutation()
	if errors.Is(err, dataset.ErrBusy) {
		fmt.Fprintln(warnOut, "waiting for another cc-data command to finish before committing the copies (interrupt to abandon them)")
		release, err = d.LockMutationWait(ctx)
	}
	if err != nil {
		return err
	}
	defer release()

	m, err := d.ReadManifest()
	if err != nil {
		return err
	}
	current, err := declaredViews(d.Dir, m)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for view, b := range built {
		decl, stillAView := current[view]
		if !stillAView || decl.signature != b.signature ||
			!sameFingerprints(b.inputs, dataset.FingerprintInputs(d.Dir, decl.files)) {
			record(view, StatusDiscarded, "its inputs or its view definition changed while the copy was running")
			continue
		}
		rec := dataset.Materialized{
			File:      filepath.ToSlash(filepath.Join(dataset.MaterializedDir, view+".parquet")),
			Inputs:    b.inputs,
			Signature: b.signature,
			BuiltAt:   now,
		}
		if err := fsutil.RenameAtomic(d.Path(b.tmp), d.Path(rec.File)); err != nil {
			record(view, StatusRefused, err.Error())
			continue
		}
		delete(built, view)
		m.Materialized[view] = rec
		record(view, StatusWritten, b.caveat)
	}
	return d.WriteManifest(m)
}

// viewDecl is what a materializable view currently declares: the files it reads
// and the signature of the statement that reads them.
type viewDecl struct {
	files     []string
	signature string
}

// declaredViews maps each materializable view to what it currently declares. The
// directory is canonicalized here so the signatures match the ones the engine
// computes when it decides whether to read a Parquet.
func declaredViews(dir string, m *dataset.Manifest) (map[string]viewDecl, error) {
	canon, err := canonicalize(dir)
	if err != nil {
		return nil, err
	}
	vs := viewSet{canonDir: canon, m: m}
	stmts := vs.statements()
	eligible := map[string]bool{}
	for _, view := range materializableFrom(stmts, vs.prefix) {
		eligible[view] = true
	}
	out := map[string]viewDecl{}
	for _, st := range stmts {
		if bare := bareViewName(st.name, vs.prefix); eligible[bare] {
			out[bare] = viewDecl{files: st.files, signature: viewSignature(st, canon, vs.prefix)}
		}
	}
	return out, nil
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
// cleanup could run: a kill, a crash, or a power cut. The materialize guard is
// held, so a temp file present now belongs to no living run.
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
		removeCopyArtifacts(d, tmp)
		return "", fmt.Errorf("materializing %s: %w", view, err)
	}
	return tmp, nil
}

// removeCopyArtifacts removes a temp copy and anything left beside it. A COPY
// that fails partway leaves its own scratch file in the target's directory,
// named after the target, so removing the target alone leaks it. Matching on the
// temp name rather than on a fixed prefix keeps this independent of how the
// engine spells its scratch file.
func removeCopyArtifacts(d *dataset.Dataset, tmp string) {
	os.Remove(d.Path(tmp))
	dir := d.Path(dataset.MaterializedDir)
	base := filepath.Base(tmp)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != base && strings.Contains(entry.Name(), base) {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// parquetReadable reports whether the engine can open the file and read its
// footer, which is what separates a recorded copy that is still usable from one
// that has been deleted or truncated.
func parquetReadable(ctx context.Context, e *Engine, path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	_, err := parquetRowCount(ctx, e, path)
	return err == nil
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

// viewCaveat is what a written or fresh view's outcome has to say about the
// copy: how many of its declared inputs it was built from when some are missing,
// and which of the runs it reads are marked incomplete. A fresh copy built with
// an input missing records that input as absent, so the same test applies to it.
func viewCaveat(m *dataset.Manifest, view string, files, missing []string) string {
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("built from %d of %d declared inputs (missing: %s)",
			len(files)-len(missing), len(files), strings.Join(missing, ", ")))
	}
	if runs := incompleteRuns(m, view, files); len(runs) > 0 {
		parts = append(parts, fmt.Sprintf("reads %s marked incomplete", runList(runs)))
	}
	return strings.Join(parts, "; ")
}

// incompleteRuns names the runs a view reads whose download is marked
// incomplete. It is a caveat rather than a refusal: only a store fetch can set
// the flag, and it merges once at the end, so an incomplete store download
// leaves the Parquet byte-identical to one built if the run had never been
// requested.
//
// The view-to-download mapping is three rules because no single rule covers it.
// Report-backed views map by file intersection. The store views map by download
// type, since a store is one merged file across every run and carries no per-run
// identity. run_membership maps by type too: its filenames do encode (type, run),
// but an incomplete run has no membership file at all, so a per-run mapping would
// never see the run the caveat is about.
func incompleteRuns(m *dataset.Manifest, view string, files []string) []int {
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
		if intersects(dl.Files, files) {
			add(dl.RunID)
		}
	}
	sort.Ints(runs)
	return runs
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

// staleMaterializedViews names the views whose recorded Parquet no longer matches
// the inputs. It lives here, not in dataset, because a view's input list is a
// property of the view set.
func staleMaterializedViews(d *dataset.Dataset, m *dataset.Manifest) ([]string, error) {
	if len(m.Materialized) == 0 {
		return nil, nil
	}
	current, err := declaredViews(d.Dir, m)
	if err != nil {
		return nil, err
	}
	var stale []string
	for view, rec := range m.Materialized {
		decl, ok := current[view]
		if !ok || !rec.Fresh(d.Dir, decl.files, decl.signature) {
			stale = append(stale, view)
		}
	}
	sort.Strings(stale)
	return stale, nil
}

// ShowJSON builds a dataset's summary from one manifest read, with the warnings
// only the view set can decide appended and the whole list re-sorted. Both
// surfaces call it, so the same documented ShowJSON contract cannot carry
// different warning sets on the CLI and over MCP, and the holdings and the
// warnings cannot describe two different versions of the manifest.
func ShowJSON(d *dataset.Dataset, full bool) (*dataset.ShowJSON, error) {
	m, err := d.ReadManifest()
	if err != nil {
		return nil, err
	}
	s := d.BuildShowJSON(m, full)
	stale, err := staleMaterializedViews(d, m)
	if err != nil {
		return nil, err
	}
	for _, view := range stale {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"STALE_MATERIALIZED: view %s was materialized from inputs that have since changed; run cc-data dataset materialize %s to refresh it",
			view, d.Ref.String()))
	}
	sort.Strings(s.Warnings)
	return s, nil
}
