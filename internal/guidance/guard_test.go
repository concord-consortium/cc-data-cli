package guidance_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/guidance"
	"github.com/concord-consortium/cc-data-cli/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registeredTools(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	srv := mcpserver.NewServer(mcpserver.Options{Version: "guard"})
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "guard", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func TestGuidanceDocumentsEveryStaticView(t *testing.T) {
	views := duck.StaticViewNames()
	if len(views) == 0 {
		t.Fatal("no static views enumerated; the inventory side of the guard is broken")
	}
	documented, err := guidance.ParseCatalog(guidance.Core(), "Views")
	if err != nil {
		t.Fatal(err)
	}
	if m := guidance.Missing(views, documented); len(m) > 0 {
		t.Fatalf("views registered but not documented: %v", m)
	}
	if m := guidance.Missing(documented, views); len(m) > 0 {
		t.Fatalf("views documented but no longer registered: %v", m)
	}
}

func TestGuidanceDocumentsEveryTool(t *testing.T) {
	tools := registeredTools(t)
	if len(tools) == 0 {
		t.Fatal("no tools enumerated; the inventory side of the guard is broken")
	}
	documented, err := guidance.ParseCatalog(guidance.Tools(), "Tools")
	if err != nil {
		t.Fatal(err)
	}
	if m := guidance.Missing(tools, documented); len(m) > 0 {
		t.Fatalf("tools registered but not documented: %v", m)
	}
	if m := guidance.Missing(documented, tools); len(m) > 0 {
		t.Fatalf("tools documented but no longer registered: %v", m)
	}
}

func TestGuidanceDocumentsEveryIdentityColumn(t *testing.T) {
	cols := duck.IdentityColumnNames()
	if len(cols) == 0 {
		t.Fatal("no identity columns enumerated; the inventory side of the guard is broken")
	}
	documented, err := guidance.ParseCatalog(guidance.Core(), "Identity columns")
	if err != nil {
		t.Fatal(err)
	}
	if m := guidance.Missing(cols, documented); len(m) > 0 {
		t.Fatalf("identity columns registered but not documented: %v", m)
	}
	if m := guidance.Missing(documented, cols); len(m) > 0 {
		t.Fatalf("identity columns documented but no longer registered: %v", m)
	}
}

// The auth remedy is the one piece of guidance that exists on both surfaces, worded
// differently on each, so no inventory comparison can notice a surface losing it.
// Matched without its leading capital, so rewording around the rule does not fail.
func TestBothSurfacesCarryTheAuthRemedy(t *testing.T) {
	for surface, body := range map[string]string{"skill": guidance.Skill(), "instructions": guidance.Instructions()} {
		if !strings.Contains(body, "NOT_AUTHENTICATED") {
			t.Errorf("%s: no NOT_AUTHENTICATED remedy", surface)
		}
		if !strings.Contains(body, "drive the browser login") {
			t.Errorf("%s: the browser-login norm is missing", surface)
		}
	}
}

// The core renders into the MCP instructions, where the model has tools and no shell, so
// a command written there would be an instruction it cannot carry out. Command spellings
// belong in the per-surface wrappers.
func TestCoreNamesNoCommand(t *testing.T) {
	if strings.Contains(guidance.Core(), "`cc-data ") {
		t.Error("the core names a cc-data command; move it to skill_header.md")
	}
}

func TestResearcherGuideDocumentsEveryStaticView(t *testing.T) {
	body, err := os.ReadFile("../../docs/researcher-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	documented, err := guidance.ParseCatalog(string(body), "How datasets are organized")
	if err != nil {
		t.Fatal(err)
	}
	if m := guidance.Missing(duck.StaticViewNames(), documented); len(m) > 0 {
		t.Fatalf("views missing from the researcher guide's table: %v", m)
	}
	if m := guidance.Missing(documented, duck.StaticViewNames()); len(m) > 0 {
		t.Fatalf("researcher guide's table lists views that no longer exist: %v", m)
	}
}

// The guard must be able to fail, not merely observed passing. Both halves it drives
// are shipped code: gut either ParseCatalog or Missing and this goes red.
func TestGuardDetectsAnUndocumentedName(t *testing.T) {
	body := "## Views\n\n- `reports` — only this one\n"
	documented, err := guidance.ParseCatalog(body, "Views")
	if err != nil {
		t.Fatal(err)
	}
	m := guidance.Missing([]string{"reports", "answers"}, documented)
	if len(m) != 1 || m[0] != "answers" {
		t.Fatalf("guard did not report the missing name, got %v", m)
	}
}
