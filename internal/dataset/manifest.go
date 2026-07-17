package dataset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// MergeCounts is re-exported from the store engine for manifest consumers.
type MergeCounts = store.MergeCounts

// CurrentManifestVersion is the schema version this binary writes.
const CurrentManifestVersion = 1

// ManifestFile is the manifest's filename within a dataset directory.
const ManifestFile = "manifest.json"

// Manifest indexes a dataset's holdings; all file paths are relative to the
// dataset folder.
type Manifest struct {
	Version     int                      `json:"version"`
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	CreatedAt   time.Time                `json:"created_at"`
	Stores      map[string]Store         `json:"stores"`
	Membership  map[string]MembershipRef `json:"membership"`
	Downloads   []Download               `json:"downloads"`
	Attachments []AttachmentFile         `json:"attachments"`
}

type Store struct {
	File    string            `json:"file"`
	Version int               `json:"version"`
	Count   int               `json:"count"`
	Columns map[string]string `json:"columns"`
}

type MembershipRef struct {
	File    string `json:"file"`
	Version int    `json:"version"`
}

type Download struct {
	Type         string            `json:"type"`
	RunID        int               `json:"run_id"`
	JobID        *int              `json:"job_id,omitempty"`
	Slug         string            `json:"slug,omitempty"`
	ReportType   string            `json:"report_type,omitempty"`
	SourceKey    string            `json:"source_key,omitempty"`
	Filters      json.RawMessage   `json:"filters,omitempty"`
	FilterLabels []string          `json:"filter_labels,omitempty"`
	Files        []string          `json:"files,omitempty"`
	Coverage     *Coverage         `json:"coverage,omitempty"`
	MergeCounts  *MergeCounts      `json:"merge_counts,omitempty"`
	HistoryMode  string            `json:"history_mode,omitempty"`
	Complete     bool              `json:"complete"`
	FetchedAt    time.Time         `json:"fetched_at"`
	Scanned      []string          `json:"scanned,omitempty"`
	RowCount     *int              `json:"row_count,omitempty"`
	Columns      map[string]string `json:"columns,omitempty"`
	CSVDialect   *CSVDialect       `json:"csv_dialect,omitempty"`
	Recovered    bool              `json:"recovered,omitempty"`
}

type CSVDialect struct {
	Delim  string `json:"delim"`
	Quote  string `json:"quote"`
	Escape string `json:"escape"`
	Header bool   `json:"header"`
}

type Coverage struct {
	Queried  *int          `json:"queried"`
	WithData int           `json:"with_data"`
	Empty    *int          `json:"empty"`
	Missing  []MissingItem `json:"missing,omitempty"`
}

type MissingItem struct {
	DocID string `json:"doc_id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

type AttachmentFile struct {
	ID12        string          `json:"id12"`
	Name        string          `json:"name"`
	Source      string          `json:"source"`
	PublicPath  string          `json:"public_path"`
	ContentType string          `json:"content_type,omitempty"`
	Size        int64           `json:"size"`
	File        string          `json:"file"`
	State       bool            `json:"state,omitempty"`
	Refs        []AttachmentRef `json:"refs"`
}

type AttachmentRef struct {
	Type           string `json:"type"`
	SourceKey      string `json:"source_key"`
	RemoteEndpoint string `json:"remote_endpoint"`
	QuestionID     string `json:"question_id"`
	HistoryID      string `json:"history_id,omitempty"`
}

// ReadManifestFile reads and migrates a manifest from a dataset directory.
func ReadManifestFile(datasetDir string) (*Manifest, error) {
	data, err := fsutil.ReadReplaceTarget(filepath.Join(datasetDir, ManifestFile))
	if err != nil {
		return nil, err
	}
	return decodeManifest(data)
}

func decodeManifest(data []byte) (*Manifest, error) {
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("manifest.json is not valid JSON: %w", err)
	}
	if probe.Version > CurrentManifestVersion {
		return nil, fmt.Errorf("manifest version %d is newer than this cc-data understands; please upgrade cc-data", probe.Version)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	// Forward migration switch; v1 is a no-op today (the extension point).
	switch m.Version {
	case 0, 1:
		m.Version = CurrentManifestVersion
	}
	ensureMaps(&m)
	return &m, nil
}

func ensureMaps(m *Manifest) {
	if m.Stores == nil {
		m.Stores = map[string]Store{}
	}
	if m.Membership == nil {
		m.Membership = map[string]MembershipRef{}
	}
}

// writeManifestFile writes the manifest atomically; callers must hold the
// per-dataset guard (asserted by Dataset.WriteManifest).
func writeManifestFile(datasetDir string, m *Manifest) error {
	if m.Version == 0 {
		m.Version = CurrentManifestVersion
	}
	ensureMaps(m)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(datasetDir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic0600(filepath.Join(datasetDir, ManifestFile), data)
}

// MembershipKey is the manifest membership map key for a (type, run).
func MembershipKey(typ string, run int) string {
	return fmt.Sprintf("%s/%d", typ, run)
}

// SetMembershipRef repoints a (type, run)'s membership to a version.
func (m *Manifest) SetMembershipRef(typ string, run, version int) {
	if m.Membership == nil {
		m.Membership = map[string]MembershipRef{}
	}
	m.Membership[MembershipKey(typ, run)] = MembershipRef{
		File:    fmt.Sprintf("members_%s_%d.v%d.jsonl", typ, run, version),
		Version: version,
	}
}
