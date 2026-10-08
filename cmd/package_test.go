package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/packages"
)

const testManifest = `{"name": "wildfire-responses", "title": "Wildfire responses", "version": "0.1.0",
  "urls": {"all": [], "any": [], "none": []}, "clue_prepull": false,
  "entrypoint": "run.py", "expected_duration_seconds": 300}`

type seenRequest struct {
	method, path, contentType, auth string
	query                           map[string][]string
	contentLength                   int64
	body                            []byte
}

// packageServer answers the routes it is given and 404s every other path, as a report-server
// without the validate and applies routes does.
type packageServer struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

func newPackageServer(t *testing.T, routes map[string]func(http.ResponseWriter, []byte)) *packageServer {
	t.Helper()
	s := &packageServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, seenRequest{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.URL.Query(), r.ContentLength, body})
		s.mu.Unlock()
		if handle, ok := routes[r.URL.Path]; ok {
			handle(w, body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"NOT_FOUND","message":"Not found."}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *packageServer) requests(path string) []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []seenRequest
	for _, r := range s.seen {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func answer(status int, body string) func(http.ResponseWriter, []byte) {
	return func(w http.ResponseWriter, _ []byte) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
}

const validated = `{"identity": "users/7/p", "version": "0.1.0", "checksum": "sha256:x", "visibility": "private", "already_published": false, "publishing_unavailable": null}`

func validateAnswering(body string) map[string]func(http.ResponseWriter, []byte) {
	return map[string]func(http.ResponseWriter, []byte){"/api/v1/packages/validate": answer(http.StatusOK, body)}
}

func writePackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"manifest.json": testManifest, "run.py": "print(1)\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func capture(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	restore := output.SetStreams(&out, &errb)
	t.Cleanup(restore)
	return &out, &errb
}

func defaultZip(dir string) string {
	return filepath.Join(dir, packages.BuildDirName, "wildfire-responses-0.1.0.zip")
}

func asCLIError(t *testing.T, err error) *output.CLIError {
	t.Helper()
	var cliErr *output.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("err = %v (%T), want a *output.CLIError", err, err)
	}
	return cliErr
}

func TestPackageInitRefusesASecondInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-package")
	written, err := packages.Init(dir, "")
	if err != nil || strings.Join(written, ",") != "manifest.json,run.py" {
		t.Fatalf("first init: %v, %v", written, err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"name": "my-package"`) {
		t.Errorf("manifest = %s", manifest)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.py"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := packages.Init(dir, ""); err == nil {
		t.Error("a second init succeeded")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "run.py")); string(got) != "mine" {
		t.Errorf("the second init overwrote run.py: %q", got)
	}
	if _, err := packages.Init(filepath.Join(t.TempDir(), "Not_A_Name"), ""); err == nil {
		t.Error("init accepted a directory name outside the grammar without --name")
	}
}

func TestPackageBuildValidatesThenWrites(t *testing.T) {
	dir := writePackage(t)
	srv := newPackageServer(t, validateAnswering(validated))
	out, _ := capture(t)
	if err := (packageBuildFlags{}).run(context.Background(), api.New(srv.URL, "tok"), dir); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests("/api/v1/packages/validate")
	if len(reqs) != 1 {
		t.Fatalf("%d validate requests, want 1", len(reqs))
	}
	written, err := os.ReadFile(defaultZip(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reqs[0].body, written) || reqs[0].contentType != "application/zip" {
		t.Errorf("validate got %d bytes as %q; the zip written is %d bytes", len(reqs[0].body), reqs[0].contentType, len(written))
	}
	if gi, err := os.ReadFile(filepath.Join(dir, packages.BuildDirName, ".gitignore")); err != nil || string(gi) != "*\n" {
		t.Errorf(".cc-data-build/.gitignore = %q, %v", gi, err)
	}
	r, err := zip.NewReader(bytes.NewReader(written), int64(len(written)))
	if err != nil || len(r.File) != 2 {
		t.Fatalf("zip: %v, %v", r, err)
	}
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["checksum"] != packages.Checksum(written) {
		t.Errorf("printed checksum %v, archive %s", line["checksum"], packages.Checksum(written))
	}

	t.Chdir(dir)
	out.Reset()
	if err := (packageBuildFlags{}).run(context.Background(), api.New(srv.URL, "tok"), "."); err != nil {
		t.Fatal(err)
	}
	var again map[string]any
	if err := json.Unmarshal(out.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again["checksum"] != line["checksum"] {
		t.Errorf("a second build from inside the package gave %v, want %v", again["checksum"], line["checksum"])
	}
}

func TestPackageBuildWritesNothingTheServerRefuses(t *testing.T) {
	dir := writePackage(t)
	srv := newPackageServer(t, map[string]func(http.ResponseWriter, []byte){
		"/api/v1/packages/validate": answer(http.StatusUnprocessableEntity, `{"error":"UNPROCESSABLE","message":"version must be MAJOR.MINOR.PATCH"}`),
	})
	capture(t)
	err := (packageBuildFlags{}).run(context.Background(), api.New(srv.URL, "tok"), dir)
	if cliErr := asCLIError(t, err); cliErr.Code != "UNPROCESSABLE" || cliErr.ExitCode != output.ExitContract {
		t.Errorf("err = %+v", cliErr)
	}
	if _, err := os.Stat(defaultZip(dir)); err == nil {
		t.Error("a zip was written for a refused archive")
	}
}

// buildWarnings builds against a validate route answering body and returns stderr.
func buildWarnings(t *testing.T, body string) string {
	t.Helper()
	dir := writePackage(t)
	srv := newPackageServer(t, validateAnswering(body))
	_, errb := capture(t)
	if err := (packageBuildFlags{}).run(context.Background(), api.New(srv.URL, "tok"), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(defaultZip(dir)); err != nil {
		t.Errorf("no zip written: %v", err)
	}
	return errb.String()
}

func TestPackageBuildWarnsOnAPublishedVersionAndStillWrites(t *testing.T) {
	got := buildWarnings(t, strings.Replace(validated, `"already_published": false`, `"already_published": true`, 1))
	if !strings.Contains(got, "users/7/p 0.1.0 is already published; publish will refuse it until the version changes") {
		t.Errorf("stderr = %q", got)
	}
}

func TestPackageBuildIsQuietOnANewVersion(t *testing.T) {
	if got := buildWarnings(t, validated); strings.Contains(got, "already published") {
		t.Errorf("stderr = %q", got)
	}
}

func TestPackageBuildWarnsWhenThePortalCannotPublishAndStillWrites(t *testing.T) {
	got := buildWarnings(t, strings.Replace(validated, `"publishing_unavailable": null`, `"publishing_unavailable": "publishing is not configured for x"`, 1))
	if !strings.Contains(got, "this package cannot be published yet: publishing is not configured for x") {
		t.Errorf("stderr = %q", got)
	}
}

func TestPackageBuildIsQuietWhenPublishingIsAvailable(t *testing.T) {
	if got := buildWarnings(t, validated); strings.Contains(got, "cannot be published yet") {
		t.Errorf("stderr = %q", got)
	}
}

func TestPackageBuildValidatesAsThePublishWillBe(t *testing.T) {
	dir := writePackage(t)
	srv := newPackageServer(t, validateAnswering(validated))
	capture(t)
	client := api.New(srv.URL, "tok")
	if err := (packageBuildFlags{origin: "projects/20", official: true}).run(context.Background(), client, dir); err != nil {
		t.Fatal(err)
	}
	if err := (packageBuildFlags{}).run(context.Background(), client, dir); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests("/api/v1/packages/validate")
	if len(reqs) != 2 {
		t.Fatalf("%d validate requests, want 2", len(reqs))
	}
	if q := reqs[0].query; q["origin"][0] != "projects/20" || q["official"][0] != "true" {
		t.Errorf("flagged build's query = %v", q)
	}
	if q := reqs[1].query; len(q) != 0 {
		t.Errorf("plain build's query = %v, want none", q)
	}
}

func TestValidateReturnsAPublishedVersionAsAnAnswerNotAnError(t *testing.T) {
	srv := newPackageServer(t, validateAnswering(strings.Replace(validated, `"already_published": false`, `"already_published": true`, 1)))
	capture(t)
	ans, err := validate(context.Background(), api.New(srv.URL, "tok"), []byte("zip"), "", false)
	if err != nil || ans == nil || !ans.AlreadyPublished {
		t.Errorf("validate = %+v, %v", ans, err)
	}
}

func TestValidateReturnsAnUnavailablePortalAsAnAnswerNotAnError(t *testing.T) {
	srv := newPackageServer(t, validateAnswering(strings.Replace(validated, `"publishing_unavailable": null`, `"publishing_unavailable": "publishing is not configured for x"`, 1)))
	capture(t)
	ans, err := validate(context.Background(), api.New(srv.URL, "tok"), []byte("zip"), "", false)
	if err != nil || ans == nil || ans.PublishingUnavailable == nil || *ans.PublishingUnavailable != "publishing is not configured for x" {
		t.Errorf("validate = %+v, %v", ans, err)
	}
}

func TestPackageBuildOnAServerWithoutTheRouteStillBuilds(t *testing.T) {
	dir := writePackage(t)
	srv := newPackageServer(t, nil)
	_, errb := capture(t)
	if err := (packageBuildFlags{}).run(context.Background(), api.New(srv.URL, "tok"), dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errb.String(), "no validate route yet") {
		t.Errorf("stderr = %q", errb.String())
	}
	if _, err := os.Stat(defaultZip(dir)); err != nil {
		t.Errorf("no zip written: %v", err)
	}
}

func TestPackageBuildRefusesAnOutputItWouldCollect(t *testing.T) {
	dir := writePackage(t)
	srv := newPackageServer(t, validateAnswering(validated))
	capture(t)
	err := (packageBuildFlags{out: filepath.Join(dir, "p.zip")}).run(context.Background(), api.New(srv.URL, "tok"), dir)
	if cliErr := asCLIError(t, err); cliErr.ExitCode != output.ExitUsage {
		t.Errorf("err = %+v", cliErr)
	}
	if err := (packageBuildFlags{out: filepath.Join(dir, ".builds", "p.zip")}).run(context.Background(), api.New(srv.URL, "tok"), dir); err != nil {
		t.Errorf("an --out under a dot directory was refused: %v", err)
	}
}

func TestPackageBuildRefusesANameThatIsAPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a", "pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := strings.Replace(testManifest, `"name": "wildfire-responses"`, `"name": "../../escaped"`, 1)
	for name, content := range map[string]string{"manifest.json": manifest, "run.py": "print(1)\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newPackageServer(t, nil)
	capture(t)
	err := (packageBuildFlags{}).run(context.Background(), api.New(srv.URL, "tok"), dir)
	if cliErr := asCLIError(t, err); cliErr.Code != "INVALID_MANIFEST" {
		t.Errorf("err = %+v", cliErr)
	}
	var zips []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".zip") {
			zips = append(zips, p)
		}
		return nil
	})
	if len(zips) != 0 {
		t.Errorf("zips written: %v", zips)
	}
}

func testScope(t *testing.T) packages.LocalScope {
	t.Helper()
	scope, err := packages.ParseLocalScope([]byte(`{"kind": "class", "id": "h",
	  "classes": [{"class_hash": "0123456789abcdef0123456789abcdef0123456789abcdef", "class_id": 6}],
	  "assignments": [{"offering_id": 9, "runnable_id": 1234, "name": "Wildfire", "url": "https://activity-player.concord.org/?sequence=830"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestPackageRunAsksTheServersMatcher(t *testing.T) {
	srv := newPackageServer(t, map[string]func(http.ResponseWriter, []byte){
		"/api/v1/packages/applies": answer(http.StatusOK, `{"applies": false, "reason": "no URL in this class matches the required pattern *//wildfire.concord.org/*", "interactive_urls": [], "unread": [], "truncated": false}`),
	})
	capture(t)
	verdict, err := appliesFor(api.New(srv.URL, "tok"), testScope(t))(context.Background(), packages.URLs{All: []string{"*//wildfire.concord.org/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Applies || verdict.Unconfirmed || !strings.Contains(verdict.Reason, "required pattern") {
		t.Errorf("verdict = %+v", verdict)
	}
	var sent struct {
		URLs           map[string][]string `json:"urls"`
		AssignmentURLs []string            `json:"assignment_urls"`
	}
	if err := json.Unmarshal(srv.requests("/api/v1/packages/applies")[0].body, &sent); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sent.URLs["all"], ",") != "*//wildfire.concord.org/*" || strings.Join(sent.AssignmentURLs, ",") != "https://activity-player.concord.org/?sequence=830" {
		t.Errorf("sent %+v", sent)
	}
}

func TestPackageRunOnAServerWithoutTheRouteIsUnconfirmed(t *testing.T) {
	srv := newPackageServer(t, nil)
	capture(t)
	verdict, err := appliesFor(api.New(srv.URL, "tok"), testScope(t))(context.Background(), packages.URLs{Any: []string{"*x*"}})
	if err != nil || !verdict.Unconfirmed {
		t.Errorf("verdict = %+v, %v", verdict, err)
	}
}

func TestPackageRunAlwaysSendsURLs(t *testing.T) {
	srv := newPackageServer(t, map[string]func(http.ResponseWriter, []byte){
		"/api/v1/packages/applies": answer(http.StatusOK, `{"applies": true, "reason": null}`),
	})
	capture(t)
	if _, err := appliesFor(api.New(srv.URL, "tok"), testScope(t))(context.Background(), packages.URLs{None: []string{"*x*"}}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(srv.requests("/api/v1/packages/applies")[0].body, &sent); err != nil {
		t.Fatal(err)
	}
	if urls, ok := sent["urls"]; !ok || string(urls) == "null" {
		t.Errorf("applies body = %s", srv.requests("/api/v1/packages/applies")[0].body)
	}
}

func TestPackageRunKeepsTheServersCode(t *testing.T) {
	for _, tc := range []struct {
		apiErr *api.APIError
		exit   int
	}{
		{&api.APIError{Status: 503, Code: "SERVICE_UNAVAILABLE", Message: "deriver down"}, output.ExitContract},
		{&api.APIError{Status: 401, Code: "NOT_AUTHENTICATED"}, output.ExitNotAuth},
	} {
		got := asCLIError(t, packageRunError(api.AsCLIError(tc.apiErr)))
		if got.Code != tc.apiErr.Code || got.ExitCode != tc.exit {
			t.Errorf("%s: got %s exit %d", tc.apiErr.Code, got.Code, got.ExitCode)
		}
	}
}

func publishAnswering(checksum string) map[string]func(http.ResponseWriter, []byte) {
	return map[string]func(http.ResponseWriter, []byte){"/api/v1/packages": func(w http.ResponseWriter, body []byte) {
		if checksum == "" {
			checksum = packages.Checksum(body)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"catalog_id": 3, "identity": "projects/20/p", "version": "0.1.0",
			"checksum": checksum, "visibility": "private", "official": false, "current_version": "0.1.0"})
	}}
}

func TestPackagePublishSendsTheZipAsTheBody(t *testing.T) {
	srv := newPackageServer(t, publishAnswering(""))
	out, _ := capture(t)
	archive := []byte("PK zip bytes")
	if err := (packagePublishFlags{origin: "projects/20"}).run(context.Background(), api.New(srv.URL, "tok"), archive); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests("/api/v1/packages")
	if len(reqs) != 1 {
		t.Fatalf("%d publish requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodPost || r.contentType != "application/zip" || r.contentLength != int64(len(archive)) || !bytes.Equal(r.body, archive) || r.auth != "Bearer tok" {
		t.Errorf("request = %s %q length %d auth %q", r.method, r.contentType, r.contentLength, r.auth)
	}
	if r.query["origin"][0] != "projects/20" || r.query["official"] != nil {
		t.Errorf("query = %v", r.query)
	}
	if !strings.Contains(out.String(), "identity         projects/20/p") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestPackagePublishRefusesAChecksumItDidNotSend(t *testing.T) {
	srv := newPackageServer(t, publishAnswering("sha256:other"))
	capture(t)
	err := (packagePublishFlags{}).run(context.Background(), api.New(srv.URL, "tok"), []byte("zip"))
	if err == nil || !strings.Contains(err.Error(), "sha256:other") {
		t.Errorf("err = %v", err)
	}
}

func TestPackagePublishPassesTheServersCodeThrough(t *testing.T) {
	srv := newPackageServer(t, map[string]func(http.ResponseWriter, []byte){
		"/api/v1/packages": answer(http.StatusConflict, `{"error":"ALREADY_EXISTS","message":"version 0.1.0 exists"}`),
	})
	capture(t)
	err := (packagePublishFlags{}).run(context.Background(), api.New(srv.URL, "tok"), []byte("zip"))
	if cliErr := asCLIError(t, err); cliErr.Code != "ALREADY_EXISTS" || cliErr.ExitCode != output.ExitContract {
		t.Errorf("err = %+v", cliErr)
	}
	if n := len(srv.requests("/api/v1/packages")); n != 1 {
		t.Errorf("%d publish requests, want 1", n)
	}
}

func TestPackageRunWarnsWhatTheDeriverCouldNotRead(t *testing.T) {
	srv := newPackageServer(t, map[string]func(http.ResponseWriter, []byte){
		"/api/v1/packages/applies": answer(http.StatusOK, `{"applies": true, "reason": null, "unread": [{"url": "https://a/1", "reason": "timeout"}], "truncated": true}`),
	})
	_, errb := capture(t)
	if _, err := appliesFor(api.New(srv.URL, "tok"), testScope(t))(context.Background(), packages.URLs{Any: []string{"*x*"}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"could not read https://a/1 (timeout)", "profile was truncated"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr = %q, want %q", errb.String(), want)
		}
	}
}

func TestPackageRunRefusesAnUnreadableScopeFile(t *testing.T) {
	dir := writePackage(t)
	capture(t)
	cmd := newPackageRunCmd()
	cmd.SetArgs([]string{dir, "--dataset", "learn.concord.org/d", "--scope", filepath.Join(t.TempDir(), "missing.json")})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if cliErr := asCLIError(t, cmd.Execute()); cliErr.Code != "INVALID_SCOPE" || cliErr.ExitCode != output.ExitUsage {
		t.Errorf("err = %+v", cliErr)
	}
}

func TestPackagePublishMapsAuthAndUnansweredFailures(t *testing.T) {
	srv := newPackageServer(t, map[string]func(http.ResponseWriter, []byte){
		"/api/v1/packages": answer(http.StatusUnauthorized, `{"error":"NOT_AUTHENTICATED","message":"bad token"}`),
	})
	capture(t)
	err := (packagePublishFlags{}).run(context.Background(), api.New(srv.URL, "tok"), []byte("zip"))
	if cliErr := asCLIError(t, err); cliErr.ExitCode != output.ExitNotAuth {
		t.Errorf("NOT_AUTHENTICATED: %+v", cliErr)
	}

	dropped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	t.Cleanup(dropped.Close)
	err = (packagePublishFlags{}).run(context.Background(), api.New(dropped.URL, "tok"), []byte("zip"))
	if cliErr := asCLIError(t, err); !strings.Contains(cliErr.Action, "Publishing again is safe") {
		t.Errorf("unanswered: %+v", cliErr)
	}
}

func TestPackageRunValidatesThenRunsWithTheRunnersNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the entrypoint is a shell script")
	}
	dir := t.TempDir()
	manifest := strings.Replace(testManifest, `"entrypoint": "run.py"`, `"entrypoint": "run.sh"`, 1)
	script := "#!/bin/sh\nenv > \"$RD_OUTPUT_DIR/../env.txt\"\ncp \"$RD_SCOPE_FILE\" \"$RD_OUTPUT_DIR/../scope.json\"\necho ok > \"$RD_OUTPUT_DIR/display.md\"\n"
	for name, content := range map[string]string{"manifest.json": manifest, "run.sh": script} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scopePath := filepath.Join(t.TempDir(), "scope.json")
	if err := os.WriteFile(scopePath, []byte(`{"kind": "class", "id": "h",
	  "classes": [{"class_hash": "0123456789abcdef0123456789abcdef0123456789abcdef", "class_id": 6}], "assignments": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := packageRunFlags{scopePath: scopePath, environ: func() []string { return []string{"PATH=" + os.Getenv("PATH")} }}
	pkg, err := f.load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := newPackageServer(t, validateAnswering(validated))
	out, _ := capture(t)
	ref := dataset.Ref{Portal: config.MustPortal("learn.concord.org"), Name: "wildfire"}
	if err := f.run(context.Background(), api.New(srv.URL, "tok"), pkg, ref, "/data/root"); err != nil {
		t.Fatal(err)
	}

	reqs := srv.requests("/api/v1/packages/validate")
	if len(reqs) != 1 || reqs[0].contentType != "application/zip" || len(reqs[0].body) == 0 {
		t.Fatalf("validate requests = %d", len(reqs))
	}
	run := filepath.Join(dir, packages.RunDirName)
	env, err := os.ReadFile(filepath.Join(run, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RD_DATASET=learn.concord.org/wildfire\n", "CC_DATA_PORTAL=learn.concord.org\n", "CC_DATA_ROOT=/data/root\n", "RD_REPORT_SERVER_URL=" + srv.URL + "\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("environment lacks %q", strings.TrimSpace(want))
		}
	}
	var scope map[string]any
	raw, err := os.ReadFile(filepath.Join(run, "scope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scope); err != nil || scope["dataset"] != "wildfire" {
		t.Errorf("scope.json dataset = %v, %v", scope["dataset"], err)
	}
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	for _, key := range []string{"display", "display_bytes", "summary", "counts", "elapsed_seconds"} {
		if _, ok := line[key]; !ok {
			t.Errorf("result line lacks %s: %v", key, line)
		}
	}
}
