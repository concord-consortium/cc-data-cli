// Package api is the typed HTTP client every command shares: the retry policy,
// the landed error vocabulary, pagination, and the presigned-S3 second-call rule.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one report-server origin with one bearer token.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Token   string

	MaxAttempts    int
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	RequestTimeout time.Duration // per-attempt timeout for JSON calls only

	// seams for deterministic tests
	randFloat func() float64
	sleep     func(context.Context, time.Duration)
}

// New builds a client for a server origin and token with the default retry policy.
// The http.Client carries no overall timeout: JSON calls get a per-attempt
// deadline via RequestTimeout, while streaming downloads rely on the caller's
// context so a large CSV or attachment is never cut off mid-body.
func New(baseURL, token string) *Client {
	return &Client{
		HTTP:           &http.Client{},
		BaseURL:        strings.TrimRight(baseURL, "/"),
		Token:          token,
		MaxAttempts:    6,
		BaseBackoff:    500 * time.Millisecond,
		MaxBackoff:     30 * time.Second,
		RequestTimeout: 60 * time.Second,
		randFloat:      rand.Float64,
		sleep:          sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// maxBodyBytes bounds JSON API responses. Bulk answers/history pages use an
// ~8 MiB server byte budget for items; with envelope overhead a page can exceed
// 8 MiB, so the cap is set well above it to avoid truncating a valid page into
// invalid JSON. Presigned downloads (CSV/attachment bytes) stream separately and
// are not bounded by this.
const maxBodyBytes = 64 << 20

// do runs a request under the retry policy and returns the 2xx body, or a typed
// *APIError (contract) / *TransientError (budget exhausted).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var last error
	for attempt := 0; attempt < c.MaxAttempts; attempt++ {
		if attempt > 0 {
			c.sleep(ctx, c.backoff(attempt-1))
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		data, status, err := c.attempt(ctx, method, u, body)
		if err != nil {
			last = err
			continue
		}

		if status >= 200 && status < 300 {
			return data, nil
		}

		apiErr := decodeAPIError(status, data)
		if status == http.StatusTooManyRequests || status >= 500 {
			last = apiErr
			continue
		}
		// Every 4xx-class coded error is a contract error, never blind-retried.
		return nil, apiErr
	}
	return nil, &TransientError{Attempts: c.MaxAttempts, Last: last}
}

// attempt performs one HTTP request under a per-attempt deadline and returns the
// body and status; a transport error (including deadline) is returned as err.
func (c *Client) attempt(ctx context.Context, method, u string, body []byte) ([]byte, int, error) {
	reqCtx := ctx
	var cancel context.CancelFunc
	if c.RequestTimeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, c.RequestTimeout)
		defer cancel()
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, u, reqBody)
	if err != nil {
		return nil, 0, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, 0, err
	}
	return data, resp.StatusCode, nil
}

func (c *Client) backoff(exp int) time.Duration {
	d := c.BaseBackoff << exp
	if d <= 0 || d > c.MaxBackoff {
		d = c.MaxBackoff
	}
	// Full jitter: uniform in [0, d].
	return time.Duration(c.randFloat() * float64(d))
}

// decodeAPIError builds an *APIError from a non-2xx body, capturing all envelope
// fields beyond error/message as Extra.
func decodeAPIError(status int, data []byte) *APIError {
	e := &APIError{Status: status, Extra: map[string]any{}}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err == nil {
		for k, v := range env {
			switch k {
			case "error":
				if s, ok := v.(string); ok {
					e.Code = s
				}
			case "message":
				if s, ok := v.(string); ok {
					e.Message = s
				}
			default:
				e.Extra[k] = v
			}
		}
	}
	if e.Code == "" {
		e.Code = fmt.Sprintf("HTTP_%d", status)
	}
	if len(e.Extra) == 0 {
		e.Extra = nil
	}
	return e
}

// getJSON GETs path and unmarshals the 2xx body into out.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	data, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// postJSON POSTs a JSON body and unmarshals the 2xx response into out.
func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	data, err := c.do(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// deleteJSON issues a DELETE and unmarshals the 2xx response into out.
func (c *Client) deleteJSON(ctx context.Context, path string, out any) error {
	data, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
