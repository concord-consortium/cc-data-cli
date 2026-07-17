package mcpserver

import (
	"context"

	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mapOut = map[string]any

type versionOut struct {
	Version string `json:"version"`
}

type authStatusIn struct {
	Check bool `json:"check,omitempty"`
}

type portalIn struct {
	Portal string `json:"portal"`
}

type reportsJobsIn struct {
	Portal string `json:"portal"`
	RunID  int    `json:"run_id"`
}

type getReportIn struct {
	Dataset string `json:"dataset"`
	RunID   int    `json:"run_id"`
	Job     int    `json:"job,omitempty"`
	NoWait  bool   `json:"no_wait,omitempty"`
	Refresh bool   `json:"refresh,omitempty"`
}

type getPagedIn struct {
	Dataset string `json:"dataset"`
	RunID   int    `json:"run_id"`
	Refresh bool   `json:"refresh,omitempty"`
}

type getAttachmentsIn struct {
	Dataset  string `json:"dataset"`
	RunID    int    `json:"run_id"`
	Refresh  bool   `json:"refresh,omitempty"`
	Answer   string `json:"answer,omitempty"`
	History  string `json:"history,omitempty"`
	Question string `json:"question,omitempty"`
	Name     string `json:"name,omitempty"`
}

type datasetCreateIn struct {
	Ref         string `json:"ref"`
	Description string `json:"description,omitempty"`
}

type datasetShowIn struct {
	Ref  string `json:"ref"`
	Full bool   `json:"full,omitempty"`
}

type datasetRenameIn struct {
	Ref     string `json:"ref"`
	NewName string `json:"new_name"`
}

type datasetEditIn struct {
	Ref         string `json:"ref"`
	Description string `json:"description"`
}

type datasetRefIn struct {
	Ref string `json:"ref"`
}

type confirmRefIn struct {
	Ref     string `json:"ref"`
	Confirm bool   `json:"confirm,omitempty"`
}

type queryIn struct {
	Datasets []string `json:"datasets"`
	SQL      string   `json:"sql"`
	MaxRows  int      `json:"max_rows,omitempty"`
}

type queryOut struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"`
	RowCount  int              `json:"row_count"`
}

func runQuery(ctx context.Context, e *duck.Engine, sql string, maxRows int) (*mcp.CallToolResult, queryOut, error) {
	rows, err := e.Query(ctx, sql)
	if err != nil {
		return nil, queryOut{}, err
	}
	defer rows.Close()
	cols, out, truncated, total, err := duck.CollectRows(rows, maxRows)
	if err != nil {
		return nil, queryOut{}, err
	}
	if out == nil {
		out = []map[string]any{}
	}
	return nil, queryOut{Columns: cols, Rows: out, Truncated: truncated, RowCount: total}, nil
}
