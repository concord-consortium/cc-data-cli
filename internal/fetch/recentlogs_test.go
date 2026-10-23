package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// logsBody is a response framed as the controller writes it: the opening fields, then rows,
// then the closing fields, so a test can cut it at any byte.
type logsBody struct {
	class      int
	rows       []string
	count      int // -1 means len(rows)
	nextCursor string
	until      string // "" means null
}

func (b logsBody) String() string {
	count := b.count
	if count < 0 {
		count = len(b.rows)
	}
	next := "null"
	if b.nextCursor != "" {
		next = fmt.Sprintf("%q", b.nextCursor)
	}
	until := "null"
	if b.until != "" {
		until = b.until
	}
	return fmt.Sprintf(`{"class_id":%d,"window_start":1792454400000,"nightly_through":"2026-10-20","since":1792540000000,"until":%s,"query_started_at":1792540600000,"rows":[%s],"count":%d,"latest_timestamp":1792540599000,"next_cursor":%s}`,
		b.class, until, strings.Join(b.rows, ","), count, next)
}

func logRow(id string) string {
	return fmt.Sprintf(`{"id":%q,"session":"s","application":"CLUE","activity":"a","event":"e","event_value":null,"time":1792540500,"parameters":"{\"tileType\":\"Dataflow\"}","extras":null,"run_remote_endpoint":"https://learn.example/l/1","timestamp":1792540500123,"user_id":7,"primary_user_id":null}`, id)
}

// recentLogsServer answers the recent-logs route from a queue of responses and records each
// request's query.
type recentLogsServer struct {
	*httptest.Server
	mu        sync.Mutex
	queue     []func(http.ResponseWriter)
	queries   []url.Values
	fallback  func(http.ResponseWriter)
	requested chan struct{} // when set, receives once per request before it is answered
	release   chan struct{} // when set, each answer waits for it
}

func newRecentLogsServer(t *testing.T) *recentLogsServer {
	s := &recentLogsServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/classes/") || !strings.HasSuffix(r.URL.Path, "/recent-logs") {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		var answer func(http.ResponseWriter)
		if len(s.queue) > 0 {
			answer, s.queue = s.queue[0], s.queue[1:]
		} else {
			answer = s.fallback
		}
		s.mu.Unlock()
		if s.requested != nil {
			s.requested <- struct{}{}
		}
		if s.release != nil {
			<-s.release
		}
		if answer == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		answer(w)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recentLogsServer) push(answers ...func(http.ResponseWriter)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, answers...)
}

func (s *recentLogsServer) requests() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.queries...)
}

func body(s string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, s)
	}
}

func refusal(status int, code string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":%q,"message":"refused"}`, code)
	}
}

// cutConnection writes prefix and closes the connection mid-body, as a server failure after
// the first chunk does.
func cutConnection(prefix string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, prefix)
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}
}

func pullLogs(t *testing.T, d *dataset.Dataset, s *recentLogsServer, o RecentLogsOptions) (map[string]any, error, string) {
	t.Helper()
	var progress bytes.Buffer
	o.DS, o.Client, o.Progress = d, fastClient(s.URL), &progress
	if o.ClassID == 0 {
		o.ClassID = 590
	}
	res, err := FetchRecentLogs(context.Background(), o)
	return res, err, progress.String()
}

func mustPullLogs(t *testing.T, d *dataset.Dataset, s *recentLogsServer, o RecentLogsOptions) map[string]any {
	t.Helper()
	res, err, progress := pullLogs(t, d, s, o)
	if err != nil {
		t.Fatalf("pull: %v (progress %s)", err, progress)
	}
	return res
}

func classEntry(t *testing.T, d *dataset.Dataset, class int) dataset.Download {
	t.Helper()
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, dl := range m.Downloads {
		if dl.Type == store.TypeRecentLogs && dl.ClassID == class {
			return dl
		}
	}
	t.Fatalf("no recent_logs entry for class %d in %+v", class, m.Downloads)
	return dataset.Download{}
}

func storedCursor(t *testing.T, d *dataset.Dataset) string {
	t.Helper()
	dl := classEntry(t, d, 590)
	if dl.RecentLogs == nil {
		t.Fatal("the entry has no pull")
	}
	return dl.RecentLogs.Cursor
}

func storeCount(t *testing.T, d *dataset.Dataset) int {
	t.Helper()
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	return m.Stores[store.TypeRecentLogs].Count
}

// assertNothingLeft checks a failed pull left no segment, cursor or body behind.
func assertNothingLeft(t *testing.T, d *dataset.Dataset) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(d.Dir, store.SegmentsDir))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func cliCode(t *testing.T, err error) *output.CLIError {
	t.Helper()
	var cliErr *output.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want a CLIError", err, err)
	}
	return cliErr
}

func TestRecentLogsCursorIsStoredAndSent(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e1"), logRow("e2")}, count: -1, nextCursor: "v1.first"}.String()),
		body(logsBody{class: 590, rows: []string{logRow("e2"), logRow("e3")}, count: -1, nextCursor: "v1.second"}.String()))

	res := mustPullLogs(t, d, s, RecentLogsOptions{})
	if res["cursor"] != "stored" || res["rows"] != 2 || res["expired_cursor"] != false {
		t.Fatalf("first result = %v", res)
	}
	keys := slices.Sorted(maps.Keys(res))
	if strings.Join(keys, ",") != "class_id,cursor,expired_cursor,latest_timestamp,merge_counts,nightly_through,query_started_at,rows,since,type,until,window_start" {
		t.Errorf("result keys = %v", keys)
	}
	if res["type"] != "recent_logs" || res["class_id"] != 590 || res["window_start"] != int64(1792454400000) || res["nightly_through"] != "2026-10-20" ||
		res["since"] != int64(1792540000000) || res["query_started_at"] != int64(1792540600000) || *res["latest_timestamp"].(*int64) != 1792540599000 || res["until"].(*int64) != nil {
		t.Errorf("result = %v", res)
	}
	if got := storedCursor(t, d); got != "v1.first" {
		t.Fatalf("stored cursor = %q", got)
	}
	res = mustPullLogs(t, d, s, RecentLogsOptions{})
	if c := res["merge_counts"].(store.MergeCounts); c != (store.MergeCounts{Fetched: 2, New: 1, Updated: 1}) {
		t.Fatalf("second merge counts = %+v", c)
	}
	q := s.requests()
	if len(q) != 2 || len(q[0]) != 0 || q[1].Encode() != "cursor=v1.first" {
		t.Fatalf("queries = %v", q)
	}
	if storeCount(t, d) != 3 || storedCursor(t, d) != "v1.second" {
		t.Fatalf("store %d, cursor %q", storeCount(t, d), storedCursor(t, d))
	}
	dl := classEntry(t, d, 590)
	if !dl.Complete || dl.FetchedAt.IsZero() || dl.RunID != 0 || dl.RecentLogs.NightlyThrough != "2026-10-20" || dl.RecentLogs.Rows != 2 {
		t.Errorf("entry = %+v %+v", dl, dl.RecentLogs)
	}
}

func TestRecentLogsBoundsLeaveTheCursor(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e1")}, count: -1, nextCursor: "v1.first"}.String()),
		body(logsBody{class: 590, rows: []string{logRow("e2")}, count: -1, nextCursor: "v1.from-start"}.String()),
		body(logsBody{class: 590, rows: []string{logRow("e3")}, count: -1, until: "1792540560000"}.String()))
	mustPullLogs(t, d, s, RecentLogsOptions{})
	before, _ := json.Marshal(storedCursor(t, d))

	res := mustPullLogs(t, d, s, RecentLogsOptions{Start: "1792540500000"})
	if res["cursor"] != "unchanged" {
		t.Errorf("a --start pull reported cursor %v", res["cursor"])
	}
	res = mustPullLogs(t, d, s, RecentLogsOptions{Start: "2026-10-21T00:00:00Z", End: "1792544400000"})
	if res["cursor"] != "none" {
		t.Errorf("a --start --end pull reported cursor %v", res["cursor"])
	}
	after, _ := json.Marshal(storedCursor(t, d))
	if !bytes.Equal(before, after) {
		t.Errorf("stored cursor moved from %s to %s", before, after)
	}
	q := s.requests()
	if q[1].Encode() != "start=1792540500000" || q[2].Encode() != "end=1792544400000&start=1792540800000" {
		t.Errorf("queries = %v", q)
	}
	if storeCount(t, d) != 3 {
		t.Errorf("store = %d", storeCount(t, d))
	}
}

func TestRecentLogsRefreshSendsNothing(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e1")}, count: -1, nextCursor: "v1.first"}.String()),
		body(logsBody{class: 590, rows: []string{logRow("e1")}, count: -1, nextCursor: "v1.second"}.String()))
	mustPullLogs(t, d, s, RecentLogsOptions{})
	mustPullLogs(t, d, s, RecentLogsOptions{Refresh: true})
	if q := s.requests(); len(q[1]) != 0 {
		t.Errorf("a refresh sent %v", q[1])
	}
	if got := storedCursor(t, d); got != "v1.second" {
		t.Errorf("cursor after refresh = %q", got)
	}
}

func TestRecentLogsValidate(t *testing.T) {
	for _, o := range []RecentLogsOptions{
		{ClassID: 590, Start: "1792540800000", End: "1792540800000"},
		{ClassID: 590, Start: "1792540800000", End: "1792540000000"},
		{ClassID: 590, Refresh: true, Start: "1792540800000"},
		{ClassID: 590, Refresh: true, End: "1792540800000"},
		{ClassID: 590, Start: "yesterday"},
		{ClassID: 0},
		{ClassID: -3},
	} {
		if _, _, err := o.validate(); err == nil || cliCode(t, err).ExitCode != output.ExitUsage {
			t.Errorf("%+v: err = %v, want a usage error", o, err)
		}
	}
	start, end, err := RecentLogsOptions{ClassID: 590, Start: "1792540800000", End: "1792540800001"}.validate()
	if err != nil || *start != 1792540800000 || *end != 1792540800001 {
		t.Errorf("a valid range: %v %v %v", start, end, err)
	}
}

func TestParseLogBound(t *testing.T) {
	for in, want := range map[string]int64{
		"1792540800000":               1792540800000,
		"2026-10-21T12:00:00Z":        1792584000000,
		"2026-10-21T12:00:00.5-04:00": 1792598400500,
	} {
		if got, err := ParseLogBound(in); err != nil || got != want {
			t.Errorf("ParseLogBound(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"yesterday", "", "-5", "2026-10-21"} {
		if _, err := ParseLogBound(in); err == nil {
			t.Errorf("ParseLogBound(%q) accepted", in)
		}
	}
}

func TestRecentLogsExpiredCursorRereadsTheWindow(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e1")}, count: -1, nextCursor: "v1.old"}.String()),
		refusal(http.StatusGone, "EXPIRED_CURSOR"),
		body(logsBody{class: 590, rows: []string{logRow("e2")}, count: -1, nextCursor: "v1.new"}.String()))
	mustPullLogs(t, d, s, RecentLogsOptions{})
	res, err, progress := pullLogs(t, d, s, RecentLogsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := s.requests()
	if len(q) != 3 || q[1].Get("cursor") != "v1.old" || len(q[2]) != 0 {
		t.Fatalf("queries = %v", q)
	}
	if res["expired_cursor"] != true || res["cursor"] != "stored" || storedCursor(t, d) != "v1.new" {
		t.Errorf("result = %v, cursor %q", res, storedCursor(t, d))
	}
	if !strings.Contains(progress, "Student Actions run") || !strings.Contains(progress, "warning:") {
		t.Errorf("progress = %q", progress)
	}
	if storeCount(t, d) != 2 {
		t.Errorf("store = %d", storeCount(t, d))
	}
}

func TestRecentLogsOtherCursorRefusalsAreErrors(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e1")}, count: -1, nextCursor: "v1.old"}.String()),
		refusal(http.StatusBadRequest, "BAD_REQUEST"))
	mustPullLogs(t, d, s, RecentLogsOptions{})
	_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
	if cliCode(t, err).Code != "BAD_REQUEST" || len(s.requests()) != 2 || storedCursor(t, d) != "v1.old" {
		t.Errorf("err = %v, requests %d, cursor %q", err, len(s.requests()), storedCursor(t, d))
	}
}

// A failed pull leaves the store and the stored cursor as they were.
func assertFailedPull(t *testing.T, d *dataset.Dataset, err error, code string, mention string) {
	t.Helper()
	cliErr := cliCode(t, err)
	if cliErr.Code != code || !strings.Contains(cliErr.Message, mention) {
		t.Errorf("err = %+v, want %s naming %q", cliErr, code, mention)
	}
	if storeCount(t, d) != 1 || storedCursor(t, d) != "v1.first" {
		t.Errorf("store %d, cursor %q after a failed pull", storeCount(t, d), storedCursor(t, d))
	}
	assertNothingLeft(t, d)
}

func seededPull(t *testing.T) (*dataset.Dataset, *recentLogsServer) {
	t.Helper()
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e1")}, count: -1, nextCursor: "v1.first"}.String()))
	mustPullLogs(t, d, s, RecentLogsOptions{})
	return d, s
}

func TestRecentLogsBodyCutBeforeItsClosingFields(t *testing.T) {
	d, s := seededPull(t)
	full := logsBody{class: 590, rows: []string{logRow("e2"), logRow("e3")}, count: -1, nextCursor: "v1.second"}.String()
	s.push(body(full[:strings.Index(full, `,"count"`)]))
	_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
	assertFailedPull(t, d, err, "TRANSIENT", "cut off")
}

func TestRecentLogsConnectionCutMidRows(t *testing.T) {
	d, s := seededPull(t)
	full := logsBody{class: 590, rows: []string{logRow("e2"), logRow("e3")}, count: -1, nextCursor: "v1.second"}.String()
	s.push(cutConnection(full[:strings.Index(full, `{"id":"e3"`)]))
	_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
	assertFailedPull(t, d, err, "TRANSIENT", "cut off")
	if strings.Contains(cliCode(t, err).Message, "report filter") {
		t.Errorf("a cut recent-logs answer suggested a report filter: %v", err)
	}
	if n := len(s.requests()); n != 2 {
		t.Errorf("a body cut after its first byte was retried: %d requests", n)
	}
}

func TestRecentLogsTrailingContentIsRefused(t *testing.T) {
	d, s := seededPull(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e2")}, count: -1, nextCursor: "v1.second"}.String() + `{}`))
	_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
	assertFailedPull(t, d, err, "TRANSIENT", "cut off")
}

func TestRecentLogsEnvelopeChecks(t *testing.T) {
	for name, b := range map[string]logsBody{
		"count one higher": {class: 590, rows: []string{logRow("e2")}, count: 2, nextCursor: "v1.second"},
		"another class":    {class: 591, rows: []string{logRow("e2")}, count: -1, nextCursor: "v1.second"},
	} {
		t.Run(name, func(t *testing.T) {
			d, s := seededPull(t)
			s.push(body(b.String()))
			_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
			assertFailedPull(t, d, err, "BAD_RESPONSE", "class 590")
		})
	}
}

func TestRecentLogsRowWithoutAnID(t *testing.T) {
	rows := map[string]string{
		"null":    strings.Replace(logRow("x"), `"id":"x"`, `"id":null`, 1),
		"empty":   logRow(""),
		"missing": strings.Replace(logRow("x"), `"id":"x",`, ``, 1),
		"number":  strings.Replace(logRow("x"), `"id":"x"`, `"id":17`, 1),
	}
	for name, bad := range rows {
		t.Run(name, func(t *testing.T) {
			d, s := seededPull(t)
			s.push(body(logsBody{class: 590, rows: []string{logRow("e2"), bad}, count: -1, nextCursor: "v1.second"}.String()))
			_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
			assertFailedPull(t, d, err, "MALFORMED_ITEM", "class 590")
			if !strings.Contains(err.Error(), "row 2") {
				t.Errorf("err = %v, want it to name row 2", err)
			}
		})
	}
}

func TestRecentLogsRowThatIsNotAnObject(t *testing.T) {
	d, s := seededPull(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e2"), `"x"`}, count: -1, nextCursor: "v1.second"}.String()))
	_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
	assertFailedPull(t, d, err, "BAD_RESPONSE", "row 2")
}

func TestRecentLogsEmptyResponseStillRecordsThePull(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, count: -1, nextCursor: "v1.empty"}.String()))
	res := mustPullLogs(t, d, s, RecentLogsOptions{})
	if res["rows"] != 0 || res["cursor"] != "stored" || storedCursor(t, d) != "v1.empty" || !classEntry(t, d, 590).Complete {
		t.Errorf("result = %v, cursor %q", res, storedCursor(t, d))
	}
	assertNothingLeft(t, d)
}

func TestRecentLogsDropsAnUnfinishedSegment(t *testing.T) {
	d, s := seededPull(t)
	appendPage(t, store.OpenSegment(d.Dir, store.TypeRecentLogs, 590), [][]byte{[]byte(logRow("partial"))}, time.Now(), 590)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e2")}, count: -1, nextCursor: "v1.second"}.String()))
	mustPullLogs(t, d, s, RecentLogsOptions{})
	if storeCount(t, d) != 2 {
		t.Errorf("store = %d, want the partial segment's row dropped", storeCount(t, d))
	}
}

func TestRecentLogsRowsArePassedThroughAsReceived(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.push(body(logsBody{class: 590, rows: []string{logRow("e1")}, count: -1, nextCursor: "v1.first"}.String()))
	mustPullLogs(t, d, s, RecentLogsOptions{})
	m, _ := d.ReadManifest()
	raw, err := os.ReadFile(d.Path(m.Stores[store.TypeRecentLogs].File))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &row); err != nil {
		t.Fatal(err)
	}
	if row["parameters"] != `{"tileType":"Dataflow"}` || row["_class_id"] != float64(590) || row["timestamp"] != float64(1792540500123) {
		t.Errorf("stored row = %v", row)
	}
	if _, ok := row["_fetched_at"]; !ok {
		t.Error("no _fetched_at")
	}
}

func TestRecentLogsMergesAFinishedSegmentFirst(t *testing.T) {
	d, s := seededPull(t)
	seg := store.OpenSegment(d.Dir, store.TypeRecentLogs, 590)
	appendPage(t, seg, [][]byte{[]byte(logRow("crashed"))}, time.Now(), 590)
	if err := seg.WriteCursor(&store.Cursor{Pages: 1, Items: 1}); err != nil {
		t.Fatal(err)
	}
	merged := 0
	s.push(func(w http.ResponseWriter) {
		m, _ := d.ReadManifest()
		merged = m.Stores[store.TypeRecentLogs].Count
		body(logsBody{class: 590, rows: []string{logRow("e2")}, count: -1, nextCursor: "v1.second"}.String())(w)
	})
	mustPullLogs(t, d, s, RecentLogsOptions{})
	if merged != 2 {
		t.Errorf("store held %d when the request went out, want the leftover merged first", merged)
	}
	if storeCount(t, d) != 3 {
		t.Errorf("store = %d", storeCount(t, d))
	}
}

func TestRecentLogsSameClassIsBusyOtherClassIsNot(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.requested = make(chan struct{}, 4)
	s.release = make(chan struct{})
	s.fallback = nil
	s.push(body(logsBody{class: 590, rows: []string{logRow("a1")}, count: -1, nextCursor: "v1.a"}.String()),
		body(logsBody{class: 591, rows: []string{logRow("b1")}, count: -1, nextCursor: "v1.b"}.String()))

	errs := make(chan error, 2)
	go func() { _, err, _ := pullLogs(t, d, s, RecentLogsOptions{ClassID: 590}); errs <- err }()
	<-s.requested
	if _, err, _ := pullLogs(t, d, s, RecentLogsOptions{ClassID: 590}); cliCode(t, err).Code != "BUSY" {
		t.Errorf("a second pull of the same class: %v", err)
	}
	go func() { _, err, _ := pullLogs(t, d, s, RecentLogsOptions{ClassID: 591}); errs <- err }()
	<-s.requested
	close(s.release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("pull: %v", err)
		}
	}
	if storeCount(t, d) != 2 {
		t.Errorf("store = %d, want both classes' rows", storeCount(t, d))
	}
	classEntry(t, d, 590)
	classEntry(t, d, 591)
}

func TestRecentLogsStopsAfterTwoAttempts(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	s.fallback = refusal(http.StatusServiceUnavailable, "SERVER_ERROR")
	_, err, _ := pullLogs(t, d, s, RecentLogsOptions{})
	if cliCode(t, err).Code != "TRANSIENT" {
		t.Errorf("err = %v", err)
	}
	if n := len(s.requests()); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	m, _ := d.ReadManifest()
	if len(m.Downloads) != 0 || len(m.Stores) != 0 {
		t.Errorf("a failed pull recorded %+v", m)
	}
	assertNothingLeft(t, d)
}

func TestRecentLogsBatchesLargeResponses(t *testing.T) {
	d := newTestDataset(t)
	s := newRecentLogsServer(t)
	n := recentLogsBatch*2 + 1
	rows := make([]string, n)
	for i := range rows {
		rows[i] = logRow(fmt.Sprintf("e%04d", i))
	}
	s.push(body(logsBody{class: 590, rows: rows, count: -1, nextCursor: "v1.big"}.String()))
	res := mustPullLogs(t, d, s, RecentLogsOptions{})
	if res["rows"] != n || storeCount(t, d) != n || res["merge_counts"].(store.MergeCounts) != (store.MergeCounts{Fetched: n, New: n}) {
		t.Errorf("result %v, store %d, want %d rows", res, storeCount(t, d), n)
	}
}
