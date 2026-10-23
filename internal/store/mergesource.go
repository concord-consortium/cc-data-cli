package store

import (
	"bytes"
	"encoding/json"
	"sort"
)

// MergeSource yields one winning record per identity key, in key order, with the run
// that owns it: one segment for a plain merge, or several combined for a backlog sweep.
type MergeSource interface {
	Next() (key string, rec []byte, run int, ok bool, err error)
	RawCount(run int) int
	Runs() []int
}

// SingleSource adapts one segment index (for one run) to a MergeSource.
type SingleSource struct {
	idx *SegmentIndex
	run int
	pos int
}

// NewSingleSource wraps a segment index for a run.
func NewSingleSource(idx *SegmentIndex, run int) *SingleSource {
	return &SingleSource{idx: idx, run: run}
}

func (s *SingleSource) Next() (string, []byte, int, bool, error) {
	if s.pos >= len(s.idx.entries) {
		return "", nil, 0, false, nil
	}
	i := s.pos
	s.pos++
	rec, err := s.idx.record(i)
	if err != nil {
		return "", nil, 0, false, err
	}
	return s.idx.entries[i].key, rec, s.run, true, nil
}

func (s *SingleSource) Runs() []int { return []int{s.run} }

func (s *SingleSource) RawCount(run int) int {
	if run == s.run {
		return s.idx.RawCount()
	}
	return 0
}

// MultiSource merges several per-run segment indexes in key order. A key several runs
// share resolves to the record with the newest (_fetched_at, _run_id) stamp; stamps are
// read only for such keys.
type MultiSource struct {
	byRun map[int]*SegmentIndex
	runs  []int
	pos   map[int]int
}

// NewMultiSource combines per-run segment indexes.
func NewMultiSource(byRun map[int]*SegmentIndex) *MultiSource {
	runs := make([]int, 0, len(byRun))
	for run := range byRun {
		runs = append(runs, run)
	}
	sort.Ints(runs)
	return &MultiSource{byRun: byRun, runs: runs, pos: make(map[int]int, len(runs))}
}

func (m *MultiSource) Next() (string, []byte, int, bool, error) {
	var key string
	var holders []int
	for _, run := range m.runs {
		idx, p := m.byRun[run], m.pos[run]
		if p >= len(idx.entries) {
			continue
		}
		k := idx.entries[p].key
		switch {
		case holders == nil || k < key:
			key, holders = k, []int{run}
		case k == key:
			holders = append(holders, run)
		}
	}
	if holders == nil {
		return "", nil, 0, false, nil
	}
	for _, run := range holders {
		m.pos[run]++
	}
	if len(holders) == 1 {
		run := holders[0]
		rec, err := m.byRun[run].record(m.pos[run] - 1)
		return key, rec, run, err == nil, err
	}
	var bestRec []byte
	var best stamp
	bestRun := 0
	for i, run := range holders {
		rec, err := m.byRun[run].record(m.pos[run] - 1)
		if err != nil {
			return "", nil, 0, false, err
		}
		st, err := readStamp(rec)
		if err != nil {
			return "", nil, 0, false, err
		}
		if i == 0 || st.newerThan(best) {
			best, bestRec, bestRun = st, rec, run
		}
	}
	return key, bestRec, bestRun, true, nil
}

func (m *MultiSource) Runs() []int {
	out := make([]int, len(m.runs))
	copy(out, m.runs)
	return out
}

func (m *MultiSource) RawCount(run int) int {
	if idx, ok := m.byRun[run]; ok {
		return idx.RawCount()
	}
	return 0
}

type stamp struct {
	fetchedAt string
	key       int
}

func (s stamp) newerThan(o stamp) bool {
	if s.fetchedAt != o.fetchedAt {
		return s.fetchedAt > o.fetchedAt
	}
	return s.key > o.key
}

func readStamp(rec []byte) (stamp, error) {
	var fields struct {
		FetchedAt string `json:"_fetched_at"`
		RunID     int    `json:"_run_id"`
		ClassID   int    `json:"_class_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(rec), &fields); err != nil {
		return stamp{}, err
	}
	key := fields.RunID
	if key == 0 {
		key = fields.ClassID
	}
	return stamp{fetchedAt: fields.FetchedAt, key: key}, nil
}
