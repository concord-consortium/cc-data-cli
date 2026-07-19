package claude

import "testing"

func TestManualMCPCommand(t *testing.T) {
	if got, want := ManualMCPCommand("/opt/bin/cc-data"), "claude mcp add -s user cc-data -- /opt/bin/cc-data mcp"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Empty binary falls back to the bare command name.
	if got, want := ManualMCPCommand(""), "claude mcp add -s user cc-data -- cc-data mcp"; got != want {
		t.Fatalf("empty-binary fallback: got %q, want %q", got, want)
	}
}
