package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

const attachmentChunkSize = 100

// AttachmentOptions parameterizes an attachment fetch.
type AttachmentOptions struct {
	DS       *dataset.Dataset
	Client   *api.Client
	RunID    int
	Refresh  bool
	URL      bool
	Inline   bool
	Answer   string
	History  string
	Question string
	Name     string
	Progress io.Writer
}

// FetchAttachments scans a run's records for attachment refs, presigns them in
// chunks, downloads (or prints URLs), and rebuilds the attachment index with GC.
func FetchAttachments(ctx context.Context, opts AttachmentOptions) (any, error) {
	release, err := acquireDownload(opts.DS, fmt.Sprintf("seg_attachments_%d.lock", opts.RunID),
		fmt.Sprintf("download busy: another cc-data command is fetching run %d attachments", opts.RunID))
	if err != nil {
		return nil, err
	}
	defer release()

	refs, scanned, err := opts.DS.ScanRunAttachments(opts.RunID)
	if err != nil {
		return nil, output.Internalf("scanning records: %v", err)
	}
	if len(scanned) == 0 {
		return nil, &output.CLIError{ExitCode: output.ExitUsage, Code: "NO_SOURCES", Message: fmt.Sprintf("run %d has no fetched answers or history; fetch one first so attachment refs exist", opts.RunID)}
	}
	if !contains(scanned, "history") {
		fmt.Fprintln(opts.Progress, "note: history not fetched; history-referenced attachments not included")
	}
	if !contains(scanned, "answers") {
		fmt.Fprintln(opts.Progress, "note: answers not fetched; answer-referenced attachments not included")
	}

	refs = applySelectors(refs, opts)
	refs = dedupRefs(refs)

	disposition := "attachment"
	if opts.Inline {
		disposition = "inline"
	}

	if opts.URL {
		return nil, opts.printURLs(ctx, refs, disposition)
	}

	files, missing, err := opts.downloadRefs(ctx, refs, disposition)
	if err != nil {
		return nil, err
	}

	if err := opts.recordAndReindex(scanned, files, missing); err != nil {
		return nil, err
	}

	result := map[string]any{
		"type":     "attachments",
		"run_id":   opts.RunID,
		"scanned":  scanned,
		"files":    files,
		"coverage": map[string]any{"with_data": len(files), "missing": missing},
		"complete": true,
	}
	return result, nil
}

func (opts AttachmentOptions) downloadRefs(ctx context.Context, refs []dataset.AttachRef, disposition string) (files []string, missing []dataset.MissingItem, err error) {
	// Resume: only presign refs whose final file is missing (or all on --refresh).
	var needed []dataset.AttachRef
	for _, r := range refs {
		rel := dataset.AttachmentFileName(r.Source, r.PublicPath, r.Name)
		if !opts.Refresh && fileExists(opts.DS.Path(rel)) {
			files = append(files, rel)
			continue
		}
		needed = append(needed, r)
	}

	for _, chunk := range chunkRefs(needed, attachmentChunkSize) {
		results, perr := opts.Client.PresignAttachments(ctx, opts.RunID, toReqs(chunk), disposition)
		if perr != nil {
			return nil, nil, api.AsCLIError(perr)
		}
		byKey := indexRefs(chunk)
		for _, res := range results.Results {
			key := res.DocID + "\x00" + res.Name
			refs := byKey[key]
			if len(refs) == 0 {
				continue
			}
			// Distinct refs can share a DocID+Name but differ in
			// Source/PublicPath/Collection; consume one per result so a
			// collision does not drop a ref (last-wins) as a bare map would.
			ref := refs[0]
			byKey[key] = refs[1:]
			if res.Error != "" {
				missing = append(missing, dataset.MissingItem{DocID: res.DocID, Name: res.Name, Error: res.Error})
				continue
			}
			rel := dataset.AttachmentFileName(ref.Source, ref.PublicPath, ref.Name)
			if derr := opts.downloadOne(ctx, res.URL, rel); derr != nil {
				return nil, nil, derr
			}
			files = append(files, rel)
		}
	}
	return files, missing, nil
}

func (opts AttachmentOptions) downloadOne(ctx context.Context, url, rel string) error {
	full := opts.DS.Path(rel)
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return output.Internalf("%v", err)
	}
	// A unique temp in the destination dir so concurrent fetches from different
	// runs that reference the same attachment cannot collide on a shared temp
	// path before the atomic rename.
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return output.Internalf("%v", err)
	}
	tmp := f.Name()
	f.Close()
	if err := opts.Client.DownloadURL(ctx, url, tmp); err != nil {
		os.Remove(tmp)
		return api.AsCLIError(err)
	}
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return output.Internalf("finalizing attachment: %v", err)
	}
	fmt.Fprintf(opts.Progress, "downloaded %s\n", rel)
	return nil
}

func (opts AttachmentOptions) printURLs(ctx context.Context, refs []dataset.AttachRef, disposition string) error {
	type urlLine struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	var lines []urlLine
	var missing []dataset.MissingItem
	for _, chunk := range chunkRefs(refs, attachmentChunkSize) {
		results, err := opts.Client.PresignAttachments(ctx, opts.RunID, toReqs(chunk), disposition)
		if err != nil {
			return api.AsCLIError(err)
		}
		expiresAt := time.Now().Add(time.Duration(results.ExpiresInSeconds) * time.Second).UTC().Format(time.RFC3339)
		for _, res := range results.Results {
			if res.Error != "" {
				// Surface per-item failures instead of silently dropping them,
				// mirroring how download mode records a MissingItem.
				missing = append(missing, dataset.MissingItem{DocID: res.DocID, Name: res.Name, Error: res.Error})
				fmt.Fprintf(opts.Progress, "warning: could not presign %s (%s): %s\n", res.Name, res.DocID, res.Error)
				continue
			}
			if res.URL != "" {
				lines = append(lines, urlLine{URL: res.URL, ExpiresAt: expiresAt})
			}
		}
	}
	out := output.Stdout()
	if len(lines) == 1 {
		fmt.Fprintln(out, lines[0].URL)
	} else {
		enc := json.NewEncoder(out)
		for _, l := range lines {
			if err := enc.Encode(l); err != nil {
				return output.Internalf("%v", err)
			}
		}
	}
	if len(missing) > 0 {
		// Details already went to stderr above; reflect the failures in the exit
		// code (URL mode has no result line to carry a coverage.missing list).
		return &output.CLIError{ExitCode: output.ExitContract, Code: "PRESIGN_INCOMPLETE",
			Message: fmt.Sprintf("%d attachment(s) could not be presigned", len(missing)), Silent: true}
	}
	return nil
}

func (opts AttachmentOptions) recordAndReindex(scanned, files []string, missing []dataset.MissingItem) error {
	return opts.DS.UpdateManifest(func(m *dataset.Manifest) error {
		cov := &dataset.Coverage{WithData: len(files), Missing: missing}
		entry := dataset.Download{
			Type:      "attachments",
			RunID:     opts.RunID,
			Scanned:   scanned,
			Files:     files,
			Coverage:  cov,
			Complete:  true,
			FetchedAt: time.Now().UTC(),
		}
		upsertAttachmentEntry(m, entry)
		return opts.DS.RebuildAttachmentIndex(m)
	})
}

func upsertAttachmentEntry(m *dataset.Manifest, dl dataset.Download) {
	for i := range m.Downloads {
		if m.Downloads[i].Type == "attachments" && m.Downloads[i].RunID == dl.RunID {
			m.Downloads[i] = dl
			return
		}
	}
	m.Downloads = append(m.Downloads, dl)
}

func applySelectors(refs []dataset.AttachRef, opts AttachmentOptions) []dataset.AttachRef {
	if opts.Answer == "" && opts.History == "" && opts.Question == "" && opts.Name == "" {
		return refs
	}
	var out []dataset.AttachRef
	for _, r := range refs {
		if opts.Answer != "" && !(r.Collection == "answers" && r.DocID == opts.Answer) {
			continue
		}
		if opts.History != "" && !(r.Collection == "history" && r.DocID == opts.History) {
			continue
		}
		if opts.Question != "" && r.Identity.QuestionID != opts.Question {
			continue
		}
		if opts.Name != "" && r.Name != opts.Name {
			continue
		}
		out = append(out, r)
	}
	return out
}

func dedupRefs(refs []dataset.AttachRef) []dataset.AttachRef {
	seen := map[string]bool{}
	var out []dataset.AttachRef
	for _, r := range refs {
		key := r.Source + "|" + r.PublicPath
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func chunkRefs(refs []dataset.AttachRef, size int) [][]dataset.AttachRef {
	var chunks [][]dataset.AttachRef
	for i := 0; i < len(refs); i += size {
		end := i + size
		if end > len(refs) {
			end = len(refs)
		}
		chunks = append(chunks, refs[i:end])
	}
	return chunks
}

func toReqs(refs []dataset.AttachRef) []api.AttachmentRefReq {
	reqs := make([]api.AttachmentRefReq, 0, len(refs))
	for _, r := range refs {
		reqs = append(reqs, api.AttachmentRefReq{Collection: r.Collection, Source: r.Source, DocID: r.DocID, Name: r.Name})
	}
	return reqs
}

func indexRefs(refs []dataset.AttachRef) map[string][]dataset.AttachRef {
	m := make(map[string][]dataset.AttachRef, len(refs))
	for _, r := range refs {
		key := r.DocID + "\x00" + r.Name
		m[key] = append(m[key], r)
	}
	return m
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
