package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/output"
)

// truncatingServer writes part of a CSV and then hijacks and closes the connection, which aborts
// the chunked framing exactly as a mid-stream server failure does.
func truncatingServer(calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "learner_id,run_remote_endpoint\n1,https://portal/e/AAA\n")
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		conn.Close()
	}))
}

// A missing Authorization header is invisible from the client, so the assertion has to be made
// server-side. streamURL sends none, which is right for a presigned S3 URL and would 401 here.
func TestStreamAPIToFileSendsTheBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, "learner_id\n1\n")
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.csv")
	if err := testClient(srv.URL).StreamAPIToFile(context.Background(), "/api/v1/reports/584/download", dst); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer ccd_test" {
		t.Fatalf("Authorization = %q, want the bearer token", gotAuth)
	}
}

func TestStreamAPIToFileWritesExactlyTheBytesServed(t *testing.T) {
	const body = "learner_id,run_remote_endpoint\n1,https://portal/e/AAA\n2,https://portal/e/BBB\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.csv")
	if err := testClient(srv.URL).StreamAPIToFile(context.Background(), "/download", dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("wrote %q, want %q", got, body)
	}
}

// The one refusal only the Portal path can produce. Flattening it into an HTTP-status error, as
// streamURL does, would lose the code and the message a caller acts on.
func TestStreamAPIToFileSurfacesACodedRefusal(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"error":"UNPROCESSABLE","message":"This report run has no filters and cannot be downloaded."}`)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.csv")
	err := testClient(srv.URL).StreamAPIToFile(context.Background(), "/download", dst)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want an *APIError", err, err)
	}
	if apiErr.Code != "UNPROCESSABLE" || !strings.Contains(apiErr.Message, "no filters") {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if calls != 1 {
		t.Fatalf("a contract refusal was requested %d times, want 1", calls)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("a refused download left a file behind: %v", err)
	}
}

func TestStreamAPIToFileRetriesTheRefusalsWorthRetrying(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"the download limiter's 503", http.StatusServiceUnavailable, `{"error":"SERVICE_UNAVAILABLE","message":"Too many concurrent report downloads; retry shortly."}`},
		{"a 429", http.StatusTooManyRequests, `{"error":"TOO_MANY_REQUESTS","message":"slow down"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&calls, 1) <= 2 {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				fmt.Fprint(w, "learner_id\n1\n")
			}))
			defer srv.Close()

			dst := filepath.Join(t.TempDir(), "out.csv")
			if err := testClient(srv.URL).StreamAPIToFile(context.Background(), "/download", dst); err != nil {
				t.Fatal(err)
			}
			// These arrive before any body byte, so the narrower retry rule must not swallow
			// them: three attempts, the same count an unconditional retry would take.
			if calls != 3 {
				t.Fatalf("took %d attempts, want 3", calls)
			}
		})
	}
}

func TestStreamAPIToFileExhaustsItsBudgetAsTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"SERVICE_UNAVAILABLE","message":"busy"}`)
	}))
	defer srv.Close()

	cl := testClient(srv.URL)
	dst := filepath.Join(t.TempDir(), "out.csv")
	err := cl.StreamAPIToFile(context.Background(), "/download", dst)

	var transient *TransientError
	if !errors.As(err, &transient) {
		t.Fatalf("err = %v (%T), want a *TransientError", err, err)
	}
	if int(calls) != cl.MaxAttempts {
		t.Fatalf("made %d attempts, want %d", calls, cl.MaxAttempts)
	}
	if AsCLIError(err).ExitCode != output.ExitTransient {
		t.Fatalf("exit code = %d, want transient", AsCLIError(err).ExitCode)
	}
}

func TestStreamAPIToFileTreatsTruncationAsAFailedDownload(t *testing.T) {
	var calls int32
	srv := truncatingServer(&calls)
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.csv")
	err := testClient(srv.URL).StreamAPIToFile(context.Background(), "/download", dst)
	if err == nil {
		t.Fatal("a truncated response must not read as a successful download")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("a truncated download left a partial file behind: %v", statErr)
	}
	// Retrying a post-commit failure recomputes the whole portal query against a server that
	// admits two at a time, and reproduces the likeliest cause exactly.
	if calls != 1 {
		t.Fatalf("a truncating server was requested %d times, want 1", calls)
	}
	if AsCLIError(err).ExitCode != output.ExitTransient {
		t.Fatalf("exit code = %d, want transient", AsCLIError(err).ExitCode)
	}
	if !strings.Contains(err.Error(), "narrow the report filter") {
		t.Fatalf("the failure names no recovery: %v", err)
	}
}

// RequestTimeout is a per-attempt deadline for JSON calls. Carrying it onto a streamed download
// would abandon a large cohort mid-body, and multiply the abandonment by the retry budget.
func TestStreamAPIToFileOutlivesTheJSONRequestTimeout(t *testing.T) {
	const body = "learner_id\n1\n2\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		for _, line := range strings.SplitAfter(body, "\n") {
			fmt.Fprint(w, line)
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	cl := testClient(srv.URL)
	cl.RequestTimeout = 10 * time.Millisecond
	dst := filepath.Join(t.TempDir(), "out.csv")
	if err := cl.StreamAPIToFile(context.Background(), "/download", dst); err != nil {
		t.Fatalf("a body slower than RequestTimeout must still complete: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("wrote %q, want %q", got, body)
	}
}

// A disk-full download retried six times and reported as transient tells a researcher to try again
// at the one thing that cannot work.
func TestStreamAPIToFileDoesNotRetryALocalWriteFailure(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, "learner_id\n1\n")
	}))
	defer srv.Close()

	// A directory cannot be opened for writing, which is the same class of failure as ENOSPC.
	dst := filepath.Join(t.TempDir(), "out.csv")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}

	err := testClient(srv.URL).StreamAPIToFile(context.Background(), "/download", dst)
	if err == nil {
		t.Fatal("writing to a directory must fail")
	}
	var transient *TransientError
	if errors.As(err, &transient) {
		t.Fatalf("a local disk failure was reported as transient: %v", err)
	}
	if AsCLIError(err).ExitCode != output.ExitInternal {
		t.Fatalf("exit code = %d, want internal", AsCLIError(err).ExitCode)
	}
	if calls != 0 {
		t.Fatalf("a local failure cost %d server downloads, want 0", calls)
	}
}

// The caller's context is the only bound on a streamed download, so a cancellation is an expected
// way for a long pull to end. Classified as a partial download it would blame the server's budget
// and advise narrowing a filter that has nothing to do with it.
func TestStreamAPIToFileSurfacesACanceledContextAsItself(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, "learner_id\n1\n")
		w.(http.Flusher).Flush()
		cancel()
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "2\n")
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.csv")
	err := testClient(srv.URL).StreamAPIToFile(ctx, "/download", dst)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v (%T), want context.Canceled", err, err)
	}
	if strings.Contains(err.Error(), "narrow the report filter") {
		t.Fatalf("a cancellation was blamed on the server's download budget: %v", err)
	}
}
