package store

import (
	"bufio"
	"bytes"
	"io"
	"os"
)

// MergeCounts describes a download's merge outcome.
type MergeCounts struct {
	Fetched int `json:"fetched"`
	New     int `json:"new"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
}

// MergeResult carries the per-run counts and the derived store metadata.
type MergeResult struct {
	PerRun  map[int]MergeCounts // new/updated/fetched per run
	Removed int                 // old-store identities dropped (attributed by the caller)
	Total   int                 // records written to the new store
	Columns map[string]string   // derived DuckDB column map
}

// StreamMerge performs the two-way merge on identity key between the old store
// (sorted JSONL, may be empty) and the sorted source, writing the new sorted
// store to tmpPath and fsyncing it. The source beats the store on equal keys;
// old-store identities not in covered are dropped and counted as removed. Counts
// are attributed per owning run.
func StreamMerge(oldStorePath string, k Kind, tmpPath string, src MergeSource, covered map[string]bool) (MergeResult, error) {
	res := MergeResult{PerRun: map[int]MergeCounts{}}
	for _, run := range src.Runs() {
		res.PerRun[run] = MergeCounts{Fetched: src.RawCount(run)}
	}
	detector := NewColumnDetector(k)

	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return res, err
	}
	w := bufio.NewWriter(out)

	write := func(rec []byte) error {
		detector.Observe(rec)
		if _, err := w.Write(rec); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
		res.Total++
		return nil
	}
	count := func(run int, isNew bool) {
		c := res.PerRun[run]
		if isNew {
			c.New++
		} else {
			c.Updated++
		}
		res.PerRun[run] = c
	}

	oldReader, oldClose, err := openOldStore(oldStorePath)
	if err != nil {
		out.Close()
		return res, err
	}
	defer oldClose()

	fail := func(err error) (MergeResult, error) {
		out.Close()
		return res, err
	}

	oldKey, oldRec, oldOK, err := nextOldRecord(oldReader, k)
	if err != nil {
		return fail(err)
	}
	segKey, segRec, segRun, segOK, err := src.Next()
	if err != nil {
		return fail(err)
	}

	for oldOK || segOK {
		switch {
		case oldOK && (!segOK || oldKey < segKey):
			if covered[oldKey] {
				if err := write(oldRec); err != nil {
					return fail(err)
				}
			} else {
				res.Removed++
			}
			if oldKey, oldRec, oldOK, err = nextOldRecord(oldReader, k); err != nil {
				return fail(err)
			}
		default:
			replaces := oldOK && oldKey == segKey
			if err := write(segRec); err != nil {
				return fail(err)
			}
			count(segRun, !replaces)
			if replaces {
				if oldKey, oldRec, oldOK, err = nextOldRecord(oldReader, k); err != nil {
					return fail(err)
				}
			}
			if segKey, segRec, segRun, segOK, err = src.Next(); err != nil {
				return fail(err)
			}
		}
	}

	if err := w.Flush(); err != nil {
		out.Close()
		return res, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return res, err
	}
	if err := out.Close(); err != nil {
		return res, err
	}
	res.Columns = detector.Map()
	return res, nil
}

func openOldStore(path string) (*bufio.Reader, func(), error) {
	if path == "" {
		return bufio.NewReader(bytes.NewReader(nil)), func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return bufio.NewReader(bytes.NewReader(nil)), func() {}, nil
		}
		return nil, nil, err
	}
	return bufio.NewReader(f), func() { f.Close() }, nil
}

// nextOldRecord reads the next non-empty record from the old store and computes
// its key.
func nextOldRecord(r *bufio.Reader, k Kind) (key string, rec []byte, ok bool, err error) {
	for {
		line, readErr := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(bytes.TrimRight(line, "\n"))
		if len(trimmed) > 0 {
			id, ierr := IdentityFromRecord(k, trimmed)
			if ierr != nil {
				return "", nil, false, ierr
			}
			out := make([]byte, len(trimmed))
			copy(out, trimmed)
			return k.Key(id), out, true, nil
		}
		if readErr == io.EOF {
			return "", nil, false, nil
		}
		if readErr != nil {
			return "", nil, false, readErr
		}
	}
}
