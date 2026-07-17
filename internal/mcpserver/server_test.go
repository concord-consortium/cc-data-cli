package mcpserver

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
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
	want := []string{"auth_status", "version", "reports_list", "reports_jobs", "get_report",
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
	if names["query"].Annotations == nil || !names["query"].Annotations.ReadOnlyHint {
		t.Fatal("query should be read-only")
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
	callJSON(t, cs, "dataset_create", map[string]any{"ref": "learn.concord.org/ds"})

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

func TestMCPDatasetShowParity(t *testing.T) {
	root := setupEnv(t)
	cs := connect(t)
	callJSON(t, cs, "dataset_create", map[string]any{"ref": "learn.concord.org/ds", "description": "hi"})

	_, out := callJSON(t, cs, "dataset_show", map[string]any{"ref": "learn.concord.org/ds"})

	// The tool payload must equal the CLI's BuildShowJSON for the same dataset.
	d := dataset.Open(root, dataset.Ref{Portal: "learn.concord.org", Name: "ds"})
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
	if _, err := dataset.Create(root, dataset.Ref{Portal: "learn.concord.org", Name: "ds"}, ""); err != nil {
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
