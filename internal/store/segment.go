package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// AppendPage stamps each record with _fetched_at and _run_id, appends them as
// JSONL, and fsyncs the file before the caller persists the cursor (the cursor
// must never durably name pages whose segment bytes a crash could lose).
func (s *Segment) AppendPage(records [][]byte, fetchedAt time.Time, runID int) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	stampAt, _ := json.Marshal(fetchedAt.UTC().Format(time.RFC3339))
	stampRun, _ := json.Marshal(runID)
	w := bufio.NewWriter(f)
	for _, rec := range records {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rec, &obj); err != nil {
			f.Close()
			return err
		}
		obj["_fetched_at"] = stampAt
		obj["_run_id"] = stampRun
		line, err := json.Marshal(obj)
		if err != nil {
			f.Close()
			return err
		}
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

// segEntry indexes one record's position and identity in the segment.
type segEntry struct {
	id     Identity
	offset int64
	length int
}

// SegmentIndex holds the sorted-by-key index of a segment; records are streamed
// from the file by offset, so only the tuples (not the records) live in memory.
type SegmentIndex struct {
	f        *os.File
	typ      string
	keys     []string
	entries  map[string]segEntry
	rawCount int
}

// BuildIndex scans the segment, deduping equal identities (last wins within one
// download) and sorting by identity key.
func (s *Segment) BuildIndex() (*SegmentIndex, error) {
	f, err := os.Open(s.Path())
	if err != nil {
		return nil, err
	}
	idx := &SegmentIndex{f: f, typ: s.Type, entries: map[string]segEntry{}}
	r := bufio.NewReader(f)
	var offset int64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\n")
			if len(bytes.TrimSpace(trimmed)) > 0 {
				id, ierr := IdentityFromRecord(s.Type, trimmed)
				if ierr != nil {
					f.Close()
					return nil, ierr
				}
				key := id.Key(s.Type)
				idx.entries[key] = segEntry{id: id, offset: offset, length: len(trimmed)}
				idx.rawCount++
			}
		}
		offset += int64(len(line))
		if err != nil {
			break
		}
	}
	idx.keys = make([]string, 0, len(idx.entries))
	for k := range idx.entries {
		idx.keys = append(idx.keys, k)
	}
	sort.Strings(idx.keys)
	return idx, nil
}

// Keys returns the sorted identity keys.
func (idx *SegmentIndex) Keys() []string { return idx.keys }

// RawCount returns the number of records scanned (including resume duplicates).
func (idx *SegmentIndex) RawCount() int { return idx.rawCount }

// Record reads the record bytes for a key by seeking to its offset.
func (idx *SegmentIndex) Record(key string) ([]byte, error) {
	e := idx.entries[key]
	buf := make([]byte, e.length)
	if _, err := idx.f.ReadAt(buf, e.offset); err != nil {
		return nil, err
	}
	return buf, nil
}

// Identity returns the identity for a key.
func (idx *SegmentIndex) Identity(key string) Identity { return idx.entries[key].id }

// Identities returns the segment's identity set.
func (idx *SegmentIndex) Identities() []Identity {
	out := make([]Identity, 0, len(idx.keys))
	for _, k := range idx.keys {
		out = append(out, idx.entries[k].id)
	}
	return out
}

// Close releases the segment file handle.
func (idx *SegmentIndex) Close() error { return idx.f.Close() }
