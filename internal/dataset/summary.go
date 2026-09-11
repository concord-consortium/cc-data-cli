package dataset

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// ShowJSON is the stable dataset show --json contract.
type ShowJSON struct {
	Ref         string         `json:"ref"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Portal      string         `json:"portal"`
	CreatedAt   time.Time      `json:"created_at"`
	Totals      map[string]int `json:"totals"`
	SizeBytes   int64          `json:"size_bytes"`
	// MaterializedBytes is the subset of SizeBytes under derived subfolders,
	// which a researcher can reclaim for free. SizeBytes keeps its meaning.
	MaterializedBytes int64          `json:"materialized_bytes"`
	Downloads         []DownloadJSON `json:"downloads"`
	Warnings          []string       `json:"warnings"`
}

// DownloadJSON is one download in ShowJSON.
type DownloadJSON struct {
	Type         string             `json:"type"`
	RunID        int                `json:"run_id"`
	JobID        *int               `json:"job_id,omitempty"`
	Slug         string             `json:"slug,omitempty"`
	ReportType   string             `json:"report_type,omitempty"`
	FilterLabels []string           `json:"filter_labels,omitempty"`
	FetchedAt    time.Time          `json:"fetched_at"`
	Complete     bool               `json:"complete"`
	MergeCounts  *store.MergeCounts `json:"merge_counts,omitempty"`
	Coverage     *Coverage          `json:"coverage,omitempty"`
	RowCount     *int               `json:"row_count,omitempty"`
	Recovered    bool               `json:"recovered,omitempty"`
	Files        []string           `json:"files,omitempty"`
}

// ListJSON is the dataset list --json contract.
type ListJSON struct {
	Datasets []ListRowJSON `json:"datasets"`
}

// ListRowJSON is one dataset row in ListJSON.
type ListRowJSON struct {
	Ref         string         `json:"ref"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Portal      string         `json:"portal"`
	AgeSeconds  int64          `json:"age_seconds"`
	Totals      map[string]int `json:"totals"`
	SizeBytes   int64          `json:"size_bytes"`

	MaterializedBytes int64 `json:"materialized_bytes"`
}

// BuildShowJSON computes the dataset summary from the manifest only (no data-file
// scan beyond size/drift stats, no DuckDB). full adds per-file detail.
func (d *Dataset) BuildShowJSON(full bool) (*ShowJSON, error) {
	m, err := d.ReadManifest()
	if err != nil {
		return nil, err
	}
	size, derived := dirSizes(d.Dir)
	out := &ShowJSON{
		Ref:               d.Ref.String(),
		Name:              m.Name,
		Description:       m.Description,
		Portal:            d.Ref.Portal.Host(),
		CreatedAt:         m.CreatedAt,
		Totals:            manifestTotals(m),
		SizeBytes:         size,
		MaterializedBytes: derived,
		Warnings:          driftWarnings(d, m),
	}
	for _, dl := range m.Downloads {
		dj := DownloadJSON{
			Type:         dl.Type,
			RunID:        dl.RunID,
			JobID:        dl.JobID,
			Slug:         dl.Slug,
			ReportType:   dl.ReportType,
			FilterLabels: dl.FilterLabels,
			FetchedAt:    dl.FetchedAt,
			Complete:     dl.Complete,
			MergeCounts:  dl.MergeCounts,
			Coverage:     dl.Coverage,
			RowCount:     dl.RowCount,
			Recovered:    dl.Recovered,
		}
		if full {
			dj.Files = dl.Files
		}
		out.Downloads = append(out.Downloads, dj)
	}
	if out.Downloads == nil {
		out.Downloads = []DownloadJSON{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	return out, nil
}

// BuildListJSON enumerates every dataset across all portal folders under a data
// root, from the manifest only.
func BuildListJSON(dataRoot string) (*ListJSON, error) {
	out := &ListJSON{Datasets: []ListRowJSON{}}
	portals, err := os.ReadDir(dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	now := clock().UTC()
	for _, p := range portals {
		if !p.IsDir() {
			continue
		}
		datasetsDir := filepath.Join(dataRoot, p.Name(), "datasets")
		ds, err := os.ReadDir(datasetsDir)
		if err != nil {
			continue
		}
		for _, entry := range ds {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(datasetsDir, entry.Name())
			m, err := ReadManifestFile(dir)
			if err != nil {
				continue
			}
			size, derived := dirSizes(dir)
			out.Datasets = append(out.Datasets, ListRowJSON{
				Ref:               decodePortalFolder(p.Name()) + "/" + m.Name,
				Name:              m.Name,
				Description:       m.Description,
				Portal:            decodePortalFolder(p.Name()),
				AgeSeconds:        int64(now.Sub(m.CreatedAt).Seconds()),
				Totals:            manifestTotals(m),
				SizeBytes:         size,
				MaterializedBytes: derived,
			})
		}
	}
	sort.Slice(out.Datasets, func(i, j int) bool { return out.Datasets[i].Ref < out.Datasets[j].Ref })
	return out, nil
}

// decodePortalFolder reverses the filesystem folder encoding (_ -> :) for the
// port separator; a bare host has no underscore-encoded port.
func decodePortalFolder(folder string) string {
	// Only a trailing _<digits> segment encodes a port.
	if i := lastUnderscoreBeforeDigits(folder); i >= 0 {
		return folder[:i] + ":" + folder[i+1:]
	}
	return folder
}

func lastUnderscoreBeforeDigits(s string) int {
	i := len(s) - 1
	for i >= 0 && s[i] >= '0' && s[i] <= '9' {
		i--
	}
	if i >= 0 && i < len(s)-1 && s[i] == '_' {
		return i
	}
	return -1
}

func manifestTotals(m *Manifest) map[string]int {
	totals := map[string]int{}
	if st, ok := m.Stores[store.TypeAnswers]; ok {
		totals["answers"] = st.Count
	}
	if st, ok := m.Stores[store.TypeHistory]; ok {
		totals["history"] = st.Count
	}
	reportRows := 0
	for _, dl := range m.Downloads {
		if (dl.Type == "report" || dl.Type == "report_job") && dl.RowCount != nil {
			reportRows += *dl.RowCount
		}
	}
	totals["report"] = reportRows
	totals["attachments"] = len(m.Attachments)
	return totals
}

var finalArtifactRe = regexp.MustCompile(`^(answers\.v\d+\.jsonl|history\.v\d+\.jsonl|members_.*\.v\d+\.jsonl|report_\d+.*\.csv)$`)

// driftWarnings computes cheap stat-level warnings with machine-stable codes.
func driftWarnings(d *Dataset, m *Manifest) []string {
	var warnings []string
	// Manifest-named files missing on disk.
	for typ, st := range m.Stores {
		if st.File != "" && !fileOnDisk(d.Path(st.File)) {
			warnings = append(warnings, "MISSING_FILE: store "+typ+" file "+st.File+" is named by the manifest but missing on disk")
		}
	}
	for key, ref := range m.Membership {
		if !fileOnDisk(d.Path(ref.File)) {
			warnings = append(warnings, "MISSING_FILE: membership "+key+" file "+ref.File+" is missing on disk")
		}
	}
	// Incomplete downloads.
	for _, dl := range m.Downloads {
		if !dl.Complete {
			warnings = append(warnings, "INCOMPLETE: a "+dl.Type+" download for run "+itoa(dl.RunID)+" did not finish")
		}
		// Only report/report_job downloads carry provenance; store downloads
		// (answers/history) have none, so the recovered flag there is not a
		// provenance gap and must not raise this warning.
		if dl.Recovered && (dl.Type == "report" || dl.Type == "report_job") {
			warnings = append(warnings, "RECOVERED_PROVENANCE: run "+itoa(dl.RunID)+" has no provenance; re-fetch to restore its report_type, its filter, and any dimension view it feeds")
		}
		if (dl.Type == "report" || dl.Type == "report_job") && dl.ReportType != "" && !IsAllowedReportType(dl.ReportType) {
			warnings = append(warnings, "UNKNOWN_TYPE: run "+itoa(dl.RunID)+" report_type "+dl.ReportType+" is unknown to this cc-data version and excluded from the reports view; upgrade suggested")
		}
	}
	// A Parquet in a derived subfolder that no manifest entry names. This is what
	// a reindex leaves, and what a materialize run leaves if it discards a view
	// after copying it. Deciding it needs no view knowledge, only the folder
	// listing against Materialized.
	for _, name := range unreferencedMaterialized(d, m) {
		warnings = append(warnings, "UNREFERENCED_MATERIALIZED: "+name+" is on disk but the manifest no longer records it; queries are reading the raw artifacts, run cc-data dataset materialize "+d.Ref.String()+" to restore it (external tools can still read the file)")
	}
	// Orphan final-named files not referenced by the manifest.
	referenced := manifestReferencedFiles(m)
	entries, _ := os.ReadDir(d.Dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if finalArtifactRe.MatchString(name) && !referenced[name] {
			warnings = append(warnings, "ORPHAN_FILE: "+name+" is on disk but not referenced by the manifest; run cc-data dataset reindex")
		}
	}
	sort.Strings(warnings)
	return warnings
}

func unreferencedMaterialized(d *Dataset, m *Manifest) []string {
	entries, err := os.ReadDir(filepath.Join(d.Dir, MaterializedDir))
	if err != nil {
		return nil
	}
	recorded := map[string]bool{}
	for _, mat := range m.Materialized {
		recorded[filepath.Base(mat.File)] = true
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".parquet") || recorded[name] {
			continue
		}
		out = append(out, MaterializedDir+"/"+name)
	}
	return out
}

func manifestReferencedFiles(m *Manifest) map[string]bool {
	ref := map[string]bool{}
	for _, st := range m.Stores {
		if st.File != "" {
			ref[st.File] = true
		}
	}
	for _, mr := range m.Membership {
		ref[mr.File] = true
	}
	for _, dl := range m.Downloads {
		for _, f := range dl.Files {
			ref[filepath.Base(f)] = true
		}
	}
	return ref
}

func fileOnDisk(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirSizes returns the dataset's total bytes and the subset under registered
// derived subfolders, in one walk so dataset list does not pay a second per
// dataset.
func dirSizes(dir string) (total, derived int64) {
	filepath.WalkDir(dir, func(path string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return nil
		}
		info, e := de.Info()
		if e != nil {
			return nil
		}
		total += info.Size()
		if rel, rerr := filepath.Rel(dir, path); rerr == nil {
			if top := strings.Split(filepath.ToSlash(rel), "/")[0]; IsDerivedSubdir(top) {
				derived += info.Size()
			}
		}
		return nil
	})
	return total, derived
}

func itoa(n int) string { return strconv.Itoa(n) }
