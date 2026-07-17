package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(baseURL string) *Client {
	c := New(baseURL, "ccd_test")
	c.BaseBackoff = time.Millisecond
	c.MaxBackoff = time.Millisecond
	c.randFloat = func() float64 { return 1.0 }
	c.sleep = func(ctx context.Context, d time.Duration) {}
	return c
}

func TestClassificationMatrix(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		wantCode string
		wantExit int
	}{
		{400, `{"error":"BAD_REQUEST","message":"limit must be an integer"}`, "BAD_REQUEST", 5},
		{404, `{"error":"NOT_FOUND","message":"Not found."}`, "NOT_FOUND", 5},
		{410, `{"error":"EXPIRED_CURSOR","message":"expired"}`, "EXPIRED_CURSOR", 5},
		{409, `{"error":"NOT_READY","athena_query_state":"running"}`, "NOT_READY", 5},
		{401, `{"error":"NOT_AUTHENTICATED","message":"nope"}`, "NOT_AUTHENTICATED", 3},
		{422, `{"error":"WEIRD_NEW_CODE","message":"x"}`, "WEIRD_NEW_CODE", 5},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			fmt.Fprint(w, c.body)
		}))
		cl := testClient(srv.URL)
		err := cl.getJSON(context.Background(), "/x", nil, nil)
		srv.Close()
		ae, ok := err.(*APIError)
		if !ok {
			t.Fatalf("status %d: want *APIError, got %T (%v)", c.status, err, err)
		}
		if ae.Code != c.wantCode {
			t.Fatalf("status %d: code = %q, want %q", c.status, ae.Code, c.wantCode)
		}
		cliErr := AsCLIError(err)
		if cliErr.ExitCode != c.wantExit {
			t.Fatalf("status %d: exit = %d, want %d", c.status, cliErr.ExitCode, c.wantExit)
		}
	}
}

func TestNotReadyExtra(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		fmt.Fprint(w, `{"error":"NOT_READY","athena_query_state":"running"}`)
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	err := cl.getJSON(context.Background(), "/x", nil, nil)
	ae := err.(*APIError)
	if ae.Extra["athena_query_state"] != "running" {
		t.Fatalf("extra not carried: %v", ae.Extra)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"SERVER_ERROR"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	var out map[string]any
	if err := cl.getJSON(context.Background(), "/x", nil, &out); err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryExhaustionTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"SERVER_ERROR"}`)
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	err := cl.getJSON(context.Background(), "/x", nil, nil)
	te, ok := err.(*TransientError)
	if !ok {
		t.Fatalf("want *TransientError, got %T", err)
	}
	if te.Attempts != cl.MaxAttempts {
		t.Fatalf("attempts = %d", te.Attempts)
	}
	if AsCLIError(err).ExitCode != 6 {
		t.Fatalf("transient should map to exit 6")
	}
}

func TestContractErrorNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"BAD_REQUEST"}`)
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	_ = cl.getJSON(context.Background(), "/x", nil, nil)
	if calls != 1 {
		t.Fatalf("contract error must not retry, got %d calls", calls)
	}
}

func Test429Retries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":"BAD_REQUEST"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	if err := cl.getJSON(context.Background(), "/x", nil, nil); err != nil {
		t.Fatalf("429 should retry then succeed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

func TestBearerHeaderSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	_ = cl.getJSON(context.Background(), "/x", nil, nil)
	if gotAuth != "Bearer ccd_test" {
		t.Fatalf("auth header = %q", gotAuth)
	}
}

func TestPaginationDrain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page_token") {
		case "":
			fmt.Fprint(w, `{"items":[{"id":1},{"id":2}],"next_page_token":"tok2"}`)
		case "tok2":
			fmt.Fprint(w, `{"items":[{"id":3}],"next_page_token":null}`)
		default:
			t.Errorf("unexpected token %q", r.URL.Query().Get("page_token"))
		}
	}))
	defer srv.Close()
	cl := testClient(srv.URL)
	items, err := DrainPages[ReportRun](context.Background(), cl, "/reports", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].ID != 1 || items[2].ID != 3 {
		t.Fatalf("drain = %+v", items)
	}
}

func TestStreamToFileReMintsOnS3Failure(t *testing.T) {
	var s3Calls int32
	// S3 endpoint: fail first GET, succeed second.
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&s3Calls, 1) == 1 {
			w.WriteHeader(403)
			fmt.Fprint(w, `<Error>expired</Error>`)
			return
		}
		fmt.Fprint(w, "col\n1\n")
	}))
	defer s3.Close()

	var envCalls int32
	cl := testClient("http://unused")
	dst := t.TempDir() + "/out.csv"
	env, err := cl.StreamToFile(context.Background(), func(ctx context.Context) (*DownloadEnvelope, error) {
		atomic.AddInt32(&envCalls, 1)
		return &DownloadEnvelope{DownloadURL: s3.URL, Filename: "x.csv", ExpiresInSeconds: 600}, nil
	}, dst)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if env.Filename != "x.csv" {
		t.Fatalf("env = %+v", env)
	}
	if envCalls != 2 {
		t.Fatalf("expected 2 envelope requests (re-mint), got %d", envCalls)
	}
}
