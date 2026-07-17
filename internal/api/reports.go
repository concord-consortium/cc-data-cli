package api

import (
	"context"
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
