package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// ExchangeCLIToken exchanges a PKCE grant code for an API token. The endpoint is
// public (no bearer); secrets travel in the body only.
func (c *Client) ExchangeCLIToken(ctx context.Context, code, verifier, label string) (string, error) {
	body := map[string]string{"code": code, "code_verifier": verifier}
	if label != "" {
		body["label"] = label
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.postJSON(ctx, "/auth/cli/token", body, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// RevokeCurrentToken revokes the calling bearer token.
func (c *Client) RevokeCurrentToken(ctx context.Context) error {
	var out struct {
		Revoked bool `json:"revoked"`
	}
	return c.deleteJSON(ctx, "/api/v1/tokens/current", &out)
}

// CurrentToken introspects the calling bearer token.
func (c *Client) CurrentToken(ctx context.Context) (*TokenInfo, error) {
	var info TokenInfo
	if err := c.getJSON(ctx, "/api/v1/tokens/current", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ProbeReports validates a token against an older server without the
// introspection endpoint by requesting a single report.
func (c *Client) ProbeReports(ctx context.Context) error {
	return c.getJSON(ctx, "/api/v1/reports", pageQuery(1), nil)
}

// GetReport fetches a run's metadata.
func (c *Client) GetReport(ctx context.Context, runID int) (*ReportRun, error) {
	var run ReportRun
	if err := c.getJSON(ctx, fmt.Sprintf("/api/v1/reports/%d", runID), nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// AttachmentRefReq is one presign request item.
type AttachmentRefReq struct {
	Collection string `json:"collection"`
	Source     string `json:"source"`
	DocID      string `json:"doc_id"`
	Name       string `json:"name"`
}

// PresignAttachments batch-presigns attachment refs (server cap 500 items).
func (c *Client) PresignAttachments(ctx context.Context, runID int, refs []AttachmentRefReq, disposition string) (*AttachmentResults, error) {
	body := map[string]any{"attachments": refs}
	if disposition != "" {
		body["disposition"] = disposition
	}
	var out AttachmentResults
	if err := c.postJSON(ctx, fmt.Sprintf("/api/v1/reports/%d/attachments", runID), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReportDownloadEnvelope requests the presigned download envelope for a run's CSV
// (or a job's CSV when jobID is non-nil). A not-ready run/job returns a coded
// *APIError (NOT_READY) carrying its state.
func (c *Client) ReportDownloadEnvelope(ctx context.Context, runID int, jobID *int) (*DownloadEnvelope, error) {
	path := fmt.Sprintf("/api/v1/reports/%d/download", runID)
	if jobID != nil {
		path = fmt.Sprintf("/api/v1/reports/%d/jobs/%d/download", runID, *jobID)
	}
	var env DownloadEnvelope
	if err := c.getJSON(ctx, path, nil, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// FilterOptionsReq is one page request for a report filter dimension. ReportFilter is passed
// through as the server emitted it on a run, so the client never has to decode a filter.
type FilterOptionsReq struct {
	Dimension    string
	ReportSlug   string
	Search       string
	Limit        int
	PageToken    string
	IncludeCount *bool
	ReportFilter json.RawMessage
}

func (r FilterOptionsReq) body() map[string]any {
	body := map[string]any{"dimension": r.Dimension}
	if r.ReportSlug != "" {
		body["report_slug"] = r.ReportSlug
	}
	if r.Search != "" {
		body["search"] = r.Search
	}
	if r.Limit > 0 {
		body["limit"] = r.Limit
	}
	if r.PageToken != "" {
		body["page_token"] = r.PageToken
	}
	if r.IncludeCount != nil {
		body["include_count"] = *r.IncludeCount
	}
	if len(r.ReportFilter) > 0 {
		body["report_filter"] = r.ReportFilter
	}
	return body
}

// FilterOptions fetches one page of a filter dimension's options.
func (c *Client) FilterOptions(ctx context.Context, req FilterOptionsReq) (FilterOptionsPage, error) {
	var page FilterOptionsPage
	if err := c.postJSON(ctx, "/api/v1/reports/filter-options", req.body(), &page); err != nil {
		return FilterOptionsPage{}, err
	}
	return page, nil
}

// FilterOptionsFor returns one page of a dimension's options, or every page when all is set. It is
// the shape both the CLI and the MCP tool need, so neither has to make the choice itself. The whole
// envelope comes back either way: a caller that pages needs the next token, and a caller that asked
// for a count needs to tell a refused one from a count it never requested.
func (c *Client) FilterOptionsFor(ctx context.Context, req FilterOptionsReq, all bool) (FilterOptionsPage, error) {
	if all {
		return c.DrainFilterOptions(ctx, req)
	}
	return c.FilterOptions(ctx, req)
}

// DrainFilterOptions walks every page of a dimension and returns one envelope holding every option,
// the first page's count fields, and no next token, since the walk consumed them all. Only the first
// page asks for a count: it costs what a page costs and would not change.
func (c *Client) DrainFilterOptions(ctx context.Context, req FilterOptionsReq) (FilterOptionsPage, error) {
	var drained FilterOptionsPage
	seen := map[string]bool{}

	for first := true; ; first = false {
		page, err := c.FilterOptions(ctx, req)
		if err != nil {
			return FilterOptionsPage{}, err
		}
		drained.Items = append(drained.Items, page.Items...)
		if first {
			drained.Count = page.Count
			drained.CountSkipped = page.CountSkipped
			drained.CountSkippedReason = page.CountSkippedReason
		}
		if page.NextPageToken == nil || *page.NextPageToken == "" {
			return drained, nil
		}
		next := *page.NextPageToken
		// A trusted server never repeats a token within a walk; a repeat would loop forever.
		if seen[next] {
			return FilterOptionsPage{}, fmt.Errorf("pagination stopped: server repeated page token")
		}
		seen[next] = true
		declined := false
		req.PageToken = next
		req.IncludeCount = &declined
	}
}
