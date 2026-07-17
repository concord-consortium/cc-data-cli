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
func StreamMerge(oldStorePath, typ, tmpPath string, src MergeSource, covered map[string]bool) (MergeResult, error) {
	res := MergeResult{PerRun: map[int]MergeCounts{}}
	for _, run := range src.Runs() {
		res.PerRun[run] = MergeCounts{Fetched: src.RawCount(run)}
	}
	detector := NewColumnDetector()

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
	countNew := func(key string) {
		run := src.OwnerRun(key)
		c := res.PerRun[run]
		c.New++
		res.PerRun[run] = c
	}
	countUpdated := func(key string) {
		run := src.OwnerRun(key)
		c := res.PerRun[run]
		c.Updated++
		res.PerRun[run] = c
	}

	segKeys := src.Keys()
	si := 0

	oldReader, oldClose, err := openOldStore(oldStorePath)
	if err != nil {
		out.Close()
		return res, err
	}
	defer oldClose()

	oldKey, oldRec, oldOK, err := nextOldRecord(oldReader, typ)
	if err != nil {
		out.Close()
		return res, err
	}

	fail := func(err error) (MergeResult, error) {
		out.Close()
		return res, err
	}

	for oldOK && si < len(segKeys) {
		segKey := segKeys[si]
		switch {
		case oldKey < segKey:
			if covered[oldKey] {
				if err := write(oldRec); err != nil {
					return fail(err)
				}
			} else {
				res.Removed++
			}
			oldKey, oldRec, oldOK, err = nextOldRecord(oldReader, typ)
			if err != nil {
				return fail(err)
			}
		case oldKey == segKey:
			rec, rerr := src.Record(segKey)
			if rerr != nil {
				return fail(rerr)
			}
			if err := write(rec); err != nil {
				return fail(err)
			}
			countUpdated(segKey)
			si++
			oldKey, oldRec, oldOK, err = nextOldRecord(oldReader, typ)
			if err != nil {
				return fail(err)
			}
		default: // oldKey > segKey: source identity is new
			rec, rerr := src.Record(segKey)
			if rerr != nil {
				return fail(rerr)
			}
			if err := write(rec); err != nil {
				return fail(err)
			}
			countNew(segKey)
			si++
		}
	}
	for oldOK {
		if covered[oldKey] {
			if err := write(oldRec); err != nil {
				return fail(err)
			}
		} else {
			res.Removed++
		}
		oldKey, oldRec, oldOK, err = nextOldRecord(oldReader, typ)
		if err != nil {
			return fail(err)
		}
	}
	for ; si < len(segKeys); si++ {
		key := segKeys[si]
		rec, rerr := src.Record(key)
		if rerr != nil {
			return fail(rerr)
		}
		if err := write(rec); err != nil {
			return fail(err)
		}
		countNew(key)
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
func nextOldRecord(r *bufio.Reader, typ string) (key string, rec []byte, ok bool, err error) {
	for {
		line, readErr := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(bytes.TrimRight(line, "\n"))
		if len(trimmed) > 0 {
			id, ierr := IdentityFromRecord(typ, trimmed)
			if ierr != nil {
				return "", nil, false, ierr
			}
			out := make([]byte, len(trimmed))
			copy(out, trimmed)
			return id.Key(typ), out, true, nil
		}
		if readErr == io.EOF {
			return "", nil, false, nil
		}
		if readErr != nil {
			return "", nil, false, readErr
		}
	}
}
