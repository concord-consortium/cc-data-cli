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

	newestStore := map[string]int{}
	newestStoreFile := map[string]string{}
	newestMember := map[string]int{} // key "<type>/<run>" -> version
	newestMemberFile := map[string]string{}
	var csvs []string

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if mm := reindexStoreRe.FindStringSubmatch(name); mm != nil {
			typ, v := mm[1], atoi(mm[2])
			if v > newestStore[typ] {
				newestStore[typ] = v
				newestStoreFile[typ] = name
			}
			continue
		}
		if mm := reindexMemberRe.FindStringSubmatch(name); mm != nil {
			typ, run, v := mm[1], atoi(mm[2]), atoi(mm[3])
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

	// Adopt stores with re-derived counts and column maps.
	m.Stores = map[string]Store{}
	for typ, v := range newestStore {
		count, cols, serr := scanStoreCountAndColumns(d.Path(newestStoreFile[typ]))
		if serr != nil {
			return serr
		}
		m.Stores[typ] = Store{File: newestStoreFile[typ], Version: v, Count: count, Columns: cols}
	}

	// Adopt membership.
	m.Membership = map[string]MembershipRef{}
	for key, v := range newestMember {
		m.Membership[key] = MembershipRef{File: newestMemberFile[key], Version: v}
	}

	// Rebuild downloads: one per adopted membership (answers/history), one per CSV.
	m.Downloads = nil
	for key := range newestMember {
		typ, run, ok := parseMembershipKey(key)
		if !ok {
			continue
		}
		m.Downloads = append(m.Downloads, Download{
			Type:      typ,
			RunID:     run,
			Complete:  true,
			Recovered: true,
			FetchedAt: clock().UTC(),
		})
	}
	sort.Strings(csvs)
	for _, name := range csvs {
		dl, derr := d.reindexCSV(name)
		if derr != nil {
			return derr
		}
		m.Downloads = append(m.Downloads, dl)
	}

	if err := d.RebuildAttachmentIndex(m); err != nil {
		return err
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

func (d *Dataset) reindexCSV(name string) (Download, error) {
	mm := reindexCSVRe.FindStringSubmatch(name)
	run := atoi(mm[1])
	dlType := "report"
	var jobID *int
	if mm[2] != "" {
		j := atoi(mm[2])
		jobID = &j
		dlType = "report_job"
	}
	reportType, recovered := recoverReportType(d.Path(name))
	rowCount, cols, order, dialect, err := DetectCSV(d.Path(name), reportType)
	if err != nil {
		return Download{}, err
	}
	return Download{
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
	}, nil
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
			break
		}
	}
	return count, det.Map(), nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
