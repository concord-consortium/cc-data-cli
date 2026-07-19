package claude

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// MCPServerName is the name the cc-data stdio MCP server is registered under.
const MCPServerName = "cc-data"

// MCPRegisterResult describes what RegisterMCP did, for user-facing messaging.
type MCPRegisterResult struct {
	Registered     bool   // newly added this run
	AlreadyPresent bool   // was already registered
	SkippedReason  string // non-empty when registration was skipped (e.g. no claude CLI)
	Binary         string // the cc-data binary path used for the registration
}

// RegisterMCP registers the cc-data stdio MCP server with Claude Code at USER
// scope (global for the user, across all projects) via the `claude` CLI,
// pointing at the currently running binary. It is idempotent (an existing
// registration is reported, not re-added) and a graceful no-op when the claude
// CLI is not installed, so a Claude Desktop-only user is never blocked.
func RegisterMCP() (MCPRegisterResult, error) {
	exe, err := os.Executable()
	if err != nil {
		return MCPRegisterResult{}, err
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return MCPRegisterResult{SkippedReason: "the claude CLI was not found on PATH", Binary: exe}, nil
	}
	// `claude mcp add` only writes config (no server spawn/health check), so this
	// is fast and safe to run during init. A duplicate prints "already exists";
	// treat that as success regardless of exit code so re-running init is a no-op.
	out, err := exec.Command(claudeBin, "mcp", "add", "-s", "user", MCPServerName, "--", exe, "mcp").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if strings.Contains(text, "already exists") {
		return MCPRegisterResult{AlreadyPresent: true, Binary: exe}, nil
	}
	if err != nil {
		return MCPRegisterResult{}, fmt.Errorf("claude mcp add failed: %v: %s", err, text)
	}
	return MCPRegisterResult{Registered: true, Binary: exe}, nil
}

// ManualMCPCommand is the command a user can run to register the MCP server
// themselves (shown when automatic registration is skipped or fails).
func ManualMCPCommand(binary string) string {
	if binary == "" {
		binary = "cc-data"
	}
	return fmt.Sprintf("claude mcp add -s user %s -- %s mcp", MCPServerName, binary)
}
