package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/creds"
	"github.com/zalando/go-keyring"
)

// fakeServer models the report-service auth + token surfaces the auth flow uses,
// pinned to the shapes recorded in wire-captures.md.
type fakeServer struct {
	*httptest.Server
	mu           sync.Mutex
	challenges   map[string]string // code -> code_challenge
	tokens       map[string]bool   // token -> active
	olderServer  bool              // 404 the tokens routes
	seenBodyOnly bool              // token exchange carried secrets in the body
}

var challengeRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func newFakeServer(olderServer bool) *fakeServer {
	fs := &fakeServer{
		challenges:  map[string]string{},
		tokens:      map[string]bool{},
		olderServer: olderServer,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/cli", fs.handleAuthCLI)
	mux.HandleFunc("/auth/cli/token", fs.handleTokenExchange)
	mux.HandleFunc("/api/v1/tokens/current", fs.handleTokensCurrent)
	mux.HandleFunc("/api/v1/reports", fs.handleReports)
	fs.Server = httptest.NewServer(mux)
	return fs
}

func (fs *fakeServer) handleAuthCLI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// portal is optional-with-fallback: validated only when present.
	if portal := q.Get("portal"); portal != "" && !strings.HasPrefix(portal, "https://") {
		http.Error(w, "bad portal", http.StatusBadRequest)
		return
	}
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, "/callback") {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad method", http.StatusBadRequest)
		return
	}
	challenge := q.Get("code_challenge")
	if !challengeRegex.MatchString(challenge) {
		http.Error(w, "bad challenge", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	code := "grantcode123"
	fs.mu.Lock()
	fs.challenges[code] = challenge
	fs.mu.Unlock()
	http.Redirect(w, r, fmt.Sprintf("%s?code=%s&state=%s", redirect, code, state), http.StatusFound)
}

func (fs *fakeServer) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	// Secrets must be body-only; reject them as query params.
	if r.URL.Query().Get("code") != "" || r.URL.Query().Get("code_verifier") != "" {
		http.Error(w, `{"error":"BAD_REQUEST","message":"secrets must be in the body"}`, http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var in struct {
		Code     string `json:"code"`
		Verifier string `json:"code_verifier"`
		Label    string `json:"label"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, `{"error":"BAD_REQUEST"}`, http.StatusBadRequest)
		return
	}
	fs.mu.Lock()
	want, ok := fs.challenges[in.Code]
	fs.seenBodyOnly = in.Code != "" && in.Verifier != ""
	fs.mu.Unlock()
	if !ok || Challenge(in.Verifier) != want {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"BAD_REQUEST","message":"Invalid code or verifier."}`)
		return
	}
	token := "ccd_minted_" + in.Label
	fs.mu.Lock()
	fs.tokens[token] = true
	fs.mu.Unlock()
	fmt.Fprintf(w, `{"token":%q}`, token)
}

func (fs *fakeServer) bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(h, "Bearer ")
	fs.mu.Lock()
	active := fs.tokens[token]
	fs.mu.Unlock()
	return token, active
}

func (fs *fakeServer) handleTokensCurrent(w http.ResponseWriter, r *http.Request) {
	if fs.olderServer {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"NOT_FOUND","message":"Not found."}`)
		return
	}
	token, active := fs.bearer(r)
	if !active {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"NOT_AUTHENTICATED","message":"You must supply a valid API token."}`)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		fs.mu.Lock()
		fs.tokens[token] = false
		fs.mu.Unlock()
		fmt.Fprint(w, `{"revoked":true}`)
	default:
		fmt.Fprint(w, `{"label":"CLI login (host)","last_used_at":null,"created_at":"2026-07-17T13:16:32Z","report_access":true}`)
	}
}

func (fs *fakeServer) handleReports(w http.ResponseWriter, r *http.Request) {
	if _, active := fs.bearer(r); !active {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"NOT_AUTHENTICATED"}`)
		return
	}
	fmt.Fprint(w, `{"items":[],"next_page_token":null}`)
}

func setupCredHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	keyring.MockInit()
}

// browserStub follows the auth URL like a real browser (following the 302 to the
// loopback callback), so the real login flow completes in-process.
func browserStub(t *testing.T) func() {
	t.Helper()
	orig := openBrowser
	openBrowser = func(rawURL string) error {
		go func() {
			resp, err := http.Get(rawURL)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
		return nil
	}
	return func() { openBrowser = orig }
}

func TestFullAuthFlowCurrentServer(t *testing.T) {
	setupCredHome(t)
	fs := newFakeServer(false)
	defer fs.Close()
	defer browserStub(t)()

	var progress strings.Builder
	err := Login(context.Background(), LoginOptions{
		Portal:   "learn.concord.org",
		Server:   fs.URL,
		Progress: &progress,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !fs.seenBodyOnly {
		t.Fatal("token exchange did not carry secrets in the body")
	}

	// Stored credential should point at the fake server.
	var store creds.Store
	_, server, err := store.Get("learn.concord.org")
	if err != nil {
		t.Fatal(err)
	}
	if server != fs.URL {
		t.Fatalf("recorded server = %q, want %q", server, fs.URL)
	}

	// auth status --check should report the token valid with metadata.
	res, err := Status(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Portals) != 1 || !res.Portals[0].Valid {
		t.Fatalf("status: %+v", res.Portals)
	}
	if res.Portals[0].Label == nil || *res.Portals[0].Label != "CLI login (host)" {
		t.Fatalf("label wrong: %+v", res.Portals[0].Label)
	}

	// logout revokes and removes local.
	if err := Logout(context.Background(), "learn.concord.org", &progress); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := store.Get("learn.concord.org"); err == nil {
		t.Fatal("credential should be gone after logout")
	}
}

func TestAuthFlowOlderServerDegradation(t *testing.T) {
	setupCredHome(t)
	fs := newFakeServer(true) // 404 the tokens routes
	defer fs.Close()
	defer browserStub(t)()

	var progress strings.Builder
	if err := Login(context.Background(), LoginOptions{Portal: "learn.concord.org", Server: fs.URL, Progress: &progress}); err != nil {
		t.Fatalf("login: %v", err)
	}

	// --check falls back to the reports probe with metadata unknown.
	res, err := Status(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	p := res.Portals[0]
	if !p.Valid || !p.MetadataUnknown {
		t.Fatalf("older-server check: valid=%v unknown=%v err=%s", p.Valid, p.MetadataUnknown, p.CheckError)
	}

	// logout on the older server warns but still deletes locally and succeeds.
	if err := Logout(context.Background(), "learn.concord.org", &progress); err != nil {
		t.Fatalf("older-server logout should succeed: %v", err)
	}
	if !strings.Contains(progress.String(), "may still be active") {
		t.Fatalf("expected older-server logout warning, got: %s", progress.String())
	}
	var store creds.Store
	if _, _, err := store.Get("learn.concord.org"); err == nil {
		t.Fatal("credential should be gone after older-server logout")
	}
}

func TestLogoutAlreadyRevoked(t *testing.T) {
	setupCredHome(t)
	fs := newFakeServer(false)
	defer fs.Close()

	// Store a credential whose token the server does not know (already revoked).
	var store creds.Store
	if err := store.Save("learn.concord.org", "ccd_dead", fs.URL); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	if err := Logout(context.Background(), "learn.concord.org", &progress); err != nil {
		t.Fatalf("logout with dead token should still succeed: %v", err)
	}
	if !strings.Contains(progress.String(), "nothing needed revoking") {
		t.Fatalf("expected nothing-needed-revoking note, got: %s", progress.String())
	}
}
