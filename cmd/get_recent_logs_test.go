package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
	"github.com/zalando/go-keyring"
)

func recentLogsEnv(t *testing.T) *[]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv("CC_DATA_ROOT", t.TempDir())
	keyring.MockInit()
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		fmt.Fprint(w, `{"class_id":590,"window_start":1,"nightly_through":"2026-10-20","since":2,"until":null,"query_started_at":3,"rows":[{"id":"e1"}],"count":1,"latest_timestamp":4,"next_cursor":"v1.c"}`)
	}))
	t.Cleanup(srv.Close)
	if err := (creds.Store{}).Save(config.MustPortal("learn.concord.org"), "test-token", srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runArgs(t, "dataset", "create", "learn.concord.org/ds"); code != 0 {
		t.Fatalf("dataset create exit %d: %s", code, stderr)
	}
	return &requests
}

func TestGetRecentLogsEmitsItsResultLine(t *testing.T) {
	requests := recentLogsEnv(t)
	out, stderr, code := runArgs(t, "get", "recent-logs", "590", "--dataset", "learn.concord.org/ds")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("result line %q: %v", out, err)
	}
	if res["type"] != "recent_logs" || res["class_id"] != float64(590) || res["rows"] != float64(1) || res["cursor"] != "stored" {
		t.Fatalf("result %v", res)
	}
	if len(*requests) != 1 || (*requests)[0] != "/api/v1/classes/590/recent-logs" {
		t.Fatalf("requests %v", *requests)
	}
}

func TestGetRecentLogsRefusesBadFlags(t *testing.T) {
	requests := recentLogsEnv(t)
	for _, args := range [][]string{
		{"--start", "abc"},
		{"--refresh", "--end", "1792540800000"},
		{"--start", "1792540800000", "--end", "1792540800000"},
	} {
		out, stderr, code := runArgs(t, append([]string{"get", "recent-logs", "590", "--dataset", "learn.concord.org/ds"}, args...)...)
		if code != 2 || !strings.Contains(out, `"error":"USAGE"`) {
			t.Errorf("%v: exit %d, stdout %q, stderr %s", args, code, out, stderr)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("a refused pull reached the server: %v", *requests)
	}
}
