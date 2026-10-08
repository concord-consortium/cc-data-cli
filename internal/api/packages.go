package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// PublishedPackage is report-server's answer to a publish.
type PublishedPackage struct {
	CatalogID      int64  `json:"catalog_id"`
	Identity       string `json:"identity"`
	Version        string `json:"version"`
	Checksum       string `json:"checksum"`
	Visibility     string `json:"visibility"`
	Official       bool   `json:"official"`
	CurrentVersion string `json:"current_version"`
}

// PublishPackage posts a package archive as the raw request body. origin is "" for the
// caller's own users/<id>, or projects/<id>; official asks the server to mark it official,
// which only a publisher may. Like every POST it is never retried.
func (c *Client) PublishPackage(ctx context.Context, archive []byte, origin string, official bool) (*PublishedPackage, error) {
	data, err := c.send(ctx, http.MethodPost, "/api/v1/packages", packageQuery(origin, official), archive, "application/zip")
	if err != nil {
		return nil, err
	}
	var out PublishedPackage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ValidatedPackage is what a publish of the archive would record. The last two are not
// refusals: each says only a publish would be refused now, with publish's own message for the
// second.
type ValidatedPackage struct {
	Identity              string  `json:"identity"`
	Version               string  `json:"version"`
	Checksum              string  `json:"checksum"`
	Visibility            string  `json:"visibility"`
	AlreadyPublished      bool    `json:"already_published"`
	PublishingUnavailable *string `json:"publishing_unavailable"`
}

// ValidatePackage asks report-server to apply every publish check to the archive and stop
// before storing it, with the same origin and official a publish would send. A refusal is the
// same coded error a publish would answer.
func (c *Client) ValidatePackage(ctx context.Context, archive []byte, origin string, official bool) (*ValidatedPackage, error) {
	data, err := c.send(ctx, http.MethodPost, "/api/v1/packages/validate", packageQuery(origin, official), archive, "application/zip")
	if err != nil {
		return nil, err
	}
	var out ValidatedPackage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppliesRequest names the patterns and the scope's assignment URLs, which report-service's
// deriver follows into their activities, so the URLs matched are the runner's.
type AppliesRequest struct {
	URLs           map[string][]string `json:"urls"`
	AssignmentURLs []string            `json:"assignment_urls,omitempty"`
}

// AppliesAnswer is report-server's verdict, with what the deriver read and could not.
type AppliesAnswer struct {
	Applies bool   `json:"applies"`
	Reason  string `json:"reason"`
	Unread  []struct {
		URL    string `json:"url"`
		Reason string `json:"reason"`
	} `json:"unread"`
	Truncated bool `json:"truncated"`
}

// PackageApplies asks report-server's one matcher whether the patterns match the scope.
func (c *Client) PackageApplies(ctx context.Context, req AppliesRequest) (*AppliesAnswer, error) {
	var out AppliesAnswer
	if err := c.postJSON(ctx, "/api/v1/packages/applies", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func packageQuery(origin string, official bool) url.Values {
	q := url.Values{}
	if origin != "" {
		q.Set("origin", origin)
	}
	if official {
		q.Set("official", "true")
	}
	return q
}

// RouteMissing reports a 404 from a route this cc-data expects and the server does not have
// yet: a report-server older than the validate and applies routes.
func RouteMissing(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
