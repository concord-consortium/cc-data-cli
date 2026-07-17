package api

import (
	"context"
	"net/url"
	"strconv"
)

// Pagination caps; tokens are opaque and only ever echoed, never constructed.
const (
	ReportsPageDefault = 50
	ReportsPageMax     = 200
	BulkPageMax        = 500
)

// FetchPage fetches one keyset page of typed items. An empty token requests the
// first page.
func FetchPage[T any](ctx context.Context, c *Client, path string, limit int, token string, extra url.Values) (Page[T], error) {
	q := url.Values{}
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if token != "" {
		q.Set("page_token", token)
	}
	var page Page[T]
	if err := c.getJSON(ctx, path, q, &page); err != nil {
		return Page[T]{}, err
	}
	return page, nil
}

// DrainPages fetches every page and returns all items concatenated.
func DrainPages[T any](ctx context.Context, c *Client, path string, limit int) ([]T, error) {
	var all []T
	token := ""
	for {
		page, err := FetchPage[T](ctx, c, path, limit, token, nil)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextPageToken == nil || *page.NextPageToken == "" {
			return all, nil
		}
		token = *page.NextPageToken
	}
}

// FetchBulkPage fetches one answers/history page whose items stay raw and whose
// envelope may carry total_endpoints.
func FetchBulkPage(ctx context.Context, c *Client, path string, limit int, token string) (BulkPage, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if token != "" {
		q.Set("page_token", token)
	}
	var page BulkPage
	if err := c.getJSON(ctx, path, q, &page); err != nil {
		return BulkPage{}, err
	}
	return page, nil
}
