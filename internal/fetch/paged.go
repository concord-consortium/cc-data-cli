package fetch

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// PagedOptions parameterizes an answers or history fetch (one code path).
type PagedOptions struct {
	DS       *dataset.Dataset
	Client   *api.Client
	RunID    int
	Type     string // store.TypeAnswers | store.TypeHistory
	Refresh  bool
	Progress io.Writer
}

// FetchPaged consumes the paged answers/history envelope: segment append per
// page, cursor persistence, resume, EXPIRED_CURSOR restart, double-decode,
// membership, merge, and coverage.
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
	if err := opts.startEntry(); err != nil {
		return nil, err
	}

	token, alreadyMerged, err := opts.resumeState(seg)
	if err != nil {
		return nil, err
	}
	if alreadyMerged {
		return opts.alreadyMergedResult(), nil
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
	coverage := opts.computeCoverage(totalEndpoints)
	if err := opts.completeEntry(counts, coverage); err != nil {
		return nil, err
	}
	return opts.result(counts, coverage), nil
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
		// A segment with no cursor is stale; discard it and start fresh.
		if seg.Exists() {
			seg.Remove()
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
	path := fmt.Sprintf("/api/v1/reports/%d/%s", opts.RunID, opts.Type)

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
			records = append(records, doubleDecode(item))
		}
		if err := seg.AppendPage(records, fetchedAt, opts.RunID); err != nil {
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

func (opts PagedOptions) computeCoverage(totalEndpoints *int) *dataset.Coverage {
	cov := &dataset.Coverage{}
	// with_data: distinct remote_endpoint in the run's current membership.
	m, err := opts.DS.ReadManifest()
	if err == nil {
		if ref, ok := m.Membership[dataset.MembershipKey(opts.Type, opts.RunID)]; ok {
			if ids, rerr := store.ReadMembershipFile(opts.DS.Path(ref.File)); rerr == nil {
				endpoints := map[string]bool{}
				for _, id := range ids {
					endpoints[id.RemoteEndpoint] = true
				}
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
	return cov
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
