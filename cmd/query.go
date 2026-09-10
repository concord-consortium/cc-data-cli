package cmd

import (
	"context"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

// staticViewList renders the views every dataset gets, wrapped for help output. It is derived
// rather than written out so the help cannot drift from what the engine registers, which is the
// same reason the catalog and the researcher guide are checked against StaticViewNames.
func staticViewList() string {
	const width = 76
	var lines []string
	line := " "
	for i, name := range duck.StaticViewNames() {
		word := " " + name
		if i < len(duck.StaticViewNames())-1 {
			word += ","
		}
		if len(line)+len(word) > width {
			lines = append(lines, line)
			line = " "
		}
		line += word
	}
	return strings.Join(append(lines, line), "\n")
}

func newQueryCmd() *cobra.Command {
	var datasetRefs, allowDirs []string
	var format string
	cmd := &cobra.Command{
		Use:   "query --dataset <ref> \"SELECT ...\"",
		Short: "Run a SQL query over one or more datasets",
		Long: `Run a SQL query over one or more datasets via an ephemeral in-memory DuckDB.

Views (single dataset: unqualified; multiple: schema-qualified per dataset):
` + staticViewList() + `
  plus per-run views (report_<run>, answers_<run>, history_<run>).

The session is sandboxed to the named dataset folders. --allow-dir adds a
directory to the allowlist for this invocation only (never persisted), so joining
an external roster or rubric is an explicit, visible grant. Results go to stdout
in --format; all prose goes to stderr, and a failing query emits a single JSON
error object on stdout.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			specs, err := resolveDatasetSpecs(datasetRefs)
			if err != nil {
				return err
			}
			if !validFormat(format) {
				return output.Usagef("invalid --format %q (want table|csv|json|jsonl)", format)
			}
			e, err := duck.Open(context.Background(), specs, allowDirs, output.Stderr())
			if err != nil {
				return output.Internalf("%v", err)
			}
			defer e.Close()

			rows, err := e.Query(context.Background(), args[0])
			if err != nil {
				return &output.CLIError{ExitCode: output.ExitInternal, Code: "QUERY_ERROR", Message: err.Error()}
			}
			defer rows.Close()
			if err := duck.RenderRows(output.Stdout(), rows, format); err != nil {
				return &output.CLIError{ExitCode: output.ExitInternal, Code: "QUERY_ERROR", Message: err.Error()}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&datasetRefs, "dataset", nil, "dataset ref <portal>/<name> (repeatable; pre=<ref> to alias)")
	cmd.Flags().StringArrayVar(&allowDirs, "allow-dir", nil, "additional directory the query may read (repeatable)")
	cmd.Flags().StringVar(&format, "format", duck.FormatTable, "output format: table|csv|json|jsonl")
	return cmd
}

func validFormat(f string) bool {
	switch f {
	case duck.FormatTable, duck.FormatCSV, duck.FormatJSON, duck.FormatJSONL:
		return true
	}
	return false
}

// resolveDatasetSpecs parses repeatable --dataset values (with optional pre=
// aliases) into engine specs.
func resolveDatasetSpecs(refs []string) ([]duck.DatasetSpec, error) {
	if len(refs) == 0 {
		return nil, output.Usagef("--dataset is required")
	}
	cfg, root, err := loadRuntime()
	if err != nil {
		return nil, err
	}
	specs := make([]duck.DatasetSpec, 0, len(refs))
	for _, raw := range refs {
		alias := ""
		refStr := raw
		if i := strings.Index(raw, "="); i >= 0 {
			alias, refStr = raw[:i], raw[i+1:]
		}
		ref, err := resolveExistingRef(cfg, root, refStr)
		if err != nil {
			return nil, err
		}
		echoRef(ref)
		d := dataset.Open(root, ref)
		if !d.Exists() {
			return nil, notFound(ref)
		}
		specs = append(specs, duck.DatasetSpec{Alias: alias, DS: d})
	}
	return specs, nil
}
