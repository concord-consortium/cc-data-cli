package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListReports drains every page of the user's report runs.
func (c *Client) ListReports(ctx context.Context) ([]ReportRun, error) {
	return DrainPages[ReportRun](ctx, c, "/api/v1/reports", ReportsPageDefault)
}

// ListJobs drains every page of a run's post-processing jobs.
func (c *Client) ListJobs(ctx context.Context, runID int) ([]Job, error) {
	return DrainPages[Job](ctx, c, fmt.Sprintf("/api/v1/reports/%d/jobs", runID), ReportsPageDefault)
}

// CreateReportReq is the body of POST /api/v1/reports. ReportFilter is passed through as the
// server emitted it on a run, so the client never has to decode a filter. report_filter_values is
// not sent: the server derives the labels and ignores any it is handed.
type CreateReportReq struct {
	ReportSlug   string
	ReportFilter json.RawMessage
}

func (r CreateReportReq) body() map[string]any {
	body := map[string]any{"report_slug": r.ReportSlug}
	if len(r.ReportFilter) > 0 {
		body["report_filter"] = r.ReportFilter
	}
	return body
}

// CreateReport creates a report run and returns it in the shape ListReports returns.
func (c *Client) CreateReport(ctx context.Context, req CreateReportReq) (ReportRun, error) {
	var run ReportRun
	if err := c.postJSON(ctx, "/api/v1/reports", req.body(), &run); err != nil {
		return ReportRun{}, err
	}
	return run, nil
}

// DuplicateReport creates a new run from an existing one's slug and filter. Duplicating a Portal
// run answers PORTAL_DUPLICATE_UNNECESSARY unless force is set, because a Portal report is
// computed on every request and re-reading the run returns the same data.
func (c *Client) DuplicateReport(ctx context.Context, runID int, force bool) (ReportRun, error) {
	body := map[string]any{}
	if force {
		body["force"] = true
	}
	var run ReportRun
	if err := c.postJSON(ctx, fmt.Sprintf("/api/v1/reports/%d/duplicate", runID), body, &run); err != nil {
		return ReportRun{}, err
	}
	return run, nil
}
