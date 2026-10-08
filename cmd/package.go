package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/packages"
	"github.com/spf13/cobra"
)

const packageLong = `Develop, test and publish a Researcher Dashboard package.

A package is a directory holding manifest.json and an entrypoint. init writes both; run
executes the package against your own dataset under the runner's rules; build zips it;
publish registers the zip in the catalog with your cc-data token.

What run cannot reproduce from the VM: the package runs as you, with no network sandbox,
and with HOME, PATH, the locale and any proxy settings passed through so its own cc-data
calls find your login and reach the server. A .py entrypoint runs with python3.11 when it is on PATH, as on the VM, else
python3.`

func newPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Develop, test and publish a Researcher Dashboard package",
		Long:  packageLong,
	}
	cmd.AddCommand(newPackageInitCmd(), newPackageRunCmd(), newPackageBuildCmd(), newPackagePublishCmd())
	return cmd
}

// packageDir is the optional [dir] argument, defaulting to the current directory.
func packageDir(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}

// loadPackage collects a package directory and validates its manifest against what it holds.
func loadPackage(dir string) (packages.Manifest, packages.Files, error) {
	files, err := packages.Collect(dir)
	if err != nil {
		return packages.Manifest{}, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_PACKAGE", Message: err.Error()}
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return packages.Manifest{}, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_MANIFEST", Message: fmt.Sprintf("no manifest.json in %s", dir)}
	}
	m, err := packages.ReadManifest(data, files.Set())
	if err != nil {
		return m, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_MANIFEST", Message: err.Error()}
	}
	if err := files.CheckEntrypointMode(m.Entrypoint); err != nil {
		return m, files, &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_PACKAGE", Message: err.Error()}
	}
	return m, files, nil
}

func newPackageInitCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Write a manifest.json skeleton and a run.py stub",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := packageDir(args)
			written, err := packages.Init(dir, name)
			if err != nil {
				return output.Usagef("%v", err)
			}
			return output.ResultLine(map[string]any{"dir": dir, "written": written})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the package name (default: the directory's name, when it fits ^[a-z0-9][a-z0-9-]{0,62}$)")
	return cmd
}

func newPackageRunCmd() *cobra.Command {
	var datasetRef, scopePath string
	cmd := &cobra.Command{
		Use:   "run [dir] --dataset <ref> --scope <file>",
		Short: "Run a package locally exactly as the dashboard's runner will",
		Long: "Run a package against your own dataset under the runner's rules: the same scope.json,\n" +
			"environment variables, output files, display.md cap and applicability check.\n\n" +
			"--scope names a file holding the scope's kind, id, classes and assignments.\n" +
			"Results are written under <dir>/.cc-data-run/, which build never ships.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if datasetRef == "" || scopePath == "" {
				return output.Usagef("--dataset and --scope are required")
			}
			dir := packageDir(args)
			m, files, err := loadPackage(dir)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(scopePath)
			if err != nil {
				return output.Usagef("reading the scope file: %v", err)
			}
			scope, err := packages.ParseLocalScope(raw)
			if err != nil {
				return &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_SCOPE", Message: err.Error()}
			}
			cfg, dataRoot, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveRef(cfg, datasetRef)
			if err != nil {
				return err
			}
			client, err := api.ForPortal(ref.Portal)
			if err != nil {
				return err
			}
			// The terminal's Ctrl-C cannot reach the package's own process group, so it cancels the run.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			archive, err := packages.Zip(files)
			if err != nil {
				return output.Internalf("%v", err)
			}
			// A version that is already published, or a portal that cannot store packages yet,
			// still runs; only a publish is refused.
			if _, err := validate(ctx, client, archive, "", false); err != nil {
				return err
			}
			start := time.Now()
			res, err := packages.Run(ctx, packages.RunOptions{
				Dir: dir, Manifest: m, Files: files, Scope: scope, Applies: appliesFor(client, scope),
				Dataset: ref.String(), Name: ref.Name, Portal: ref.Portal.Host(), DataRoot: dataRoot,
				ReportSrv: client.BaseURL, Stderr: output.Stderr(), Environ: os.Environ, LookPath: exec.LookPath,
			})
			if err != nil {
				return packageRunError(err)
			}
			return output.ResultLine(map[string]any{
				"display": res.DisplayPath, "display_bytes": res.DisplayBytes, "summary": res.Summary,
				"counts": res.Counts, "elapsed_seconds": int(time.Since(start).Seconds()),
			})
		},
	}
	cmd.Flags().StringVar(&datasetRef, "dataset", "", "the dataset the package pulls into and reads: <portal>/<name>")
	cmd.Flags().StringVar(&scopePath, "scope", "", "a JSON file holding the scope's kind, id, classes and assignments")
	return cmd
}

// derivationTimeout covers report-service's deriver, which may spend up to 240 seconds
// fetching a scope's activities; the client's 60-second default would cut it off.
const derivationTimeout = 5 * time.Minute

// validate asks report-server whether a publish of the archive would be accepted, and returns
// its answer. A report-server without the route is not a refusal: the archive is unchecked,
// the answer is nil, and saying so is the whole result.
func validate(ctx context.Context, client *api.Client, archive []byte, origin string, official bool) (*api.ValidatedPackage, error) {
	ans, err := client.ValidatePackage(ctx, archive, origin, official)
	switch {
	case api.RouteMissing(err):
		output.Warnf("report-server has no validate route yet, so this package is not checked against the catalog's rules until publish")
		return nil, nil
	case err != nil:
		return nil, api.AsCLIError(err)
	}
	return ans, nil
}

// appliesFor is package run's applicability check: report-server's one matcher, against the
// scope's assignment URLs and the interactive URLs report-service's deriver finds in them.
func appliesFor(client *api.Client, scope packages.LocalScope) func(context.Context, packages.URLs) (packages.Verdict, error) {
	return func(ctx context.Context, urls packages.URLs) (packages.Verdict, error) {
		long := *client
		long.RequestTimeout = derivationTimeout
		ans, err := long.PackageApplies(ctx, api.AppliesRequest{
			URLs:           map[string][]string{"all": urls.All, "any": urls.Any, "none": urls.None},
			AssignmentURLs: scope.AssignmentURLs(),
		})
		switch {
		case api.RouteMissing(err):
			return packages.Verdict{Unconfirmed: true}, nil
		case err != nil:
			return packages.Verdict{}, api.AsCLIError(err)
		}
		for _, u := range ans.Unread {
			output.Warnf("the class profile could not read %s (%s), so its interactives are not matched", u.URL, u.Reason)
		}
		if ans.Truncated {
			output.Warnf("the class profile was truncated, so not every interactive URL was matched")
		}
		return packages.Verdict{Applies: ans.Applies, Reason: ans.Reason}, nil
	}
}

func packageRunError(err error) error {
	var refused *packages.Refused
	var failed *packages.Failed
	var out *packages.OutputRefused
	var cliErr *output.CLIError
	switch {
	case errors.As(err, &cliErr):
		return cliErr
	case errors.As(err, &refused):
		return &output.CLIError{ExitCode: output.ExitInternal, Code: "PACKAGE_REFUSED", Message: err.Error()}
	case errors.As(err, &failed):
		return &output.CLIError{ExitCode: output.ExitInternal, Code: "PACKAGE_FAILED", Message: err.Error()}
	case errors.As(err, &out):
		return &output.CLIError{ExitCode: output.ExitInternal, Code: "PACKAGE_OUTPUT_REFUSED", Message: err.Error()}
	}
	return output.Internalf("%v", err)
}

type packageBuildFlags struct {
	out, portal, origin string
	official            bool
}

// run builds, validates and writes; it takes the client so it can be exercised against a test
// server without a stored credential.
func (f packageBuildFlags) run(ctx context.Context, client *api.Client, dir string) error {
	m, files, err := loadPackage(dir)
	if err != nil {
		return err
	}
	out := f.out
	if out == "" {
		// The manifest is unchecked until validate answers, and unchecked for good on a server
		// without the route, so its name and version must not be able to steer the path.
		name := fmt.Sprintf("%s-%s.zip", m.Name, m.Version)
		if strings.ContainsAny(name, `/\`) {
			return &output.CLIError{ExitCode: output.ExitUsage, Code: "INVALID_MANIFEST", Message: fmt.Sprintf("the manifest's name and version make %q, which is not a file name; fix them or pass --out", name)}
		}
		out = filepath.Join(dir, packages.BuildDirName, name)
	} else if err := packages.CheckOutsidePackage(dir, out); err != nil {
		return output.Usagef("%v", err)
	}
	archive, err := packages.Zip(files)
	if err != nil {
		return output.Internalf("%v", err)
	}
	ans, err := validate(ctx, client, archive, f.origin, f.official)
	if err != nil {
		return err
	}
	if ans != nil && ans.AlreadyPublished {
		output.Warnf("%s %s is already published; publish will refuse it until the version changes", ans.Identity, ans.Version)
	}
	if ans != nil && ans.PublishingUnavailable != nil {
		output.Warnf("this package cannot be published yet: %s", *ans.PublishingUnavailable)
	}
	if err := packages.WriteBuild(out, archive); err != nil {
		return output.Internalf("%v", err)
	}
	return output.ResultLine(map[string]any{
		"path": out, "checksum": packages.Checksum(archive), "bytes": len(archive), "files": len(files.Paths),
	})
}

func newPackageBuildCmd() *cobra.Command {
	var f packageBuildFlags
	cmd := &cobra.Command{
		Use:   "build [dir]",
		Short: "Zip a package reproducibly, check it against the catalog's rules and print its checksum",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := reportsClient(f.portal)
			if err != nil {
				return err
			}
			return f.run(context.Background(), client, packageDir(args))
		},
	}
	cmd.Flags().StringVar(&f.out, "out", "", "where to write the zip (default: <dir>/.cc-data-build/<name>-<version>.zip)")
	cmd.Flags().StringVar(&f.portal, "portal", "", "portal whose catalog rules to check against: an environment alias or a hostname")
	cmd.Flags().StringVar(&f.origin, "origin", "", "projects/<id>, when the package will be published as a project's")
	cmd.Flags().BoolVar(&f.official, "official", false, "check it as an official publish will be checked (publisher role only)")
	return cmd
}

type packagePublishFlags struct {
	portal, origin   string
	official, asJSON bool
}

// run publishes the archive; it takes the client so it can be exercised against a test server
// without a stored credential.
func (f packagePublishFlags) run(ctx context.Context, client *api.Client, archive []byte) error {
	got, err := client.PublishPackage(ctx, archive, f.origin, f.official)
	if err != nil {
		return api.AsWriteCLIError(err, "Publishing again is safe: ALREADY_EXISTS then means this attempt landed")
	}
	if want := packages.Checksum(archive); got.Checksum != want {
		return output.Internalf("report-server recorded checksum %s, but the archive sent is %s", got.Checksum, want)
	}
	if f.asJSON {
		return output.ResultLine(got)
	}
	fmt.Fprintf(output.Stdout(), "identity         %s\nversion          %s\nvisibility       %s\nofficial         %t\ncurrent version  %s\nchecksum         %s\n",
		got.Identity, got.Version, got.Visibility, got.Official, got.CurrentVersion, got.Checksum)
	return nil
}

func newPackagePublishCmd() *cobra.Command {
	var f packagePublishFlags
	cmd := &cobra.Command{
		Use:   "publish <zip> [--portal <portal|env>]",
		Short: "Publish a built package to the catalog with your cc-data token",
		Long: "POST the zip to report-server's catalog, which creates the package private on its first\n" +
			"publish and records the version. --origin projects/<id> publishes a project package;\n" +
			"--official needs the publisher role. The request is never retried.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			archive, err := os.ReadFile(args[0])
			if err != nil {
				return output.Usagef("reading %s: %v", args[0], err)
			}
			client, err := reportsClient(f.portal)
			if err != nil {
				return err
			}
			return f.run(context.Background(), client, archive)
		},
	}
	cmd.Flags().StringVar(&f.portal, "portal", "", "portal whose catalog to publish to: an environment alias or a hostname")
	cmd.Flags().StringVar(&f.origin, "origin", "", "projects/<id> to publish a project's package")
	cmd.Flags().BoolVar(&f.official, "official", false, "mark the package official (publisher role only)")
	cmd.Flags().BoolVar(&f.asJSON, "json", false, "emit the catalog's answer as JSON")
	return cmd
}
