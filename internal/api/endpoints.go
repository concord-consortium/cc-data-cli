package api

import (
	"context"
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
