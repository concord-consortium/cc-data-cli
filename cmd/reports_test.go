package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

// TestResolvePortal covers what reports list and reports jobs add over
// auth.ResolvePortalTarget: the default_portal fallback layered on top of it.
// The accepted portal spellings themselves are config.TestPortalMatrix's to pin,
// so only one alias case appears here, as a check that expansion is reached at
// all. There is no --server on these commands; the server comes from the stored
// credential.
func TestResolvePortal(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.Config
		flagVal string
		want    string
	}{
		{"the flag is alias-expanded", &config.Config{}, "staging", config.StagingPortal},
		{"flag wins over default_portal", &config.Config{DefaultPortal: config.ProductionPortal}, "staging", config.StagingPortal},
		{"empty flag falls back to default_portal", &config.Config{DefaultPortal: config.ProductionPortal}, "", config.ProductionPortal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolvePortal(c.cfg, c.flagVal)
			if err != nil {
				t.Fatal(err)
			}
			if got.Host() != c.want {
				t.Fatalf("resolvePortal(%q) = %q, want %q", c.flagVal, got.Host(), c.want)
			}
		})
	}
}

func TestResolvePortalRequiresAPortal(t *testing.T) {
	if _, err := resolvePortal(&config.Config{}, ""); err == nil {
		t.Fatal("no flag and no default_portal should be a usage error")
	}
}

func TestFilterOptionsRequiresADimension(t *testing.T) {
	stdout, stderr, code := runArgs(t, "reports", "filter-options", "--portal", "prod")

	if code == output.ExitSuccess {
		t.Fatal("a missing --dimension should not succeed")
	}
	if !strings.Contains(stdout+stderr, "--dimension is required") {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}
}

// Reached only through RunE, every flag but --dimension was unverifiable: breaking --search,
// --limit, --report-slug or --json each left the suite green.
func TestFilterOptionsFlagsBuildTheRequest(t *testing.T) {
	req, err := filterOptionsFlags{dimension: "student", slug: "student-answers", search: "ada", limit: 25}.request()
	if err != nil {
		t.Fatal(err)
	}
	if req.Dimension != "student" {
		t.Errorf("--dimension did not reach the request: %q", req.Dimension)
	}
	if req.ReportSlug != "student-answers" {
		t.Errorf("--report-slug did not reach the request: %q", req.ReportSlug)
	}
	if req.Search != "ada" {
		t.Errorf("--search did not reach the request: %q", req.Search)
	}
	if req.Limit != 25 {
		t.Errorf("--limit did not reach the request: %d", req.Limit)
	}
}

func TestFilterOptionsFlagsAllWalksEveryPage(t *testing.T) {
	var tokens []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		tokens = append(tokens, body["page_token"])
		if body["page_token"] == nil {
			fmt.Fprint(w, `{"items":[{"id":"1","label":"Ada"}],"next_page_token":"p2","count":null,"count_skipped":false,"count_skipped_reason":null}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"2","label":"Bea"}],"next_page_token":null,"count":null,"count_skipped":false,"count_skipped_reason":null}`)
	}))
	defer srv.Close()

	page, err := filterOptionsFlags{dimension: "class", all: true}.fetch(context.Background(), api.New(srv.URL, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("--all requested %d pages, want 2", len(tokens))
	}
	if len(page.Items) != 2 {
		t.Fatalf("options = %+v", page.Items)
	}
}

func TestFilterOptionsFlagsRequireADimension(t *testing.T) {
	if _, err := (filterOptionsFlags{search: "ada"}).request(); err == nil {
		t.Fatal("a missing --dimension must be an error")
	}
}

func TestFilterOptionsFlagsRenderJSONThroughReportview(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	if err := (filterOptionsFlags{asJSON: true}).render(api.FilterOptionsPage{
		Items: []api.FilterOption{{ID: "1", Label: "Ada"}},
	}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// The payload renames items to options, so emitting the raw page would show "items".
	if !strings.Contains(got, `"options"`) || strings.Contains(got, `"items"`) {
		t.Fatalf("--json did not go through reportview.FilterOptions: %s", got)
	}
}

func TestRenderFilterOptionsTableSaysWhenNoTotalIsAvailable(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	// Outside the server's three documented states, so this is defensive: without it a refused
	// count with no reason prints nothing and reads as a count nobody asked for.
	renderFilterOptionsTable(api.FilterOptionsPage{
		Items:        []api.FilterOption{{ID: "1", Label: "Ada"}},
		CountSkipped: true,
	})
	if !strings.Contains(out.String(), "no total available") {
		t.Fatalf("a refused count with no reason explained nothing:\n%s", out.String())
	}
}

// After the cap, --all can return a next token, and the old message told the user to pass the
// flag they had just passed, with no way to continue.
func TestRenderFilterOptionsTableSaysHowToResumeAfterTheCap(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	token := "next-page-token"
	renderFilterOptionsTable(api.FilterOptionsPage{
		Items:         []api.FilterOption{{ID: "1", Label: "Ada"}},
		NextPageToken: &token,
		Truncated:     true,
	})
	got := out.String()
	if strings.Contains(got, "pass --all") {
		t.Errorf("a walk that already ran must not tell the user to pass --all:\n%s", got)
	}
	for _, want := range []string{"--page-token", token} {
		if !strings.Contains(got, want) {
			t.Errorf("no way to continue past the cap, missing %q:\n%s", want, got)
		}
	}
}

func TestFilterOptionsFlagsSendThePageToken(t *testing.T) {
	req, err := filterOptionsFlags{dimension: "class", pageToken: "abc"}.request()
	if err != nil {
		t.Fatal(err)
	}
	if req.PageToken != "abc" {
		t.Fatalf("--page-token did not reach the request: %q", req.PageToken)
	}
}

func TestRenderFilterOptionsTable(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	total := 9
	renderFilterOptionsTable(api.FilterOptionsPage{
		Items: []api.FilterOption{{ID: "3", Label: ""}, {ID: "2", Label: "Adams (a)"}},
		Count: &total,
	})

	got := out.String()
	for _, want := range []string{"ID", "LABEL", "Adams (a)", "2 shown of 9 total"} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
	// A coalesced empty label still gets its own row, so the id stays selectable.
	if !strings.Contains(got, "3") {
		t.Fatalf("an empty label dropped its row:\n%s", got)
	}
}

func TestRenderFilterOptionsTableWithoutACount(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	renderFilterOptionsTable(api.FilterOptionsPage{Items: []api.FilterOption{{ID: "2", Label: "Adams (a)"}}})

	if strings.Contains(out.String(), "total") {
		t.Fatalf("a missing count must not be rendered as a total:\n%s", out.String())
	}
}

func TestRenderFilterOptionsTableSaysWhenMoreRemain(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	token := "eyJ0b2tlbiI6MX0"
	renderFilterOptionsTable(api.FilterOptionsPage{
		Items:         []api.FilterOption{{ID: "2", Label: "Adams (a)"}},
		NextPageToken: &token,
	})

	// Without this the table looks complete when it is one page of many.
	if !strings.Contains(out.String(), "--all") {
		t.Fatalf("a truncated page must say how to see the rest:\n%s", out.String())
	}
}

func TestRenderFilterOptionsTableExplainsARefusedCount(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	reason := "counting every student without a narrowing selection is unbounded"
	renderFilterOptionsTable(api.FilterOptionsPage{
		Items:              []api.FilterOption{{ID: "71", Label: "Stu One <101>"}},
		CountSkipped:       true,
		CountSkippedReason: &reason,
	})

	got := out.String()
	if !strings.Contains(got, "no total") || !strings.Contains(got, "unbounded") {
		t.Fatalf("a refused count must say why rather than showing nothing:\n%s", got)
	}
}

func TestReportFilterFlagsInlineAndFileAgree(t *testing.T) {
	const filter = `{"cohort":[1,2]}`
	path := filepath.Join(t.TempDir(), "filter.json")
	if err := os.WriteFile(path, []byte(filter+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inline, err := reportFilterFlags{inline: filter}.raw()
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := reportFilterFlags{file: path}.raw()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical(t, inline), canonical(t, fromFile)) {
		t.Fatalf("--report-filter %s and --report-filter-file %s produced different bodies", inline, fromFile)
	}
}

func canonical(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReportFilterFlagsRefusesBothSources(t *testing.T) {
	if _, err := (reportFilterFlags{inline: "{}", file: "f.json"}).raw(); err == nil {
		t.Fatal("passing both --report-filter and --report-filter-file must be a usage error")
	}
}

func TestReportFilterFlagsRejectMalformedJSONLocally(t *testing.T) {
	if _, err := (reportFilterFlags{inline: `{"cohort":[1`}).raw(); err == nil {
		t.Fatal("malformed JSON must be a usage error rather than a server round trip")
	}
	if _, err := (reportFilterFlags{inline: `[1,2]`}).raw(); err == nil {
		t.Fatal("a filter that is not an object must be a usage error")
	}
}

func TestReportFilterFlagsAreAbsentWhenUnset(t *testing.T) {
	raw, err := reportFilterFlags{}.raw()
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Fatalf("raw = %s, want nil so the body omits the key", raw)
	}
}

// The same expression backs both commands, so filter-options narrows by exactly what create sends.
func TestFilterOptionsFlagsCarryTheReportFilter(t *testing.T) {
	req, err := filterOptionsFlags{dimension: "school", filter: reportFilterFlags{inline: `{"cohort":[1]}`}}.request()
	if err != nil {
		t.Fatal(err)
	}
	if string(req.ReportFilter) != `{"cohort":[1]}` {
		t.Fatalf("--report-filter did not reach the request: %s", req.ReportFilter)
	}
}

func TestFilterOptionsFlagsRejectAMalformedFilter(t *testing.T) {
	if _, err := (filterOptionsFlags{dimension: "school", filter: reportFilterFlags{inline: "{"}}).request(); err == nil {
		t.Fatal("a malformed --report-filter must be a usage error")
	}
}

func TestReportsCreateRequiresASlug(t *testing.T) {
	stdout, stderr, code := runArgs(t, "reports", "create", "--portal", "prod")

	if code == output.ExitSuccess {
		t.Fatal("a missing --report-slug should not succeed")
	}
	if !strings.Contains(stdout+stderr, "--report-slug is required") {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}
}

func TestReportsCreateRejectsAMalformedFilterBeforeAnyRequest(t *testing.T) {
	stdout, stderr, code := runArgs(t, "reports", "create", "--portal", "prod", "--report-slug", "student-answers", "--report-filter", "{")

	if code != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", code, output.ExitUsage)
	}
	if !strings.Contains(stdout+stderr, "--report-filter must be a JSON object") {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}
}

func TestReportsDuplicateRequiresAnIntegerRunID(t *testing.T) {
	stdout, stderr, code := runArgs(t, "reports", "duplicate", "abc", "--portal", "prod")

	if code != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", code, output.ExitUsage)
	}
	if !strings.Contains(stdout+stderr, "run-id must be an integer") {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}
}

// The guard's message and the run it names are what tell a caller to re-read rather than retry.
func TestReportWriteErrorForwardsACodedError(t *testing.T) {
	err := reportWriteError(&api.APIError{
		Status:  409,
		Code:    api.CodePortalDuplicateUnnecessary,
		Message: "Run 90073 is a Portal report, computed live on every request. Re-read run 90073 for current data, or pass force: true to duplicate anyway.",
		Extra:   map[string]any{"run_id": float64(90073)},
	})

	var cliErr *output.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %T", err)
	}
	if cliErr.Code != api.CodePortalDuplicateUnnecessary || !strings.Contains(cliErr.Message, "Re-read run 90073") {
		t.Fatalf("cli error = %+v", cliErr)
	}
	if cliErr.Action != "" {
		t.Fatalf("a coded refusal must not claim the run may have been created: %q", cliErr.Action)
	}
	if fmt.Sprint(cliErr.Envelope()["run_id"]) != "90073" {
		t.Fatalf("envelope = %v", cliErr.Envelope())
	}
}

// A POST that fails in transport is never retried, so the run may exist and the user has to look.
// A retry budget that ran out has the same problem, even though it wraps the last coded error it
// saw, which is why the advice keys off the exit class rather than the Go error type.
func TestReportWriteErrorPointsAnUnansweredWriteAtReportsList(t *testing.T) {
	unanswered := []error{
		errors.New("dial tcp: connection reset"),
		&api.TransientError{Attempts: 3, Last: &api.APIError{Status: 503, Code: api.CodeServerError}},
	}

	for _, err := range unanswered {
		var cliErr *output.CLIError
		if !errors.As(reportWriteError(err), &cliErr) {
			t.Fatalf("err = %T", err)
		}
		if !strings.Contains(cliErr.Action, "cc-data reports list") {
			t.Fatalf("%v: action = %q", err, cliErr.Action)
		}
	}
}

func TestReportWriteErrorLeavesAnAuthFailureAlone(t *testing.T) {
	err := reportWriteError(&api.APIError{Status: 401, Code: api.CodeNotAuthed, Message: "You must supply a valid API token."})

	var cliErr *output.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %T", err)
	}
	if !strings.Contains(cliErr.Action, "cc-data login") {
		t.Fatalf("action = %q, want the login action a rejected token already has", cliErr.Action)
	}
}

func TestRenderRunEmitsTheRunPayload(t *testing.T) {
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	defer restore()

	if err := renderRun(api.ReportRun{ID: 90070, ReportSlug: "student-answers"}, true); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"run"`) || !strings.Contains(got, `"run_id":90070`) {
		t.Fatalf("--json did not go through reportview: %s", got)
	}
}
