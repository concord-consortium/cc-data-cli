package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// localIOError tags a filesystem write/sync/close failure (for example ENOSPC)
// so the retry loop can treat it as terminal: re-minting a fresh presigned URL
// cannot fix a local disk problem.
type localIOError struct{ err error }

func (e *localIOError) Error() string { return e.err.Error() }
func (e *localIOError) Unwrap() error { return e.err }

// destWriter records the first error writing to the destination file so a copy
// failure can be attributed to local I/O rather than the network read.
type destWriter struct {
	f        *os.File
	writeErr error
}

func (d *destWriter) Write(p []byte) (int, error) {
	n, err := d.f.Write(p)
	if err != nil && d.writeErr == nil {
		d.writeErr = err
	}
	return n, err
}

// streamURL GETs a presigned URL with no auth header and copies the body to dst.
// Any non-2xx or transport failure returns an error with the body discarded
// unparsed (S3 XML is outside the JSON error contract). The URL is never logged.
func (c *Client) streamURL(ctx context.Context, rawURL string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("presigned download failed with HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

// StreamToFile requests an envelope, immediately GETs its download_url, and
// streams the bytes to dstPath (fsynced; the caller handles the final rename).
// Any S3 failure, including expiry, re-invokes envelopeFn for a fresh URL within
// the same bounded retry budget.
func (c *Client) StreamToFile(ctx context.Context, envelopeFn func(context.Context) (*DownloadEnvelope, error), dstPath string) (*DownloadEnvelope, error) {
	var last error
	for attempt := 0; attempt < c.MaxAttempts; attempt++ {
		if attempt > 0 {
			c.sleep(ctx, c.backoff(attempt-1))
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		env, err := envelopeFn(ctx)
		if err != nil {
			// The envelope call already exhausted its own retry budget; a
			// contract error there is terminal, not S3-retryable.
			return nil, err
		}

		if err := c.streamToPath(ctx, env.DownloadURL, dstPath); err != nil {
			// A local filesystem failure is terminal: only network/URL errors
			// are worth re-minting a fresh presigned URL for.
			var le *localIOError
			if errors.As(err, &le) {
				return nil, le.err
			}
			last = err
			continue
		}
		return env, nil
	}
	return nil, &TransientError{Attempts: c.MaxAttempts, Last: last}
}

// DownloadURL streams one already-minted presigned URL to dstPath, fsynced. The
// caller decides whether to re-mint on failure.
func (c *Client) DownloadURL(ctx context.Context, rawURL, dstPath string) error {
	return c.streamToPath(ctx, rawURL, dstPath)
}

func (c *Client) streamToPath(ctx context.Context, rawURL, dstPath string) error {
	f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return &localIOError{err}
	}
	dw := &destWriter{f: f}
	if err := c.streamURL(ctx, rawURL, dw); err != nil {
		f.Close()
		os.Remove(dstPath)
		// A write failure (for example ENOSPC) is local and terminal; any
		// other copy error is a network/URL failure and stays retryable.
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
