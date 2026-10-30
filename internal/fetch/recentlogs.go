package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// recentLogsBatch is how many rows reach the segment per append.
const recentLogsBatch = 500

// RecentLogsOptions parameterizes a pull of a class's recent logs.
type RecentLogsOptions struct {
	DS       *dataset.Dataset
	Client   *api.Client
	ClassID  int
	Start    string // epoch milliseconds or RFC 3339; "" for none
	End      string
	Refresh  bool
	Progress io.Writer
}

// validate checks the options and returns the start and end they ask for.
func (o RecentLogsOptions) validate() (start, end *int64, err error) {
	if o.ClassID < 1 {
		return nil, nil, output.Usagef("class id must be a positive integer")
	}
	if o.Refresh && (o.Start != "" || o.End != "") {
		return nil, nil, output.Usagef("--refresh reads the whole window and cannot be combined with --start or --end")
	}
	parse := func(flag, s string) (*int64, error) {
		if s == "" {
			return nil, nil
		}
		ms, err := ParseLogBound(s)
		if err != nil {
			return nil, output.Usagef("--%s %q: give epoch milliseconds or an RFC 3339 time", flag, s)
		}
		return &ms, nil
	}
	if start, err = parse("start", o.Start); err != nil {
		return nil, nil, err
	}
	if end, err = parse("end", o.End); err != nil {
		return nil, nil, err
	}
	if start != nil && end != nil && *end <= *start {
		return nil, nil, output.Usagef("--end must be after --start")
	}
	return start, end, nil
}

// ParseLogBound reads a recent-logs bound: all digits is epoch milliseconds, anything else
// an RFC 3339 time.
func ParseLogBound(s string) (int64, error) {
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && s[0] != '-' && s[0] != '+' {
		return ms, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// FetchRecentLogs pulls a class's recent log events into the dataset's recent_logs store and
// records the pull, with its cursor, on the class's download entry. Nothing is stored, and the
// cursor does not move, unless the whole response parsed and checked out.
func FetchRecentLogs(ctx context.Context, o RecentLogsOptions) (map[string]any, error) {
	start, end, err := o.validate()
	if err != nil {
		return nil, err
	}
	class := o.ClassID
	seg := store.OpenSegment(o.DS.Dir, store.TypeRecentLogs, class)
	release, err := acquireDownload(o.DS, seg.LockName(),
		fmt.Sprintf("download busy: another cc-data command is pulling class %d's recent logs", class))
	if err != nil {
		return nil, err
	}
	defer release()

	if err := o.mergeLeftover(seg); err != nil {
		return nil, err
	}

	prior, err := o.priorEntry()
	if err != nil {
		return nil, err
	}
	q := api.RecentLogsQuery{Start: start, End: end}
	if !q.Bounded() && !o.Refresh && prior.RecentLogs != nil {
		q.Cursor = prior.RecentLogs.Cursor
	}

	if err := os.MkdirAll(filepath.Join(o.DS.Dir, store.SegmentsDir), 0o700); err != nil {
		return nil, output.Internalf("segments directory: %v", err)
	}
	body := filepath.Join(o.DS.Dir, store.SegmentsDir, fmt.Sprintf("seg_%s_%d.body.json", store.TypeRecentLogs, class))
	defer os.Remove(body)

	expired := false
	err = o.Client.StreamRecentLogs(ctx, class, q, body)
	if err != nil && q.Cursor != "" && api.IsExpiredCursor(err) {
		expired = true
		q = api.RecentLogsQuery{}
		fmt.Fprintf(o.Progress, "warning: class %d's stored cursor has expired, so the whole window is read again; "+
			"events between the previous pull and the window's start are not in recent_logs, and come from a Student Actions run\n", class)
		err = o.Client.StreamRecentLogs(ctx, class, q, body)
	}
	if err != nil {
		if api.IsPartialDownload(err) {
			return nil, cutOff(class)
		}
		return nil, api.AsCLIError(err)
	}

	fetchedAt := time.Now().UTC()
	env, rows, cliErr := o.decodeIntoSegment(seg, body, fetchedAt, q.Bounded())
	if cliErr != nil {
		seg.Remove()
		return nil, cliErr
	}

	var counts store.MergeCounts
	if rows > 0 {
		if err := seg.WriteCursor(&store.Cursor{Pages: 1, Items: rows}); err != nil {
			seg.Remove()
			return nil, output.Internalf("persisting cursor: %v", err)
		}
		if counts, err = o.DS.MergeCompact(store.TypeRecentLogs, class, seg); err != nil {
			return nil, output.Internalf("merge: %v", err)
		}
	}

	pull := &dataset.RecentLogsPull{
		WindowStart:     env.WindowStart,
		NightlyThrough:  env.NightlyThrough,
		Since:           env.Since,
		Until:           env.Until,
		QueryStartedAt:  env.QueryStartedAt,
		LatestTimestamp: env.LatestTimestamp,
		Rows:            rows,
	}
	cursor := "unchanged"
	switch {
	case env.NextCursor == nil:
		cursor = "none"
	case !q.Bounded():
		cursor = "stored"
	}
	if !q.Bounded() {
		if env.NextCursor != nil {
			pull.Cursor = *env.NextCursor
		}
	} else if prior.RecentLogs != nil {
		pull.Cursor = prior.RecentLogs.Cursor
	}
	if err := o.DS.UpsertDownload(dataset.Download{
		Type:        store.TypeRecentLogs,
		ClassID:     class,
		Complete:    true,
		FetchedAt:   fetchedAt,
		MergeCounts: &counts,
		RecentLogs:  pull,
	}); err != nil {
		return nil, output.Internalf("recording the pull: %v", err)
	}
	fmt.Fprintf(o.Progress, "class %d recent_logs: %d rows, %d new, %d updated\n", class, rows, counts.New, counts.Updated)

	return map[string]any{
		"type":             store.TypeRecentLogs,
		"class_id":         class,
		"window_start":     env.WindowStart,
		"nightly_through":  env.NightlyThrough,
		"since":            env.Since,
		"until":            env.Until,
		"query_started_at": env.QueryStartedAt,
		"latest_timestamp": env.LatestTimestamp,
		"rows":             rows,
		"merge_counts":     counts,
		"cursor":           cursor,
		"expired_cursor":   expired,
	}, nil
}

// mergeLeftover merges this class's segment if an earlier pull parsed it whole but did not
// merge it, and otherwise discards it. The leftover's rows are good and the stored cursor
// still predates them, so nothing is skipped.
func (o RecentLogsOptions) mergeLeftover(seg *store.Segment) error {
	cur, ok, err := seg.ReadCursor()
	if err != nil {
		return output.Internalf("reading cursor: %v", err)
	}
	if ok && cur.Finished() {
		if _, err := o.DS.MergeCompact(store.TypeRecentLogs, o.ClassID, seg); err != nil {
			return output.Internalf("merging an earlier pull: %v", err)
		}
	}
	if err := seg.Remove(); err != nil {
		return output.Internalf("clearing stale segment: %v", err)
	}
	return nil
}

// priorEntry is the class's download entry, or a zero one.
func (o RecentLogsOptions) priorEntry() (dataset.Download, error) {
	m, err := o.DS.ReadManifest()
	if err != nil {
		return dataset.Download{}, output.Internalf("reading manifest: %v", err)
	}
	for _, dl := range m.Downloads {
		if dl.Type == store.TypeRecentLogs && dl.ClassID == o.ClassID {
			return dl, nil
		}
	}
	return dataset.Download{}, nil
}

// decodeIntoSegment decodes the body file into the segment and checks the envelope against
// the class asked for, the rows read and, for a pull with no bounds, the cursor it must carry.
func (o RecentLogsOptions) decodeIntoSegment(seg *store.Segment, body string, fetchedAt time.Time, bounded bool) (recentLogsEnvelope, int, *output.CLIError) {
	f, err := os.Open(body)
	if err != nil {
		return recentLogsEnvelope{}, 0, output.Internalf("opening the response: %v", err)
	}
	defer f.Close()
	rows := 0
	env, err := decodeRecentLogs(f, func(batch [][]byte) error {
		lines, err := store.MustKind(store.TypeRecentLogs).StampLines(batch, fetchedAt, o.ClassID)
		if err == nil {
			err = seg.AppendLines(lines)
		}
		if err != nil {
			return &localError{err}
		}
		rows += len(batch)
		return nil
	})
	var rowErr *rowIdentityError
	var badRow *badRowError
	var shape *shapeError
	var local *localError
	switch {
	case errors.As(err, &rowErr):
		return env, 0, &output.CLIError{ExitCode: output.ExitContract, Code: "MALFORMED_ITEM",
			Message: fmt.Sprintf("class %d %s: row %d has no id (id %s)", o.ClassID, store.TypeRecentLogs, rowErr.Row, rowErr.Err.Reason)}
	case errors.As(err, &badRow):
		return env, 0, badResponse("class %d %s: row %d: %v", o.ClassID, store.TypeRecentLogs, badRow.Row, badRow.Err)
	case errors.As(err, &local):
		return env, 0, output.Internalf("appending rows: %v", local.err)
	case errors.As(err, &shape):
		return env, 0, badResponse("class %d %s: %v", o.ClassID, store.TypeRecentLogs, shape.err)
	case err != nil:
		return env, 0, cutOff(o.ClassID)
	case env.ClassID != o.ClassID:
		return env, 0, badResponse("class %d %s: the response is for class %d", o.ClassID, store.TypeRecentLogs, env.ClassID)
	case env.Count != rows:
		return env, 0, badResponse("class %d %s: the response counts %d rows and holds %d", o.ClassID, store.TypeRecentLogs, env.Count, rows)
	case !bounded && env.NextCursor == nil:
		return env, 0, badResponse("class %d %s: the response has no next_cursor", o.ClassID, store.TypeRecentLogs)
	}
	return env, rows, nil
}

func cutOff(class int) *output.CLIError {
	return &output.CLIError{ExitCode: output.ExitTransient, Code: "TRANSIENT",
		Message: fmt.Sprintf("class %d %s: the recent-logs answer was cut off; re-run", class, store.TypeRecentLogs)}
}

func badResponse(format string, args ...any) *output.CLIError {
	return &output.CLIError{ExitCode: output.ExitContract, Code: "BAD_RESPONSE", Message: fmt.Sprintf(format, args...)}
}

// shapeError is a response that parsed but is not the shape the route answers.
type shapeError struct{ err error }

func (e *shapeError) Error() string { return e.err.Error() }

type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }

// rowIdentityError is a row whose id is unusable; Row counts from 1.
type rowIdentityError struct {
	Row int
	Err *store.IdentityError
}

func (e *rowIdentityError) Error() string { return fmt.Sprintf("row %d: %v", e.Row, e.Err) }

// badRowError is a row that is not a JSON object; Row counts from 1.
type badRowError struct {
	Row int
	Err error
}

func (e *badRowError) Error() string { return fmt.Sprintf("row %d: %v", e.Row, e.Err) }

// recentLogsEnvelope is the response's fields other than rows.
type recentLogsEnvelope struct {
	ClassID         int     `json:"class_id"`
	WindowStart     int64   `json:"window_start"`
	NightlyThrough  string  `json:"nightly_through"`
	Since           int64   `json:"since"`
	Until           *int64  `json:"until"`
	QueryStartedAt  int64   `json:"query_started_at"`
	Count           int     `json:"count"`
	LatestTimestamp *int64  `json:"latest_timestamp"`
	NextCursor      *string `json:"next_cursor"`
}

// decodeRecentLogs reads a recent-logs response, handing its rows to sink in batches as they
// are read, byte for byte, and returning the other fields. It fails on a row without a usable
// id, on anything after the closing brace, and on a missing rows or count. A body cut before
// its closing fields fails only after its earlier rows reached sink, so the caller keeps
// nothing until it returns cleanly.
func decodeRecentLogs(r io.Reader, sink func([][]byte) error) (recentLogsEnvelope, error) {
	var env recentLogsEnvelope
	kind := store.MustKind(store.TypeRecentLogs)
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := expectDelim(dec, '{'); err != nil {
		return env, err
	}
	fields := map[string]json.RawMessage{}
	sawRows := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return env, err
		}
		key, _ := tok.(string)
		if key != "rows" {
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return env, err
			}
			fields[key] = v
			continue
		}
		if sawRows {
			return env, &shapeError{errors.New("the response has rows twice")}
		}
		sawRows = true
		if err := expectDelim(dec, '['); err != nil {
			return env, err
		}
		batch := make([][]byte, 0, recentLogsBatch)
		n := 0
		for dec.More() {
			var row json.RawMessage
			if err := dec.Decode(&row); err != nil {
				return env, err
			}
			n++
			if _, err := store.IdentityFromRecord(kind, row); err != nil {
				var idErr *store.IdentityError
				if errors.As(err, &idErr) {
					return env, &rowIdentityError{Row: n, Err: idErr}
				}
				return env, &badRowError{Row: n, Err: err}
			}
			batch = append(batch, row)
			if len(batch) == recentLogsBatch {
				if err := sink(batch); err != nil {
					return env, err
				}
				batch = make([][]byte, 0, recentLogsBatch)
			}
		}
		if err := expectDelim(dec, ']'); err != nil {
			return env, err
		}
		if len(batch) > 0 {
			if err := sink(batch); err != nil {
				return env, err
			}
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return env, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return env, &shapeError{errors.New("the response continues after its closing brace")}
	}
	if !sawRows {
		return env, &shapeError{errors.New("the response has no rows")}
	}
	if v, ok := fields["count"]; !ok || string(v) == "null" {
		return env, &shapeError{errors.New("the response has no count")}
	}
	raw, _ := json.Marshal(fields)
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, &shapeError{err}
	}
	return env, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return &shapeError{fmt.Errorf("expected %q, got %v", want, tok)}
	}
	return nil
}
