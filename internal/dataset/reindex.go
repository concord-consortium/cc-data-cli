package dataset

import (
	"bufio"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

var reindexStoreRe = regexp.MustCompile(`^(answers|history)\.v(\d+)\.jsonl$`)
var reindexMemberRe = regexp.MustCompile(`^members_(answers|history)_(\d+)\.v(\d+)\.jsonl$`)
var reindexCSVRe = regexp.MustCompile(`^report_(\d+)(?:_job_(\d+))?\.csv$`)

// Reindex rebuilds the manifest from the filesystem under the mutation locks:
// adopt the newest final store and membership versions, rebuild download entries
// from membership files and CSVs (recovering report_type from CSV shape only
// partially), rebuild the attachment index, and GC unreferenced files.
func (d *Dataset) Reindex() error {
	release, err := d.lockMutation()
	if err != nil {
		return err
	}
	defer release()

	m := d.reindexIdentity()

	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		return err
	}

	storeVersFiles := map[string]map[int]string{}  // type -> store version -> filename
	memberVersPresent := map[string]map[int]bool{} // type -> membership versions present
	newestMember := map[string]int{}               // key "<type>/<run>" -> version
	newestMemberFile := map[string]string{}
	var csvs []string

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if mm := reindexStoreRe.FindStringSubmatch(name); mm != nil {
			typ, v := mm[1], atoi(mm[2])
			if storeVersFiles[typ] == nil {
				storeVersFiles[typ] = map[int]string{}
			}
			storeVersFiles[typ][v] = name
			continue
		}
		if mm := reindexMemberRe.FindStringSubmatch(name); mm != nil {
			typ, run, v := mm[1], atoi(mm[2]), atoi(mm[3])
			if memberVersPresent[typ] == nil {
				memberVersPresent[typ] = map[int]bool{}
			}
			memberVersPresent[typ][v] = true
			key := MembershipKey(typ, run)
			if v > newestMember[key] {
				newestMember[key] = v
				newestMemberFile[key] = name
			}
			continue
		}
		if reindexCSVRe.MatchString(name) {
			csvs = append(csvs, name)
		}
	}

	// Adopt stores with re-derived counts and column maps. A merge writes the
	// store rename before the membership files, so a crash in that window can
	// leave an uncommitted store vN on disk while membership is still vN-1;
	// adopting vN then would drop vN's identities from covered on the next merge.
	// Only adopt a store version proven committed by a matching membership file
	// at the same version; otherwise fall back to the newest such generation. If
	// none committed (e.g. a crashed first merge), skip the store so a resume
	// rebuilds it from the surviving segment.
	m.Stores = map[string]Store{}
	for typ, versFiles := range storeVersFiles {
		v := newestCommittedStoreVersion(versFiles, memberVersPresent[typ])
		if v == 0 {
			continue
		}
		count, cols, serr := scanStoreCountAndColumns(d.Path(versFiles[v]))
		if serr != nil {
			return serr
		}
		m.Stores[typ] = Store{File: versFiles[v], Version: v, Count: count, Columns: cols}
	}

	// Adopt membership.
	m.Membership = map[string]MembershipRef{}
	for key, v := range newestMember {
		m.Membership[key] = MembershipRef{File: newestMemberFile[key], Version: v}
	}

	// Rebuild downloads: one per adopted membership (answers/history), one per CSV.
	// Label provenance (report_type, slug, filters, source_key, history_mode) is
	// not on disk, so a reindex that still has the old manifest carries it forward
	// rather than discarding it and flagging the entry recovered. Disk-derived
	// fields are always rebuilt from the filesystem below.
	prior := d.priorDownloadIndex()
	m.Downloads = nil
	memberKeys := make([]string, 0, len(newestMember))
	for key := range newestMember {
		memberKeys = append(memberKeys, key)
	}
	sort.Strings(memberKeys) // deterministic Downloads ordering
	for _, key := range memberKeys {
		typ, run, ok := parseMembershipKey(key)
		if !ok {
			continue
		}
		dl := Download{
			Type:      typ,
			RunID:     run,
			Complete:  true,
			Recovered: true,
			FetchedAt: clock().UTC(),
		}
		carryProvenance(&dl, prior[downloadKey(typ, run, nil)])
		m.Downloads = append(m.Downloads, dl)
	}
	sort.Strings(csvs)
	for _, name := range csvs {
		dl, derr := d.reindexCSV(name, prior)
		if derr != nil {
			return derr
		}
		m.Downloads = append(m.Downloads, dl)
	}

	// Carry forward attachments downloads: reindex has no path that rebuilds them
	// from the filesystem (only get_attachments writes them), so without this the
	// attachments entry and its scanned/coverage provenance fall off the manifest.
	// Sort by run for deterministic Downloads ordering; Files are reconciled against
	// the rebuilt on-disk index below.
	var attach []Download
	for k, dl := range prior {
		if k.typ == "attachments" {
			attach = append(attach, dl)
		}
	}
	sort.Slice(attach, func(i, j int) bool { return attach[i].RunID < attach[j].RunID })
	m.Downloads = append(m.Downloads, attach...)

	if err := d.RebuildAttachmentIndex(m); err != nil {
		return err
	}
	// Reconcile carried attachments Files against the rebuilt on-disk index: a file
	// GC'd or removed since the last fetch must not linger in the entry. Coverage
	// (the historical fetch record) is preserved as-is.
	onDisk := make(map[string]bool, len(m.Attachments))
	for _, af := range m.Attachments {
		onDisk[af.File] = true
	}
	for i := range m.Downloads {
		if m.Downloads[i].Type != "attachments" {
			continue
		}
		kept := make([]string, 0, len(m.Downloads[i].Files))
		for _, f := range m.Downloads[i].Files {
			if onDisk[f] {
				kept = append(kept, f)
			}
		}
		m.Downloads[i].Files = kept
	}
	return d.WriteManifest(m)
}

// reindexIdentity keeps the existing manifest identity or synthesizes one.
func (d *Dataset) reindexIdentity() *Manifest {
	if m, err := d.ReadManifest(); err == nil {
		return &Manifest{
			Version:     CurrentManifestVersion,
			Name:        m.Name,
			Description: m.Description,
			CreatedAt:   m.CreatedAt,
			Stores:      map[string]Store{},
			Membership:  map[string]MembershipRef{},
		}
	}
	return &Manifest{
		Version:    CurrentManifestVersion,
		Name:       filepath.Base(d.Dir),
		CreatedAt:  clock().UTC(),
		Stores:     map[string]Store{},
		Membership: map[string]MembershipRef{},
	}
}

// dlKey identifies a download for provenance carry-forward (job -1 means none).
type dlKey struct {
	typ string
	run int
	job int
}

func downloadKey(typ string, run int, jobID *int) dlKey {
	job := -1
	if jobID != nil {
		job = *jobID
	}
	return dlKey{typ: typ, run: run, job: job}
}

// priorDownloadIndex reads the existing manifest (if any) and indexes its
// downloads by identity so reindex can carry provenance forward. An absent or
// unreadable manifest yields an empty index (the disaster-recovery path, where
// there is nothing to carry and entries stay recovered).
func (d *Dataset) priorDownloadIndex() map[dlKey]Download {
	idx := map[dlKey]Download{}
	m, err := d.ReadManifest()
	if err != nil {
		return idx
	}
	for _, dl := range m.Downloads {
		idx[downloadKey(dl.Type, dl.RunID, dl.JobID)] = dl
	}
	return idx
}

// carryProvenance overlays label/provenance fields from a prior manifest download
// onto a reindex-rebuilt one. Disk-derived fields (files, columns, row_count,
// dialect) are left as reindex computed them. An authoritative prior entry (a real
// fetch, not a previous recovery) also restores the exact report_type and clears
// the recovered flag.
func carryProvenance(dl *Download, prior Download) {
	if prior.Type == "" {
		return // no matching prior entry
	}
	if prior.Slug != "" {
		dl.Slug = prior.Slug
	}
	if prior.SourceKey != "" {
		dl.SourceKey = prior.SourceKey
	}
	if prior.Filters != nil {
		dl.Filters = prior.Filters
	}
	if len(prior.FilterLabels) > 0 {
		dl.FilterLabels = prior.FilterLabels
	}
	if prior.HistoryMode != "" {
		dl.HistoryMode = prior.HistoryMode
	}
	if prior.Coverage != nil {
		dl.Coverage = prior.Coverage
	}
	if prior.MergeCounts != nil {
		dl.MergeCounts = prior.MergeCounts
	}
	if !prior.Recovered {
		if prior.ReportType != "" {
			dl.ReportType = prior.ReportType
		}
		dl.Recovered = false
	}
}

func (d *Dataset) reindexCSV(name string, prior map[dlKey]Download) (Download, error) {
	mm := reindexCSVRe.FindStringSubmatch(name)
	run := atoi(mm[1])
	dlType := "report"
	var jobID *int
	if mm[2] != "" {
		j := atoi(mm[2])
		jobID = &j
		dlType = "report_job"
	}
	// An authoritative prior report_type (a real fetch, not a previous recovery)
	// both restores the exact type and drives CSV column detection correctly; fall
	// back to shape-based recovery only when the manifest cannot vouch for it.
	p := prior[downloadKey(dlType, run, jobID)]
	reportType, recovered := p.ReportType, false
	if p.Recovered || p.ReportType == "" {
		reportType, recovered = recoverReportType(d.Path(name))
	}
	rowCount, cols, order, dialect, err := DetectCSV(d.Path(name), reportType)
	if err != nil {
		return Download{}, err
	}
	dl := Download{
		Type:        dlType,
		RunID:       run,
		JobID:       jobID,
		ReportType:  reportType,
		Files:       []string{name},
		RowCount:    &rowCount,
		Columns:     cols,
		ColumnOrder: order,
		CSVDialect:  &dialect,
		Complete:    true,
		Recovered:   recovered,
		FetchedAt:   clock().UTC(),
	}
	carryProvenance(&dl, p)
	return dl, nil
}

// recoverReportType recovers the report type from CSV shape only partially:
// no student_id column -> log; student_id plus pseudo-header rows -> answers;
// the ambiguous remainder -> the distinguished recovered value.
func recoverReportType(path string) (reportType string, recovered bool) {
	f, err := os.Open(path)
	if err != nil {
		return ReportTypeRecovered, true
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return ReportTypeRecovered, true
	}
	studentIDCol := indexOf(header, "student_id")
	if studentIDCol < 0 {
		return ReportTypeLog, false
	}
	for {
		row, rerr := r.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			break
		}
		if studentIDCol < len(row) && (row[studentIDCol] == "Prompt" || row[studentIDCol] == "Correct answer") {
			return ReportTypeAnswers, false
		}
	}
	return ReportTypeRecovered, true
}

func scanStoreCountAndColumns(path string) (int, map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	det := store.NewColumnDetector()
	count := 0
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		trimmed := trimLine(line)
		if len(strings.TrimSpace(string(trimmed))) > 0 {
			det.Observe(trimmed)
			count++
		}
		if rerr != nil {
			// io.EOF ends the final (unterminated) line; a real read error must
			// not commit a truncated count and partial column map as success.
			if rerr == io.EOF {
				break
			}
			return 0, nil, rerr
		}
	}
	return count, det.Map(), nil
}

// newestCommittedStoreVersion returns the highest store version whose merge
// generation committed, proven by a membership file present at the same version.
// Returns 0 when no store version has a matching membership file.
func newestCommittedStoreVersion(versFiles map[int]string, memberVers map[int]bool) int {
	best := 0
	for v := range versFiles {
		if memberVers[v] && v > best {
			best = v
		}
	}
	return best
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
