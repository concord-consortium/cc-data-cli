package packages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

// TimeoutMargin is what the runner adds to expected_duration_seconds before it kills a run.
const TimeoutMargin = 10 * time.Minute

// RunDirName is the working tree package run keeps inside the package directory. It starts
// with "." so Collect, and so build, never ships it.
const RunDirName = ".cc-data-run"

// passthrough is what a laptop adds so the package's own cc-data calls find the stored
// credential, reach the server through any proxy, and run at all. These names match in any
// case, so the lower-case proxy spellings pass; the LC_ prefix matches as written.
var passthrough = []string{
	"HOME", "PATH", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR",
	"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
	// Windows has no release, but CI builds and tests there.
	"SYSTEMROOT", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATHEXT", "COMSPEC",
}

// Verdict is report-server's answer to whether a package's patterns match a scope. Unconfirmed
// means the report-server answering has no applicability route yet, so nothing was checked.
type Verdict struct {
	Applies     bool
	Reason      string
	Unconfirmed bool
}

// Refused is a package the runner would not start: it does not apply, or it needs the CLUE
// pre-pull, which cc-data cannot do.
type Refused struct{ Reason string }

func (e *Refused) Error() string { return e.Reason }

// Failed is a package that started and did not finish cleanly.
type Failed struct{ Reason string }

func (e *Failed) Error() string { return e.Reason }

// RunOptions is everything package run decides before the entrypoint starts.
type RunOptions struct {
	Dir       string                                       // the package directory
	Manifest  Manifest                                     // validated against Files
	Files     Files                                        // what build would ship
	Scope     LocalScope                                   // the author's scope file
	Applies   func(context.Context, URLs) (Verdict, error) // report-server's applicability answer
	Dataset   string                                       // the full ref, <portal>/<name>
	Name      string                                       // the ref's bare name
	Portal    string                                       // the ref's portal host
	DataRoot  string                                       // CC_DATA_ROOT, absolute
	ReportSrv string                                       // the stored credential's server, or ""
	BinDir    string                                       // put first on PATH, so the package calls this cc-data
	Stderr    io.Writer                                    // where the package's own output goes
	Environ   func() []string
	LookPath  func(string) (string, error)
	Timeout   time.Duration // zero means expected_duration_seconds + TimeoutMargin
}

// Run prepares the runner's layout, runs the entrypoint and reads its result.
func Run(ctx context.Context, o RunOptions) (Result, error) {
	if o.Manifest.CluePrepull {
		return Result{}, &Refused{Reason: "the package sets clue_prepull, and cc-data cannot fetch CLUE data yet, so it runs only on the VM"}
	}
	if o.Manifest.URLs.DeclaresPatterns() {
		v, err := o.Applies(ctx, o.Manifest.URLs)
		switch {
		case err != nil:
			return Result{}, err
		case v.Unconfirmed:
			fmt.Fprintln(o.Stderr, "warning: report-server cannot check applicability yet; the package runs, and the VM's runner is the check")
		case !v.Applies:
			return Result{}, &Refused{Reason: "the package does not apply to this class: " + v.Reason}
		}
	}
	run := filepath.Join(o.Dir, RunDirName)
	paths, err := prepare(run, o.Files)
	if err != nil {
		return Result{}, err
	}
	scope := ScopeFile{LocalScope: o.Scope, ClueSource: "firebase", Dataset: o.Name, OutputDir: paths.out}
	b, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return Result{}, err
	}
	scopePath := filepath.Join(paths.in, "scope.json")
	if err := fsutil.WriteFileAtomic0600(scopePath, b); err != nil {
		return Result{}, err
	}

	env := map[string]string{
		"CC_DATA_ROOT":   o.DataRoot,
		"CC_DATA_PORTAL": o.Portal,
		"CC_DATA_LOCAL":  paths.data,
		"RD_DATASET":     o.Dataset,
		"RD_SCOPE_FILE":  scopePath,
		"RD_DATA_DIR":    paths.data,
		"RD_OUTPUT_DIR":  paths.out,
	}
	if o.ReportSrv != "" {
		env["RD_REPORT_SERVER_URL"] = o.ReportSrv
	}
	command, args, note, err := entrypointCommand(paths.pkg, o.Manifest.Entrypoint, o.LookPath)
	if err != nil {
		return Result{}, err
	}
	if note != "" {
		fmt.Fprintln(o.Stderr, note)
	}

	timeout := o.Timeout
	if timeout == 0 {
		timeout = time.Duration(o.Manifest.ExpectedDurationSeconds)*time.Second + TimeoutMargin
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = paths.pkg
	cmd.Env = environment(o.Environ(), env, o.BinDir)
	cmd.Stdout = o.Stderr
	cmd.Stderr = o.Stderr
	killGroup(cmd)
	err = cmd.Run()
	// On Unix, whatever is left in the package's process group dies before its output is read.
	reapGroup(cmd)
	if err != nil {
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			return Result{}, &Failed{Reason: fmt.Sprintf("the package ran past its %ds bound", int(timeout.Seconds()))}
		case ctx.Err() != nil:
			return Result{}, &Failed{Reason: "the package was interrupted"}
		}
		return Result{}, &Failed{Reason: fmt.Sprintf("the package exited unsuccessfully: %v", err)}
	}
	return ReadResult(paths.out)
}

type runPaths struct{ pkg, in, out, data string }

// prepare empties and rebuilds the run tree, keeping only data/ from one run to the next. Every
// directory it removes or creates under the package must be a real directory, so a link left
// there can never aim the removal outside.
func prepare(run string, files Files) (runPaths, error) {
	p := runPaths{
		pkg:  filepath.Join(run, "pkg"),
		in:   filepath.Join(run, "in"),
		out:  filepath.Join(run, "out"),
		data: filepath.Join(run, "data"),
	}
	if err := ensureRealDir(run); err != nil {
		return p, err
	}
	if err := ignoreAll(run); err != nil {
		return p, err
	}
	for _, d := range []string{p.pkg, p.in, p.out} {
		if _, err := os.Lstat(d); err == nil {
			if err := RealDir(d); err != nil {
				return p, fmt.Errorf("refusing to empty %s: it is a link or not a directory", d)
			}
			if err := os.RemoveAll(d); err != nil {
				return p, err
			}
		}
		if err := os.Mkdir(d, 0o700); err != nil {
			return p, err
		}
	}
	if err := ensureRealDir(p.data); err != nil {
		return p, err
	}
	abs, err := filepath.Abs(run)
	if err != nil {
		return p, err
	}
	p = runPaths{filepath.Join(abs, "pkg"), filepath.Join(abs, "in"), filepath.Join(abs, "out"), filepath.Join(abs, "data")}
	for _, rel := range files.Paths {
		if err := copyFile(filepath.Join(files.Dir, filepath.FromSlash(rel)), filepath.Join(p.pkg, filepath.FromSlash(rel)), files.Modes[rel]); err != nil {
			return p, err
		}
	}
	return p, nil
}

// ignoreAll keeps git out of a directory this tool makes inside a package, whose files can
// hold student data.
func ignoreAll(dir string) error {
	p := filepath.Join(dir, ".gitignore")
	if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to write %s: it is a link or not a file", p)
	}
	return fsutil.WriteFileAtomic0600(p, []byte("*\n"))
}

// ensureRealDir creates d, or checks that it is a real directory, and keeps it private: what
// this tool writes inside a package can hold student data.
func ensureRealDir(d string) error {
	if _, err := os.Lstat(d); errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(d, 0o700)
	}
	if err := RealDir(d); err != nil {
		return fmt.Errorf("refusing to use %s: it is a link or not a directory", d)
	}
	return os.Chmod(d, 0o700)
}

// copyFile stages one file with the mode build gives it, so the staged package is the one
// unzip produces on the VM.
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// entrypointCommand is the runner's choice of interpreter: python3.11 for a .py entrypoint,
// otherwise the entrypoint itself. A laptop without python3.11 falls back to python3, saying so.
func entrypointCommand(pkgDir, entrypoint string, lookPath func(string) (string, error)) (string, []string, string, error) {
	path := filepath.Join(pkgDir, filepath.FromSlash(entrypoint))
	if !strings.HasSuffix(entrypoint, ".py") {
		return path, nil, "", nil
	}
	if p, err := lookPath("python3.11"); err == nil {
		return p, []string{path}, "", nil
	}
	if p, err := lookPath("python3"); err == nil {
		return p, []string{path}, "note: python3.11, the VM's interpreter, is not on PATH; running with python3", nil
	}
	return "", nil, "", fmt.Errorf("neither python3.11 nor python3 is on PATH")
}

// environment is the passthrough taken from the caller's environment (LC_* included), then
// the runner's variables over it; nothing else is inherited.
func environment(from []string, set map[string]string, binDir string) []string {
	keep := map[string]string{}
	for _, kv := range from {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(k, "LC_") || slices.Contains(passthrough, strings.ToUpper(k)) {
			keep[k] = v
		}
	}
	for k, v := range set {
		keep[k] = v
	}
	if binDir != "" {
		pathKey := "PATH"
		for k := range keep {
			if strings.EqualFold(k, "PATH") {
				pathKey = k
			}
		}
		if keep[pathKey] == "" {
			keep[pathKey] = binDir
		} else {
			keep[pathKey] = binDir + string(os.PathListSeparator) + keep[pathKey]
		}
	}
	out := make([]string, 0, len(keep))
	for k, v := range keep {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
