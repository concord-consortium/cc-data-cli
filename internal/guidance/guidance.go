// Package guidance owns the single source of the cc-data data-model prose and renders it
// for each surface that carries it: the Claude Code skill file, the MCP instructions, and
// the MCP guide tool. The instructions are an index short enough for a client to keep whole;
// the guide tool returns the full text.
package guidance

import _ "embed"

//go:embed src/datasets.md
var datasets string

//go:embed src/core.md
var core string

//go:embed src/sensitive.md
var sensitive string

//go:embed src/tools.md
var tools string

//go:embed src/skill_header.md
var skillHeader string

//go:embed src/mcp_header.md
var mcpHeader string

//go:embed src/mcp_guide.md
var mcpGuide string

// Core is the shared data-model prose the skill and the guide tool render, and the only
// prose the drift guard reads.
func Core() string { return datasets + "\n" + core + "\n" + sensitive }

// Tools is the tool catalog the guide tool renders after the core.
func Tools() string { return tools }

// Skill is the body cc-data init writes to ~/.claude/skills/cc-data/SKILL.md, before stamping.
func Skill() string { return skillHeader + "\n" + Core() }

// Guide is the full guidance the MCP guide tool returns: the core and the tool catalog.
func Guide() string { return Core() + "\n" + tools }

// Instructions is the MCP server's instructions block: an index of the guidance, short enough
// that a client keeping only the first 4,096 characters of a server's instructions (Claude Code
// does) holds all of it. Most important first: the auth remedy, the pointer to the guide tool
// and the sensitive-data norm sit within the first 2,048, a cap earlier Claude Code builds used,
// and the dataset rules, which the guide repeats, come last. It carries no frontmatter
// and never directs the model to run a shell command or read --help, neither of which an
// MCP-only client can do; relaying a terminal instruction to the user (the auth remedy) is a
// different act.
func Instructions() string {
	return mcpHeader + "\n" + mcpGuide + "\n" + sensitive + "\n" + datasets
}
