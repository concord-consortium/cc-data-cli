package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// maxErrorBodyBytes bounds the non-2xx body read on a streamed download, which is a JSON error
// envelope rather than the report.
const maxErrorBodyBytes = 64 << 10

// partialDownloadError is a copy failure after the response was committed and bytes reached the
// destination. It is transient rather than a contract error, since re-running fixes the
// network-blip case, and its message names the other cause because a retry cannot fix that one.
type partialDownloadError struct {
	Wrote int64
	Err   error
}

func (e *partialDownloadError) Error() string {
	return fmt.Sprintf("download failed after %d bytes; the server may have exceeded its download "+
		"budget for this cohort. Re-run to retry, or narrow the report filter.", e.Wrote)
}

func (e *partialDownloadError) Unwrap() error { return e.Err }

// StreamAPIToFile GETs an API path and streams the 2xx body to dstPath. It is do() with the
// buffering removed: same bearer token, same backoff, same decodeAPIError on a non-2xx, so a coded
// refusal reaches AsCLIError intact instead of becoming an untyped HTTP-status error.
//
// It does NOT apply RequestTimeout. That is a per-attempt deadline for JSON calls, and a report
// body legitimately outlives it, so the bound is the caller's context. Each attempt truncates the
// destination, so a retry after a partial write cannot append to it.
func (c *Client) StreamAPIToFile(ctx context.Context, path, dstPath string) error {
	u := c.BaseURL + path
	var last error
	for attempt := 0; attempt < c.MaxAttempts; attempt++ {
		if attempt > 0 {
			c.sleep(ctx, c.backoff(attempt-1))
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		var wrote int64
		err := c.streamToPathWith(ctx, dstPath, func(ctx context.Context, dst io.Writer) error {
			var copyErr error
			wrote, copyErr = c.streamAPIBody(ctx, u, dst)
			return copyErr
		})
		if err == nil {
			return nil
		}

		// A local filesystem failure is terminal: retrying cannot free the disk.
		var le *localIOError
		if errors.As(err, &le) {
			return le.err
		}

		// The caller's context is the only bound on this download, so a cancellation or a
		// caller deadline is an expected way for a long pull to end, and it has to surface as
		// itself rather than as the post-commit failure the copy error otherwise looks like.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		// The server commits the 200 and the header row before any data, then reraises on a
		// mid-stream failure so the chunked framing aborts. Bytes on disk therefore mean the
		// failure landed after commit, and its likeliest cause is the server passing its own
		// download deadline, which a retry reproduces exactly: there is no Range support to
		// resume from, so each retry recomputes the whole portal query against a server that
		// admits two at a time. Every refusal worth retrying arrives before the first byte.
		if wrote > 0 {
			return &partialDownloadError{Wrote: wrote, Err: err}
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if apiErr.Status != http.StatusTooManyRequests && apiErr.Status < 500 {
				return apiErr
			}
		}
		last = err
	}
	return &TransientError{Attempts: c.MaxAttempts, Last: last}
}

// streamAPIBody performs one authenticated GET and copies the 2xx body to dst, returning the
// bytes copied. A non-2xx body is decoded as a coded error and nothing is written.
func (c *Client) streamAPIBody(ctx context.Context, u string, dst io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	// The success body is a CSV and the refusal body is the JSON error envelope.
	req.Header.Set("Accept", "text/csv, application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return 0, decodeAPIError(resp.StatusCode, data)
	}
	return io.Copy(dst, resp.Body)
}

// streamToPathWith owns the destination file for any streamed download: truncate, copy, classify a
// local failure as localIOError, fsync, and remove the partial file on error. fetch performs one
// request and copies the body to dst.
func (c *Client) streamToPathWith(ctx context.Context, dstPath string, fetch func(context.Context, io.Writer) error) error {
	f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return &localIOError{err}
	}
	dw := &destWriter{f: f}
	if err := fetch(ctx, dw); err != nil {
		f.Close()
		os.Remove(dstPath)
		// A write failure (for example ENOSPC) is local and terminal; any other copy error is
		// a network failure and stays retryable.
		if dw.writeErr != nil {
			return &localIOError{dw.writeErr}
		}
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(dstPath)
		return &localIOError{err}
	}
	if err := f.Close(); err != nil {
		os.Remove(dstPath)
		return &localIOError{err}
	}
	return nil
}
