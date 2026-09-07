// Package guidance owns the single source of the cc-data data-model prose and renders it
// for each surface that carries it: the Claude Code skill file and the MCP instructions.
package guidance

import _ "embed"

//go:embed src/core.md
var core string

//go:embed src/tools.md
var tools string

//go:embed src/skill_header.md
var skillHeader string

//go:embed src/mcp_header.md
var mcpHeader string

// Core is the shared data-model prose both surfaces render, and the only prose the drift
// guard reads.
func Core() string { return core }

// Tools is the tool catalog the MCP surface renders.
func Tools() string { return tools }

// Skill is the body cc-data init writes to ~/.claude/skills/cc-data/SKILL.md, before stamping.
func Skill() string { return skillHeader + "\n" + core }

// Instructions is the MCP server's instructions block. It carries no frontmatter and never
// directs the model to run a shell command or read --help, neither of which an MCP-only client
// can do; relaying a terminal instruction to the user (the auth remedy) is a different act.
func Instructions() string { return mcpHeader + "\n" + core + "\n" + tools }
