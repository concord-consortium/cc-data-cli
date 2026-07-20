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

// MCPUnregisterResult describes what UnregisterMCP did.
type MCPUnregisterResult struct {
	Removed       bool   // the registration was removed this run
	NotPresent    bool   // nothing was registered
	SkippedReason string // non-empty when unregistration was skipped (no claude CLI)
}

// UnregisterMCP removes the cc-data MCP server registration that RegisterMCP added
// (user scope). It is idempotent (an already-absent server is reported, not an
// error) and a graceful no-op when the claude CLI is not installed.
func UnregisterMCP() (MCPUnregisterResult, error) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return MCPUnregisterResult{SkippedReason: "the claude CLI was not found on PATH"}, nil
	}
	out, err := exec.Command(claudeBin, "mcp", "remove", "-s", "user", MCPServerName).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		// "No MCP server named ..." means it was never registered: idempotent success.
		if strings.Contains(strings.ToLower(text), "no mcp server named") {
			return MCPUnregisterResult{NotPresent: true}, nil
		}
		return MCPUnregisterResult{}, fmt.Errorf("claude mcp remove failed: %v: %s", err, text)
	}
	return MCPUnregisterResult{Removed: true}, nil
}

// ManualMCPCommand is the command a user can run to register the MCP server
// themselves (shown when automatic registration is skipped or fails).
func ManualMCPCommand(binary string) string {
	if binary == "" {
		binary = "cc-data"
	}
	return fmt.Sprintf("claude mcp add -s user %s -- %s mcp", MCPServerName, binary)
}
