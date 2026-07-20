package api

import (
	"encoding/json"
	"time"
)

// ReportRun is a run's metadata as served by GET /reports and /reports/:id.
type ReportRun struct {
	ID                 int             `json:"id"`
	ReportSlug         string          `json:"report_slug"`
	ReportType         *string         `json:"report_type"`
	ReportFilter       json.RawMessage `json:"report_filter"`
	ReportFilterValues map[string]any  `json:"report_filter_values"`
	AthenaQueryState   *string         `json:"athena_query_state"`
	InsertedAt         time.Time       `json:"inserted_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// Page is a keyset-paginated envelope of typed items.
type Page[T any] struct {
	Items         []T     `json:"items"`
	NextPageToken *string `json:"next_page_token"`
}

// BulkPage is the answers/history paged envelope, whose items stay raw.
type BulkPage struct {
	Items          []json.RawMessage `json:"items"`
	NextPageToken  *string           `json:"next_page_token"`
	TotalEndpoints *int              `json:"total_endpoints"`
}

// DownloadEnvelope is the presigned-URL envelope for a CSV download.
type DownloadEnvelope struct {
	DownloadURL      string `json:"download_url"`
	Filename         string `json:"filename"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

// Job is one post-processing job for a run.
type Job struct {
	ID        int             `json:"id"`
	Steps     json.RawMessage `json:"steps"`
	Status    string          `json:"status"`
	HasResult bool            `json:"has_result"`
}

// AttachmentResult is one per-item presign outcome.
type AttachmentResult struct {
	DocID string `json:"doc_id"`
	Name  string `json:"name"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// AttachmentResults is the presign response.
type AttachmentResults struct {
	Results          []AttachmentResult `json:"results"`
	ExpiresInSeconds int                `json:"expires_in_seconds"`
}

// TokenInfo is the token-introspection response.
type TokenInfo struct {
	Label        *string    `json:"label"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	ReportAccess bool       `json:"report_access"`
}
