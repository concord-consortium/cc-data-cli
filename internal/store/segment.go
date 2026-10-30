package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

// SegmentsDir is the subdirectory holding a dataset's fetch segments.
const SegmentsDir = "segments"

// Cursor is the resumable state persisted after each page under the per-download
// lock. MergedAs is the landed store version (0 until the merge repoints).
type Cursor struct {
	NextPageToken  *string `json:"next_page_token"`
	Pages          int     `json:"pages"`
	Items          int     `json:"items"`
	TotalEndpoints *int    `json:"total_endpoints"`
	MergedAs       int     `json:"merged_as"`
}

// Finished reports whether all pages have been fetched.
func (c *Cursor) Finished() bool {
	return c.NextPageToken == nil || *c.NextPageToken == ""
}

// Segment is one download's on-disk segment plus its cursor and lock paths.
type Segment struct {
	dir   string // segments directory
	Type  string
	RunID int
}

// OpenSegment returns the segment handle for a (type, run) under a dataset dir.
func OpenSegment(datasetDir, typ string, runID int) *Segment {
	return &Segment{dir: filepath.Join(datasetDir, SegmentsDir), Type: typ, RunID: runID}
}

func (s *Segment) base() string       { return fmt.Sprintf("seg_%s_%d", s.Type, s.RunID) }
func (s *Segment) Path() string       { return filepath.Join(s.dir, s.base()+".jsonl") }
func (s *Segment) CursorPath() string { return filepath.Join(s.dir, s.base()+".cursor.json") }
func (s *Segment) LockName() string   { return s.base() + ".lock" }

// Exists reports whether the segment file is present.
func (s *Segment) Exists() bool {
	_, err := os.Stat(s.Path())
	return err == nil
}

// NotObjectError reports a record that is not a JSON object, by its index in the page.
type NotObjectError struct{ Index int }

func (e *NotObjectError) Error() string { return "record is not a JSON object" }

// StampLines returns each record as the line a segment stores: re-encoded from a map
// with _fetched_at and _run_id set, so a key the record repeats keeps its last value.
func StampLines(records [][]byte, fetchedAt time.Time, runID int) ([][]byte, error) {
	return stampLines(records, fetchedAt, "_run_id", runID)
}

// StampLines stamps records as the package-level StampLines does, with key in the
// kind's StampColumn.
func (k Kind) StampLines(records [][]byte, fetchedAt time.Time, key int) ([][]byte, error) {
	return stampLines(records, fetchedAt, k.StampColumn(), key)
}

func stampLines(records [][]byte, fetchedAt time.Time, col string, key int) ([][]byte, error) {
	stampAt, _ := json.Marshal(fetchedAt.UTC().Format(time.RFC3339))
	stampRun, _ := json.Marshal(key)
	lines := make([][]byte, 0, len(records))
	for i, rec := range records {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rec, &obj); err != nil || obj == nil {
			return nil, &NotObjectError{Index: i}
		}
		obj["_fetched_at"] = stampAt
		obj[col] = stampRun
		line, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// AppendLines appends stored lines and fsyncs the file before the caller persists the
// cursor.
func (s *Segment) AppendLines(lines [][]byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, line := range lines {
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteCursor persists the cursor atomically (the caller holds the per-download
// lock).
func (s *Segment) WriteCursor(c *Cursor) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic0600(s.CursorPath(), data)
}

// ReadCursor reads the cursor; the bool is false when no cursor exists.
func (s *Segment) ReadCursor() (*Cursor, bool, error) {
	data, err := fsutil.ReadReplaceTarget(s.CursorPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var c Cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false, err
	}
	return &c, true, nil
}

// Remove deletes the segment and cursor files (never the lock file).
func (s *Segment) Remove() error {
	if err := os.Remove(s.Path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(s.CursorPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// MarkMerged records the landed store version in the cursor (durable write #4,
// only after the manifest repoint).
func (s *Segment) MarkMerged(version int) error {
	c, ok, err := s.ReadCursor()
	if err != nil {
		return err
	}
	if !ok {
		c = &Cursor{}
	}
	c.MergedAs = version
	return s.WriteCursor(c)
}

type indexEntry struct {
	key    string
	offset int64
	length int32
}

// SegmentIndex is the segment's identities in key order, one entry per key (the last
// occurrence in the file wins). Records stay on disk and are read by offset, so only the
// keys and positions live in memory.
type SegmentIndex struct {
	f        *os.File
	kind     Kind
	entries  []indexEntry
	rawCount int
}

// BuildIndex scans the segment, deduping equal identities (last wins within one
// download) and sorting by identity key.
func (s *Segment) BuildIndex() (*SegmentIndex, error) {
	f, err := os.Open(s.Path())
	if err != nil {
		return nil, err
	}
	kind, ok := KindOf(s.Type)
	if !ok {
		f.Close()
		return nil, fmt.Errorf("unknown store type %q", s.Type)
	}
	idx := &SegmentIndex{f: f, kind: kind}
	r := bufio.NewReader(f)
	var offset int64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\n")
			if len(bytes.TrimSpace(trimmed)) > 0 {
				// The identity scan stops early and would accept a torn line, so validate it whole.
				if !json.Valid(trimmed) {
					f.Close()
					return nil, fmt.Errorf("segment %s: line at offset %d is not valid JSON", s.base(), offset)
				}
				id, ierr := IdentityFromRecord(kind, trimmed)
				if ierr != nil {
					f.Close()
					return nil, ierr
				}
				if len(trimmed) > math.MaxInt32 {
					f.Close()
					return nil, fmt.Errorf("segment %s: line at offset %d exceeds %d bytes", s.base(), offset, math.MaxInt32)
				}
				idx.entries = append(idx.entries, indexEntry{key: kind.Key(id), offset: offset, length: int32(len(trimmed))})
				idx.rawCount++
			}
		}
		offset += int64(len(line))
		if err != nil {
			if err == io.EOF {
				break
			}
			// A real read error must not be mistaken for end-of-file: a partial index
			// would give the merge and this run's membership a truncated identity set,
			// silently dropping old-store records the run still holds.
			f.Close()
			return nil, err
		}
	}
	// Stable, so the last entry of each run of equal keys is the latest in the file.
	sort.SliceStable(idx.entries, func(i, j int) bool { return idx.entries[i].key < idx.entries[j].key })
	kept := idx.entries[:0]
	for i, e := range idx.entries {
		if i+1 < len(idx.entries) && idx.entries[i+1].key == e.key {
			continue
		}
		kept = append(kept, e)
	}
	clear(idx.entries[len(kept):])
	if len(kept) < cap(kept)/2 {
		kept = slices.Clone(kept)
	}
	idx.entries = kept
	return idx, nil
}

// Len returns the number of distinct identities.
func (idx *SegmentIndex) Len() int { return len(idx.entries) }

// RawCount returns the number of records scanned (including resume duplicates).
func (idx *SegmentIndex) RawCount() int { return idx.rawCount }

func (idx *SegmentIndex) record(i int) ([]byte, error) {
	e := idx.entries[i]
	buf := make([]byte, e.length)
	if _, err := idx.f.ReadAt(buf, e.offset); err != nil {
		return nil, err
	}
	return buf, nil
}

// Close releases the segment file handle.
func (idx *SegmentIndex) Close() error { return idx.f.Close() }
