package packages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const scopeJSON = `{
  "kind": "class",
  "id": "0123456789abcdef0123456789abcdef0123456789abcdef",
  "classes": [{"class_hash": "0123456789abcdef0123456789abcdef0123456789abcdef", "class_id": 6}],
  "assignments": [{"offering_id": 9, "runnable_id": 1234, "name": "Moth 1.2", "url": "https://activity-player.concord.org/?activity=1"}]
}`

// shellPackage writes a package whose entrypoint is run.sh with the given body.
func shellPackage(t *testing.T, body string, files map[string]string) (string, Files) {
	t.Helper()
	dir := t.TempDir()
	tree := map[string]string{"manifest.json": goodManifest, "run.sh": "#!/bin/sh\n" + body}
	for k, v := range files {
		tree[k] = v
	}
	writeTree(t, dir, tree)
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	collected, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, collected
}

// runOpts is a run of the shell package against a fixed ref, with the given environment and an
// Applies that must not be called unless the test sets one.
func runOpts(t *testing.T, dir string, files Files, environ []string, stderr *bytes.Buffer) RunOptions {
	t.Helper()
	scope, err := ParseLocalScope([]byte(scopeJSON))
	if err != nil {
		t.Fatal(err)
	}
	return RunOptions{
		Dir:      dir,
		Manifest: Manifest{Entrypoint: "run.sh", ExpectedDurationSeconds: 60},
		Files:    files,
		Scope:    scope,
		Applies: func(context.Context, URLs) (Verdict, error) {
			t.Error("Applies called for a package that declares no patterns")
			return Verdict{Applies: true}, nil
		},
		Dataset:   "learn.concord.org/wildfire",
		Name:      "wildfire",
		Portal:    "learn.concord.org",
		DataRoot:  "/data/root",
		ReportSrv: "https://report.example",
		Stderr:    stderr,
		Environ:   func() []string { return environ },
		LookPath:  exec.LookPath,
	}
}

func readEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

func TestLocalScopeRefusesTheRunnersKeys(t *testing.T) {
	withDataset := strings.Replace(scopeJSON, `"kind": "class",`, `"kind": "class", "dataset": "pkg-1",`, 1)
	if _, err := ParseLocalScope([]byte(withDataset)); err == nil {
		t.Error("a scope file setting dataset was accepted")
	}
	if _, err := ParseLocalScope([]byte(scopeJSON + ` {"dataset": "other"}`)); err == nil {
		t.Error("a scope file with a second JSON value after it was accepted")
	}
	if _, err := ParseLocalScope([]byte(scopeJSON + "\n")); err != nil {
		t.Errorf("a good scope file was refused: %v", err)
	}
}

func TestRunGivesThePackageTheRunnersLayout(t *testing.T) {
	skipOnWindows(t, "the entrypoint is a shell script")
	dir, files := shellPackage(t, `env > "$RD_OUTPUT_DIR/../env.txt"
cp "$RD_SCOPE_FILE" "$RD_OUTPUT_DIR/../scope.json"
ls -A > "$RD_OUTPUT_DIR/../ls.txt"
ls -l run.sh > "$RD_OUTPUT_DIR/../mode.txt"
echo ok > "$RD_OUTPUT_DIR/display.md"
`, map[string]string{"local-data/x.csv": "secret"})
	var stderr bytes.Buffer
	environ := []string{"PATH=" + os.Getenv("PATH"), "HOME=/home/r", "LC_ALL=C", "https_proxy=http://proxy:3128", "AWS_SECRET_ACCESS_KEY=leak"}
	res, err := Run(context.Background(), runOpts(t, dir, files, environ, &stderr))
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	run := filepath.Join(dir, RunDirName)
	if res.DisplayPath != filepath.Join(run, "out", "display.md") {
		t.Errorf("display path %s", res.DisplayPath)
	}

	env := readEnv(t, filepath.Join(run, "env.txt"))
	for k, want := range map[string]string{
		"RD_DATASET":           "learn.concord.org/wildfire",
		"CC_DATA_PORTAL":       "learn.concord.org",
		"CC_DATA_ROOT":         "/data/root",
		"CC_DATA_LOCAL":        filepath.Join(run, "data"),
		"RD_DATA_DIR":          filepath.Join(run, "data"),
		"RD_SCOPE_FILE":        filepath.Join(run, "in", "scope.json"),
		"RD_OUTPUT_DIR":        filepath.Join(run, "out"),
		"RD_REPORT_SERVER_URL": "https://report.example",
		"LC_ALL":               "C",
		"https_proxy":          "http://proxy:3128",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if _, ok := env["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Error("a variable outside the passthrough reached the package")
	}

	var scope map[string]any
	raw, err := os.ReadFile(filepath.Join(run, "scope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scope); err != nil {
		t.Fatal(err)
	}
	if scope["dataset"] != "wildfire" || scope["clue_source"] != "firebase" || scope["output_dir"] != filepath.Join(run, "out") {
		t.Errorf("scope.json = %v", scope)
	}

	ls, err := os.ReadFile(filepath.Join(run, "ls.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(ls)); strings.Join(got, ",") != "manifest.json,run.sh" {
		t.Errorf("working directory holds %v, want only what build ships", got)
	}
	mode, err := os.ReadFile(filepath.Join(run, "mode.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mode), "-rwxr-xr-x") {
		t.Errorf("staged run.sh mode: %s", mode)
	}
	if gi, err := os.ReadFile(filepath.Join(run, ".gitignore")); err != nil || string(gi) != "*\n" {
		t.Errorf(".gitignore = %q, %v", gi, err)
	}
}

func TestRunRefusesBeforeStarting(t *testing.T) {
	skipOnWindows(t, "the entrypoint is a shell script")
	dir, files := shellPackage(t, `touch "$RD_OUTPUT_DIR/../started"`+"\n", nil)
	var stderr bytes.Buffer
	o := runOpts(t, dir, files, nil, &stderr)
	o.Manifest.URLs = URLs{All: []string{"*//wildfire.concord.org/*"}}
	o.Applies = func(context.Context, URLs) (Verdict, error) {
		return Verdict{Reason: "no URL in this class matches the required pattern *//wildfire.concord.org/*"}, nil
	}
	_, err := Run(context.Background(), o)
	var refused *Refused
	if !errors.As(err, &refused) || err.Error() != "the package does not apply to this class: no URL in this class matches the required pattern *//wildfire.concord.org/*" {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, RunDirName, "started")); err == nil {
		t.Error("the entrypoint started for a package that does not apply")
	}

	o = runOpts(t, dir, files, nil, &stderr)
	o.Manifest.CluePrepull = true
	if _, err := Run(context.Background(), o); !errors.As(err, &refused) {
		t.Errorf("clue_prepull: err = %v, want Refused", err)
	}
}

func TestRunWarnsAndRunsWhenApplicabilityIsUnconfirmed(t *testing.T) {
	skipOnWindows(t, "the entrypoint is a shell script")
	dir, files := shellPackage(t, `echo ok > "$RD_OUTPUT_DIR/display.md"`+"\n", nil)
	var stderr bytes.Buffer
	o := runOpts(t, dir, files, []string{"PATH=" + os.Getenv("PATH")}, &stderr)
	o.Manifest.URLs = URLs{Any: []string{"*question-interactives/*"}}
	o.Applies = func(context.Context, URLs) (Verdict, error) { return Verdict{Unconfirmed: true}, nil }
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "cannot check applicability yet") {
		t.Errorf("stderr = %q, want the unconfirmed warning", stderr.String())
	}
}

func TestRunKillsAtTheBound(t *testing.T) {
	skipOnWindows(t, "the entrypoint is a shell script")
	dir, files := shellPackage(t, "sleep 30 &\nwait\n", nil)
	var stderr bytes.Buffer
	o := runOpts(t, dir, files, []string{"PATH=" + os.Getenv("PATH")}, &stderr)
	o.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := Run(context.Background(), o)
	var failed *Failed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "past its") || time.Since(start) > 5*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

func TestRunKillsTheGroupWhenInterrupted(t *testing.T) {
	skipOnWindows(t, "the entrypoint is a shell script")
	dir, files := shellPackage(t, "sleep 30 &\nwait\n", nil)
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := Run(ctx, runOpts(t, dir, files, []string{"PATH=" + os.Getenv("PATH")}, &stderr))
	var failed *Failed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "interrupted") || time.Since(start) > 5*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

func TestRunLeavesNothingBehind(t *testing.T) {
	skipOnWindows(t, "the entrypoint is a shell script")
	dir, files := shellPackage(t, `( sleep 1; echo late > "$RD_OUTPUT_DIR/late.txt" ) >/dev/null 2>&1 &
echo ok > "$RD_OUTPUT_DIR/display.md"
`, nil)
	var stderr bytes.Buffer
	if _, err := Run(context.Background(), runOpts(t, dir, files, []string{"PATH=" + os.Getenv("PATH")}, &stderr)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, RunDirName, "out", "late.txt")); err == nil {
		t.Error("a background process the package left behind wrote after the run")
	}
}

func TestRunRefusesALinkedRunDirectory(t *testing.T) {
	skipOnWindows(t, "links need privileges on Windows")
	dir, files := shellPackage(t, `echo ok > "$RD_OUTPUT_DIR/display.md"`+"\n", nil)
	elsewhere := t.TempDir()
	keep := filepath.Join(elsewhere, "out", "keep.txt")
	writeTree(t, elsewhere, map[string]string{"out/keep.txt": "keep"})
	if err := os.Symlink(elsewhere, filepath.Join(dir, RunDirName)); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if _, err := Run(context.Background(), runOpts(t, dir, files, nil, &stderr)); err == nil {
		t.Error("a linked run directory was used")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a file behind the link was removed: %v", err)
	}
}

func TestRunRefusesALinkedGitignore(t *testing.T) {
	skipOnWindows(t, "links need privileges on Windows")
	dir, files := shellPackage(t, `echo ok > "$RD_OUTPUT_DIR/display.md"`+"\n", nil)
	target := filepath.Join(t.TempDir(), "precious")
	writeTree(t, filepath.Dir(target), map[string]string{"precious": "keep"})
	writeTree(t, dir, map[string]string{RunDirName + "/": ""})
	if err := os.Symlink(target, filepath.Join(dir, RunDirName, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if _, err := Run(context.Background(), runOpts(t, dir, files, nil, &stderr)); err == nil {
		t.Error("a linked .gitignore was written through")
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Errorf("the link's target became %q", got)
	}
}

func TestWriteBuildRefusesALinkedBuildDirectory(t *testing.T) {
	skipOnWindows(t, "links need privileges on Windows")
	dir := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(dir, BuildDirName)); err != nil {
		t.Fatal(err)
	}
	if err := WriteBuild(filepath.Join(dir, BuildDirName, "p-0.1.0.zip"), []byte("zip")); err == nil {
		t.Error("a linked build directory was written into")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("files written behind the link: %v", entries)
	}
}

func TestEntrypointFallsBackToPython3WithANote(t *testing.T) {
	look := func(have ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, h := range have {
				if h == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	cmd, _, note, err := entrypointCommand("/pkg", "run.py", look("python3.11", "python3"))
	if err != nil || cmd != "/usr/bin/python3.11" || note != "" {
		t.Errorf("with python3.11: %s, %q, %v", cmd, note, err)
	}
	cmd, _, note, err = entrypointCommand("/pkg", "run.py", look("python3"))
	if err != nil || cmd != "/usr/bin/python3" || !strings.Contains(note, "python3.11") {
		t.Errorf("without python3.11: %s, %q, %v", cmd, note, err)
	}
	if _, _, _, err := entrypointCommand("/pkg", "run.py", look()); err == nil {
		t.Error("no interpreter was not an error")
	}
}

func TestWriteBuildReplacesALinkedZipRatherThanFollowingIt(t *testing.T) {
	skipOnWindows(t, "links need privileges on Windows")
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "precious")
	writeTree(t, filepath.Dir(target), map[string]string{"precious": "keep"})
	writeTree(t, dir, map[string]string{BuildDirName + "/": ""})
	out := filepath.Join(dir, BuildDirName, "p-0.1.0.zip")
	if err := os.Symlink(target, out); err != nil {
		t.Fatal(err)
	}
	if err := WriteBuild(out, []byte("zip")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Errorf("the link's target became %q", got)
	}
	if info, err := os.Lstat(out); err != nil || !info.Mode().IsRegular() {
		t.Errorf("the zip is not a regular file: %v, %v", info, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, BuildDirName)); len(entries) != 2 {
		t.Errorf("build directory holds %v, want the zip and .gitignore only", entries)
	}
}
