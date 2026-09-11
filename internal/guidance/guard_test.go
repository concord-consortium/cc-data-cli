package guidance_test

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
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

// reportTypesIn reads the vocabulary a core sentence states. The sentences open with
// prose rather than a backticked name, so ParseCatalog cannot read them, and the core
// is hard-wrapped, so the list can span lines.
func reportTypesIn(t *testing.T, body, after string) []string {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(after) + "\\s+`report_type`\\s*\\(((?:`[a-z]+`(?:,\\s*)?)+)\\)")
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no report_type vocabulary found after %q", after)
	}
	var out []string
	for _, n := range regexp.MustCompile("`([a-z]+)`").FindAllStringSubmatch(m[1], -1) {
		out = append(out, n[1])
	}
	sort.Strings(out)
	return out
}

// These record a value the code accepts that the guidance deliberately does not name,
// with the reason. Both are empty: the guidance states both vocabularies in full. They
// exist so a value added later has to make a decision rather than be forgotten.
var (
	runReportTypeExemptions      = map[string]string{}
	downloadReportTypeExemptions = map[string]string{}
)

func TestGuidanceStatesTheRunReportTypes(t *testing.T) {
	assertVocabulary(t, reportTypesIn(t, guidance.Core(), "Report runs have a"),
		dataset.RunReportTypes(), runReportTypeExemptions, "run")
}

func assertVocabulary(t *testing.T, documented, inCode []string, exempt map[string]string, what string) {
	t.Helper()
	if len(documented) == 0 {
		t.Fatalf("%s vocabulary parsed as empty, so this checks nothing", what)
	}
	if m := guidance.Missing(documented, inCode); len(m) > 0 {
		t.Errorf("%s vocabulary names %v, which the code does not accept", what, m)
	}
	var undocumented []string
	for _, v := range guidance.Missing(inCode, documented) {
		if _, ok := exempt[v]; !ok {
			undocumented = append(undocumented, v)
		}
	}
	if len(undocumented) > 0 {
		t.Errorf("code accepts %v, which the %s vocabulary neither names nor exempts", undocumented, what)
	}
}

func TestGuidanceStatesTheDownloadReportTypes(t *testing.T) {
	assertVocabulary(t, reportTypesIn(t, guidance.Core(), "A download's"),
		dataset.AllowedReportTypes(), downloadReportTypeExemptions, "download")
}

// TestGuidanceDocumentsOnlyRealSlugs runs one direction only. The reverse is
// deliberately not checked, because the portal offers aggregate reports that have
// no Go constant today. What this cannot prove is recorded beside slugToType.
func TestGuidanceDocumentsOnlyRealSlugs(t *testing.T) {
	documented, err := guidance.ParseCatalog(guidance.Core(), "Report slugs")
	if err != nil {
		t.Fatal(err)
	}
	inCode := append(dataset.ReportSlugs(), duck.DimensionSlugs()...)
	if m := guidance.Missing(documented, inCode); len(m) > 0 {
		t.Fatalf("guidance names slugs the code does not know: %v", m)
	}
}
