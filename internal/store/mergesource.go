package store

import (
	"bytes"
	"encoding/json"
	"sort"
)

// MergeSource is the sorted input the merge consumes: one segment for a plain
// merge, or several combined for a backlog sweep. It yields one winning record
// per identity key and reports which run owns each key.
type MergeSource interface {
	Keys() []string
	Record(key string) ([]byte, error)
	OwnerRun(key string) int
	RawCount(run int) int
	Runs() []int
}

// SingleSource adapts one segment index (for one run) to a MergeSource.
type SingleSource struct {
	idx *SegmentIndex
	run int
}

// NewSingleSource wraps a segment index for a run.
func NewSingleSource(idx *SegmentIndex, run int) *SingleSource {
	return &SingleSource{idx: idx, run: run}
}

func (s *SingleSource) Keys() []string                    { return s.idx.Keys() }
func (s *SingleSource) Record(key string) ([]byte, error) { return s.idx.Record(key) }
func (s *SingleSource) OwnerRun(string) int               { return s.run }
func (s *SingleSource) Runs() []int                       { return []int{s.run} }
func (s *SingleSource) RawCount(run int) int {
	if run == s.run {
		return s.idx.RawCount()
	}
	return 0
}

// MultiSource combines several per-run segment indexes into one sorted source,
// resolving a shared identity to the record with the newest (_fetched_at,
// _run_id) stamp.
type MultiSource struct {
	byRun map[int]*SegmentIndex
	keys  []string
	owner map[string]int
}

// NewMultiSource combines per-run segment indexes.
func NewMultiSource(byRun map[int]*SegmentIndex) (*MultiSource, error) {
	m := &MultiSource{byRun: byRun, owner: map[string]int{}}
	best := map[string]stamp{}
	runs := make([]int, 0, len(byRun))
	for run := range byRun {
		runs = append(runs, run)
	}
	sort.Ints(runs)
	for _, run := range runs {
		idx := byRun[run]
		for _, key := range idx.Keys() {
			st, err := readStamp(idx, key)
			if err != nil {
				return nil, err
			}
			if cur, ok := best[key]; !ok || st.newerThan(cur) {
				best[key] = st
				m.owner[key] = run
			}
		}
	}
	m.keys = make([]string, 0, len(m.owner))
	for k := range m.owner {
		m.keys = append(m.keys, k)
	}
	sort.Strings(m.keys)
	return m, nil
}

func (m *MultiSource) Keys() []string          { return m.keys }
func (m *MultiSource) OwnerRun(key string) int { return m.owner[key] }
func (m *MultiSource) Runs() []int {
	runs := make([]int, 0, len(m.byRun))
	for r := range m.byRun {
		runs = append(runs, r)
	}
	sort.Ints(runs)
	return runs
}

func (m *MultiSource) RawCount(run int) int {
	if idx, ok := m.byRun[run]; ok {
		return idx.RawCount()
	}
	return 0
}

func (m *MultiSource) Record(key string) ([]byte, error) {
	return m.byRun[m.owner[key]].Record(key)
}

type stamp struct {
	fetchedAt string
	runID     int
}

func (s stamp) newerThan(o stamp) bool {
	if s.fetchedAt != o.fetchedAt {
		return s.fetchedAt > o.fetchedAt
	}
	return s.runID > o.runID
}

func readStamp(idx *SegmentIndex, key string) (stamp, error) {
	rec, err := idx.Record(key)
	if err != nil {
		return stamp{}, err
	}
	var fields struct {
		FetchedAt string `json:"_fetched_at"`
		RunID     int    `json:"_run_id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(rec), &fields); err != nil {
		return stamp{}, err
	}
	return stamp{fetchedAt: fields.FetchedAt, runID: fields.RunID}, nil
}
