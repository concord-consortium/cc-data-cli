package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// PagedOptions parameterizes a fetch of one store type's paged route (one code path
// for every registered store type).
type PagedOptions struct {
	DS       *dataset.Dataset
	Client   *api.Client
	RunID    int
	Type     string // a registered store type
	Refresh  bool
	Progress io.Writer
}

// FetchPaged consumes a store type's paged bulk envelope: segment append per page,
// cursor persistence, resume, EXPIRED_CURSOR restart, the type's decoding and
// identity check, membership, merge, and coverage.
func FetchPaged(ctx context.Context, opts PagedOptions) (any, error) {
	release, err := acquireDownload(opts.DS, fmt.Sprintf("seg_%s_%d.lock", opts.Type, opts.RunID),
		fmt.Sprintf("download busy: another cc-data command is fetching run %d %s", opts.RunID, opts.Type))
	if err != nil {
		return nil, err
	}
	defer release()

	seg := store.OpenSegment(opts.DS.Dir, opts.Type, opts.RunID)

	if opts.Refresh {
		if err := seg.Remove(); err != nil {
			return nil, output.Internalf("clearing segment for refresh: %v", err)
		}
	}
	// Consult resume state before startEntry mutates the manifest entry: an
	// already-merged short-circuit must not clobber the completed entry's
	// MergeCounts/Coverage (which would emit false drift warnings).
	token, alreadyMerged, err := opts.resumeState(seg)
	if err != nil {
		return nil, err
	}
	if alreadyMerged {
		return opts.alreadyMergedResult(), nil
	}

	if err := opts.startEntry(); err != nil {
		return nil, err
	}

	if token != resumeFinished {
		if cliErr := opts.fetchPages(ctx, seg, token); cliErr != nil {
			return nil, cliErr
		}
	}

	// Capture total_endpoints before the merge removes the cursor.
	var totalEndpoints *int
	if cur, ok, _ := seg.ReadCursor(); ok {
		totalEndpoints = cur.TotalEndpoints
	}

	counts, err := opts.DS.MergeCompact(opts.Type, opts.RunID, seg)
	if err != nil {
		return nil, output.Internalf("merge: %v", err)
	}
	coverage, note := opts.computeCoverage(totalEndpoints)
	if err := opts.completeEntry(counts, coverage); err != nil {
		return nil, err
	}
	res := opts.result(counts, coverage)
	if note != "" {
		res["coverage_note"] = note
	}
	return res, nil
}

// resumeFinished signals a finished-but-unmerged segment (merge only, no fetch).
const resumeFinished = "\x00finished"

// resumeState decides where to start: a fresh fetch (""), a resume token, a
// finished segment (merge only), or an already-merged short-circuit.
func (opts PagedOptions) resumeState(seg *store.Segment) (token string, alreadyMerged bool, err error) {
	cur, ok, err := seg.ReadCursor()
	if err != nil {
		return "", false, output.Internalf("reading cursor: %v", err)
	}
	if !ok {
		// A segment with no cursor is stale; discard it and start fresh. If the
		// removal fails, abort: a later O_APPEND fetch would otherwise append
		// onto the stale records.
		if seg.Exists() {
			if rerr := seg.Remove(); rerr != nil {
				return "", false, output.Internalf("clearing stale segment: %v", rerr)
			}
		}
		return "", false, nil
	}
	if cur.Finished() {
		if cur.MergedAs != 0 {
			if st, cerr := opts.DS.CurrentStore(opts.Type); cerr == nil && cur.MergedAs <= st.Version {
				return "", true, nil
			}
		}
		return resumeFinished, false, nil
	}
	if cur.NextPageToken != nil {
		return *cur.NextPageToken, false, nil
	}
	return "", false, nil
}

func (opts PagedOptions) fetchPages(ctx context.Context, seg *store.Segment, token string) *output.CLIError {
	fetchedAt := time.Now().UTC()
	restarted := false
	kind := store.MustKind(opts.Type)
	path := fmt.Sprintf("/api/v1/reports/%d/%s", opts.RunID, kind.Route)

	cur, _, _ := seg.ReadCursor()
	if cur == nil {
		cur = &store.Cursor{}
	}

	for {
		page, err := api.FetchBulkPage(ctx, opts.Client, path, api.BulkPageMax, token)
		if err != nil {
			if api.IsExpiredCursor(err) && !restarted {
				if rerr := seg.Remove(); rerr != nil {
					return output.Internalf("clearing expired segment: %v", rerr)
				}
				restarted = true
				token = ""
				cur = &store.Cursor{}
				fmt.Fprintf(opts.Progress, "run %d %s cursor expired; restarting\n", opts.RunID, opts.Type)
				continue
			}
			return api.AsCLIError(err)
		}

		records := make([][]byte, 0, len(page.Items))
		for _, item := range page.Items {
			if kind.DoubleDecode {
				item = doubleDecode(item)
			}
			records = append(records, item)
		}
		malformed := func(i int, err error) *output.CLIError {
			return &output.CLIError{ExitCode: output.ExitContract, Code: "MALFORMED_ITEM",
				Message: fmt.Sprintf("run %d %s: item %d of page %d: %v", opts.RunID, opts.Type, i+1, cur.Pages+1, err)}
		}
		lines, serr := store.StampLines(records, fetchedAt, opts.RunID)
		var nerr *store.NotObjectError
		if kind.Strict && errors.As(serr, &nerr) {
			return malformed(nerr.Index, serr)
		}
		if serr != nil {
			return output.Internalf("stamping page: %v", serr)
		}
		if kind.Strict {
			for i, line := range lines {
				if _, ierr := store.IdentityFromRecord(kind, line); ierr != nil {
					return malformed(i, ierr)
				}
			}
		}
		if err := seg.AppendLines(lines); err != nil {
			return output.Internalf("appending page: %v", err)
		}

		cur.Pages++
		cur.Items += len(page.Items)
		if page.TotalEndpoints != nil {
			cur.TotalEndpoints = page.TotalEndpoints
		}
		cur.NextPageToken = page.NextPageToken
		if err := seg.WriteCursor(cur); err != nil {
			return output.Internalf("persisting cursor: %v", err)
		}

		fmt.Fprintf(opts.Progress, "run %d %s: %d items fetched\n", opts.RunID, opts.Type, cur.Items)
		if page.NextPageToken == nil || *page.NextPageToken == "" {
			return nil
		}
		token = *page.NextPageToken
	}
}

// computeCoverage reports the run's coverage and, when CLUE history owners cannot be
// counted, a note saying why.
func (opts PagedOptions) computeCoverage(totalEndpoints *int) (*dataset.Coverage, string) {
	if kind := store.MustKind(opts.Type); kind.OwnerStore != "" {
		return opts.ownerCoverage(kind, totalEndpoints)
	}
	cov := &dataset.Coverage{}
	// with_data: distinct remote_endpoint in the run's current membership.
	m, err := opts.DS.ReadManifest()
	if err == nil {
		if ref, ok := m.Membership[dataset.MembershipKey(opts.Type, opts.RunID)]; ok {
			kind := store.MustKind(opts.Type)
			if r, rerr := store.OpenMembership(opts.DS.Path(ref.File), kind); rerr == nil {
				endpoints := map[string]bool{}
				for {
					key, ok, nerr := r.Next()
					if nerr != nil {
						endpoints = nil
						break
					}
					if !ok {
						break
					}
					endpoints[kind.Field(kind.Decode(key), "remote_endpoint")] = true
				}
				r.Close()
				cov.WithData = len(endpoints)
			}
		}
	}
	// queried/empty from total_endpoints when the server provided it.
	if totalEndpoints != nil {
		q := *totalEndpoints
		cov.Queried = &q
		empty := q - cov.WithData
		cov.Empty = &empty
	}
	return cov, ""
}

func (opts PagedOptions) startEntry() error {
	return opts.DS.UpdateManifest(func(m *dataset.Manifest) error {
		entry := dataset.Download{Type: opts.Type, RunID: opts.RunID, Complete: false, FetchedAt: time.Now().UTC()}
		if opts.Type == store.TypeHistory {
			entry.HistoryMode = "full"
		}
		upsertInto(m, entry)
		return nil
	})
}

func (opts PagedOptions) completeEntry(counts store.MergeCounts, cov *dataset.Coverage) error {
	return opts.DS.UpdateManifest(func(m *dataset.Manifest) error {
		for i := range m.Downloads {
			dl := &m.Downloads[i]
			if dl.Type == opts.Type && dl.RunID == opts.RunID && dl.JobID == nil {
				c := counts
				dl.Complete = true
				dl.MergeCounts = &c
				dl.Coverage = cov
				dl.FetchedAt = time.Now().UTC()
				if opts.Type == store.TypeHistory {
					dl.HistoryMode = "full"
				}
				return nil
			}
		}
		return nil
	})
}

func upsertInto(m *dataset.Manifest, dl dataset.Download) {
	for i := range m.Downloads {
		if m.Downloads[i].Type == dl.Type && m.Downloads[i].RunID == dl.RunID && m.Downloads[i].JobID == nil {
			m.Downloads[i] = dl
			return
		}
	}
	m.Downloads = append(m.Downloads, dl)
}

func (opts PagedOptions) result(counts store.MergeCounts, cov *dataset.Coverage) map[string]any {
	res := map[string]any{
		"type":         opts.Type,
		"run_id":       opts.RunID,
		"complete":     true,
		"merge_counts": counts,
		"coverage":     cov,
	}
	if opts.Type == store.TypeHistory {
		res["history_mode"] = "full"
	}
	return res
}

func (opts PagedOptions) alreadyMergedResult() map[string]any {
	return map[string]any{
		"type":           opts.Type,
		"run_id":         opts.RunID,
		"complete":       true,
		"merge_counts":   store.MergeCounts{},
		"already_merged": true,
	}
}
