package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/guidance"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zalando/go-keyring"
)

func setupEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	root := t.TempDir()
	t.Setenv("CC_DATA_ROOT", root)
	keyring.MockInit()
	return root
}

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	server := NewServer(Options{Version: "test-1.0"})
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestMCPInstructionsArriveInInitialize(t *testing.T) {
	setupEnv(t)
	got := connect(t).InitializeResult().Instructions
	if got == "" {
		t.Fatal("no instructions in the initialize response")
	}
	for _, want := range []string{"run_membership", "NOT_AUTHENTICATED", "auth_status"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions do not carry %q", want)
		}
	}
	if strings.HasPrefix(got, "---") {
		t.Error("instructions open with frontmatter, which is meaningless to an MCP client")
	}
	if strings.Contains(got, "--help") {
		t.Error("instructions point at --help, which an MCP client cannot read")
	}
}

func callJSON(t *testing.T, cs *mcp.ClientSession, name string, args any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var out map[string]any
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			json.Unmarshal([]byte(tc.Text), &out)
		}
	}
	return res, out
}

func TestMCPVersion(t *testing.T) {
	setupEnv(t)
	cs := connect(t)
	_, out := callJSON(t, cs, "version", struct{}{})
	if out["version"] != "test-1.0" {
		t.Fatalf("version = %v", out)
	}
}

func TestMCPToolSurface(t *testing.T) {
	setupEnv(t)
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool
	}
	want := []string{"auth_status", "version", "reports_list", "reports_jobs",
		"reports_filter_options", "get_report",
		"get_answers", "get_history", "get_attachments", "dataset_create", "dataset_list",
		"dataset_show", "dataset_rename", "dataset_edit", "dataset_delete", "dataset_purge",
		"dataset_reindex", "query"}
	for _, n := range want {
		if _, ok := names[n]; !ok {
			t.Fatalf("missing tool %q", n)
		}
	}
	// Excluded terminal/installer commands are not exposed.
	for _, n := range []string{"login", "logout", "repl", "mcp", "init", "uninstall"} {
		if _, ok := names[n]; ok {
			t.Fatalf("tool %q should not be exposed", n)
		}
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("expected exactly %d tools, got %d", len(want), len(res.Tools))
	}

	// Annotations: read-only on listings/show/query/status/version.
	for _, n := range []string{"query", "reports_filter_options"} {
		if names[n].Annotations == nil || !names[n].Annotations.ReadOnlyHint {
			t.Fatalf("%s should be read-only", n)
		}
	}
	// Destructive hint on delete/purge.
	if names["dataset_delete"].Annotations == nil || names["dataset_delete"].Annotations.DestructiveHint == nil || !*names["dataset_delete"].Annotations.DestructiveHint {
		t.Fatal("dataset_delete should be destructive")
	}
}

func TestMCPArgSchemaExcludesCapabilityFlags(t *testing.T) {
	setupEnv(t)
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		schema, _ := json.Marshal(tool.InputSchema)
		var parsed struct {
			Properties map[string]any `json:"properties"`
		}
		json.Unmarshal(schema, &parsed)
		for _, banned := range []string{"url", "inline", "allow_dir", "allow-dir", "allowdir"} {
			if _, ok := parsed.Properties[banned]; ok {
				t.Fatalf("tool %q must not expose the %q argument", tool.Name, banned)
			}
		}
	}
}

func TestMCPDeletePurgeRequireConfirm(t *testing.T) {
	setupEnv(t)
	cs := connect(t)
	// Create a dataset first.
	callJSON(t, cs, "dataset_create", map[string]any{"portal": "learn.concord.org", "name": "ds"})

	res, _ := callJSON(t, cs, "dataset_delete", map[string]any{"ref": "learn.concord.org/ds"})
	if !res.IsError {
		t.Fatal("dataset_delete without confirm should be an error")
	}
	res, _ = callJSON(t, cs, "dataset_purge", map[string]any{"ref": "learn.concord.org/ds"})
	if !res.IsError {
		t.Fatal("dataset_purge without confirm should be an error")
	}
	// With confirm it succeeds.
	res, _ = callJSON(t, cs, "dataset_purge", map[string]any{"ref": "learn.concord.org/ds", "confirm": true})
	if res.IsError {
		t.Fatal("dataset_purge with confirm should succeed")
	}
}

// The guidance tells the model what to do when a tool reports NOT_AUTHENTICATED, so the
// server has to actually say it. Error() on a CLIError is the message alone, which drops
// both the code and the action, leaving that guidance with no trigger.
func TestMCPUnauthenticatedResponseCarriesTheCodeAndAction(t *testing.T) {
	setupEnv(t)
	cs := connect(t)
	res, _ := callJSON(t, cs, "reports_list", map[string]any{"portal": "learn.concord.org"})
	if !res.IsError {
		t.Fatal("expected an error with no stored credential")
	}
	text := errorText(res)
	for _, want := range []string{"NOT_AUTHENTICATED", "cc-data login"} {
		if !strings.Contains(text, want) {
			t.Errorf("response does not carry %q: %s", want, text)
		}
	}
	if !strings.Contains(guidance.Instructions(), "NOT_AUTHENTICATED") {
		t.Error("the instructions no longer name the code the server returns")
	}
}

// A run whose report_filter is a real object has to survive the library's output-schema
// validation. Held as json.RawMessage it typed as an array of bytes, so the call failed at the
// protocol level and the model got no runs at all, from the tool a run_id comes from.
func TestMCPReportsListCarriesARunFilterObject(t *testing.T) {
	setupEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":601,"report_slug":"student-answers","athena_query_state":"succeeded","report_filter":{"class":[601],"cohort":null}}],"next_page_token":null}`)
	}))
	defer srv.Close()
	if err := (creds.Store{}).Save(config.MustPortal("learn.concord.org"), "test-token", srv.URL); err != nil {
		t.Fatal(err)
	}

	res, out := callJSON(t, connect(t), "reports_list", map[string]any{"portal": "learn.concord.org"})
	if res.IsError {
		t.Fatalf("reports_list failed: %s", errorText(res))
	}
	runs, _ := out["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %v", out["runs"])
	}
	run, _ := runs[0].(map[string]any)
	filter, ok := run["report_filter"].(map[string]any)
	if !ok {
		t.Fatalf("report_filter did not arrive as an object: %v", run["report_filter"])
	}
	if _, present := filter["cohort"]; !present {
		t.Error("an explicit null inside the filter did not survive the round trip")
	}
	class, _ := filter["class"].([]any)
	if len(class) != 1 || class[0] != float64(601) {
		t.Errorf("the class selection did not survive: %v", filter["class"])
	}
}

// Stopping at NOT_AUTHENTICATED proves the argument decoded, not that it was sent. This drives
// the whole path and asserts the filter arrives in the request body.
func TestMCPFilterOptionsSendsTheFilterToTheServer(t *testing.T) {
	setupEnv(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"items":[{"id":"601","label":"Class 601"}],"next_page_token":null,"count":null,"count_skipped":false,"count_skipped_reason":null}`)
	}))
	defer srv.Close()
	if err := (creds.Store{}).Save(config.MustPortal("learn.concord.org"), "test-token", srv.URL); err != nil {
		t.Fatal(err)
	}

	res, out := callJSON(t, connect(t), "reports_filter_options", map[string]any{
		"portal":        "learn.concord.org",
		"dimension":     "student",
		"report_filter": map[string]any{"class": []any{601}, "cohort": nil},
	})
	if res.IsError {
		t.Fatalf("call failed: %s", errorText(res))
	}
	filter, ok := body["report_filter"].(map[string]any)
	if !ok {
		t.Fatalf("report_filter did not reach the request body: %v", body)
	}
	class, _ := filter["class"].([]any)
	if len(class) != 1 || class[0] != float64(601) {
		t.Errorf("the class selection did not reach the server: %v", filter["class"])
	}
	if _, present := filter["cohort"]; !present {
		t.Error("an explicit null was dropped on the way out")
	}
	if options, _ := out["options"].([]any); len(options) != 1 {
		t.Errorf("options = %v", out["options"])
	}
}

func TestMCPQueryDescriptionCarriesTheQueryRules(t *testing.T) {
	setupEnv(t)
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var desc string
	for _, tool := range res.Tools {
		if tool.Name == "query" {
			desc = tool.Description
		}
	}
	if desc == "" {
		t.Fatal("no query tool")
	}
	for _, want := range append(duck.StaticViewNames(), "TRY_CAST", "UNION ALL BY NAME") {
		if !strings.Contains(desc, want) {
			t.Errorf("query description does not mention %q", want)
		}
	}
}

func TestMCPDatasetCreateArguments(t *testing.T) {
	setupEnv(t)
	cs := connect(t)

	res, out := callJSON(t, cs, "dataset_create", map[string]any{"portal": "ngss-assessment.portal.concord.org", "name": "wf"})
	if res.IsError {
		t.Fatalf("explicit portal should create: %v", out)
	}
	if out["ref"] != "ngss-assessment.portal.concord.org/wf" {
		t.Errorf("created under the wrong portal: %v", out["ref"])
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultPortal = "learn.concord.org"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	res, out = callJSON(t, cs, "dataset_create", map[string]any{"name": "fallback"})
	if res.IsError {
		t.Fatalf("an omitted portal should fall back to the default: %v", out)
	}
	if out["ref"] != "learn.concord.org/fallback" {
		t.Errorf("fallback resolved to %v", out["ref"])
	}

	res, out = callJSON(t, cs, "dataset_create", map[string]any{"portal": "https://learn.concord.org", "name": "urlform"})
	if res.IsError {
		t.Fatalf("a URL-shaped portal is normalized to its hostname, not refused: %v", out)
	}
	if out["ref"] != "learn.concord.org/urlform" {
		t.Errorf("URL-shaped portal resolved to %v", out["ref"])
	}

	for _, tc := range []struct {
		label string
		args  map[string]any
		want  string
	}{
		{"environment alias", map[string]any{"portal": "staging", "name": "wf"}, "environment alias"},
		{"slash in name", map[string]any{"portal": "learn.concord.org", "name": "a/b"}, "must not contain a slash"},
		{"ref in name", map[string]any{"name": "learn.concord.org/wf"}, "must not contain a slash"},
		{"path in portal", map[string]any{"portal": "learn.concord.org/x", "name": "y"}, `portal "learn.concord.org/x" must be a hostname`},
		{"trailing slash in portal", map[string]any{"portal": "learn.concord.org/", "name": "c"}, "must be a hostname"},
		{"empty name", map[string]any{"portal": "learn.concord.org", "name": ""}, "name is required"},
	} {
		res, _ := callJSON(t, cs, "dataset_create", tc.args)
		if !res.IsError {
			t.Errorf("%s should be refused", tc.label)
			continue
		}
		if text, ok := res.Content[0].(*mcp.TextContent); !ok || !strings.Contains(text.Text, tc.want) {
			t.Errorf("%s: error should mention %q, got %v", tc.label, tc.want, res.Content[0])
		}
	}
}

func TestMCPDatasetShowParity(t *testing.T) {
	root := setupEnv(t)
	cs := connect(t)
	callJSON(t, cs, "dataset_create", map[string]any{"portal": "learn.concord.org", "name": "ds", "description": "hi"})

	_, out := callJSON(t, cs, "dataset_show", map[string]any{"ref": "learn.concord.org/ds"})

	// The tool payload must equal the CLI's BuildShowJSON for the same dataset.
	d := dataset.Open(root, dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "ds"})
	cliJSON, _ := d.BuildShowJSON(false)
	cliBytes, _ := json.Marshal(cliJSON)
	toolBytes, _ := json.Marshal(out)
	// Re-normalize both through generic maps for a stable compare.
	var a, b map[string]any
	json.Unmarshal(cliBytes, &a)
	json.Unmarshal(toolBytes, &b)
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatalf("MCP dataset_show payload differs from CLI --json:\nCLI:  %s\nMCP:  %s", ab, bb)
	}
}

func TestMCPQueryTruncation(t *testing.T) {
	root := setupEnv(t)
	cs := connect(t)
	if _, err := dataset.Create(root, dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "ds"}, ""); err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "query",
		Arguments: map[string]any{"datasets": []string{"learn.concord.org/ds"}, "sql": "SELECT * FROM range(10)", "max_rows": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out queryOut
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		json.Unmarshal([]byte(tc.Text), &out)
	}
	if !out.Truncated || out.RowCount != 10 || len(out.Rows) != 3 {
		t.Fatalf("truncation wrong: truncated=%v total=%d rows=%d", out.Truncated, out.RowCount, len(out.Rows))
	}
}

// The API tests drive the client directly, which bypasses the SDK's argument schema and its
// json.RawMessage decoding. These cover the path an MCP client actually takes.
func TestMCPFilterOptionsAcceptsAFilterObject(t *testing.T) {
	setupEnv(t)
	cs := connect(t)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var props map[string]any
	for _, tool := range res.Tools {
		if tool.Name != "reports_filter_options" {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		var parsed struct {
			Properties map[string]any `json:"properties"`
		}
		json.Unmarshal(schema, &parsed)
		props = parsed.Properties
	}
	if props == nil {
		t.Fatal("no reports_filter_options tool")
	}
	for _, arg := range []string{"dimension", "report_filter", "page_token", "include_count", "all"} {
		if _, ok := props[arg]; !ok {
			t.Fatalf("the tool does not expose %q", arg)
		}
	}
	// The advertised round trip needs the schema to permit the object reports_list hands back.
	// Asserting that directly, rather than ruling out one wrong type: held as json.RawMessage
	// this typed as an array of bytes, which a denylist of wrong shapes would have passed.
	filter, _ := props["report_filter"].(map[string]any)
	if !schemaPermitsObject(filter) {
		t.Fatalf("report_filter must accept an object, schema = %v", filter)
	}

	// No stored credential, so this cannot reach the network. Reaching the portal lookup is
	// proved by the error being NOT_AUTHENTICATED: an argument the library refuses never gets
	// that far, and naming the error we want beats ruling out three words we do not.
	callRes, _ := callJSON(t, cs, "reports_filter_options", map[string]any{
		"portal":        "learn.concord.org",
		"dimension":     "student",
		"report_filter": map[string]any{"class": []any{601}, "cohort": nil},
		"include_count": true,
	})
	if !callRes.IsError {
		t.Fatal("without a credential the call cannot succeed")
	}
	if text := errorText(callRes); !strings.Contains(text, "NOT_AUTHENTICATED") {
		t.Fatalf("the filter object did not reach the portal lookup: %s", text)
	}
}

// schemaPermitsObject reports whether a JSON Schema fragment allows an object, covering both
// a bare "type" and the list form the library emits for nullable fields.
func schemaPermitsObject(schema map[string]any) bool {
	switch t := schema["type"].(type) {
	case string:
		return t == "object"
	case []any:
		for _, v := range t {
			if v == "object" {
				return true
			}
		}
	}
	return false
}

func errorText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}
