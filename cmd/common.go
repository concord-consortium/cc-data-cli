package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

// loadRuntime returns the loaded config and resolved data root.
func loadRuntime() (*config.Config, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	root, err := cfg.DataRootDir()
	if err != nil {
		return nil, "", err
	}
	return cfg, root, nil
}

// resolveRef parses a dataset ref against the configured default portal. Only
// create uses it: a new dataset must name a portal the parser fully accepts.
func resolveRef(cfg *config.Config, raw string) (dataset.Ref, error) {
	ref, err := dataset.ParseRefForConfig(cfg, raw)
	if err != nil {
		return dataset.Ref{}, output.Usagef("%v", err)
	}
	return ref, nil
}

// resolveExistingRef is resolveRef for every command that names a dataset
// already on disk (show, delete, purge, rename, edit, reindex, get, query). It
// must reach anything dataset list shows, including a folder under a portal the
// parser would otherwise refuse, so the fix for such a folder is not rm -rf.
func resolveExistingRef(cfg *config.Config, dataRoot, raw string) (dataset.Ref, error) {
	ref, err := dataset.ParseRefForExisting(cfg, dataRoot, raw)
	if err != nil {
		return dataset.Ref{}, output.Usagef("%v", err)
	}
	return ref, nil
}

// echoRef writes the resolved ref to stderr, as every dataset command does.
func echoRef(ref dataset.Ref) {
	output.Progressf("dataset: %s", ref)
}

// confirm prompts on stderr for a y/N answer, defaulting to no.
func confirm(prompt string) bool {
	fmt.Fprintf(output.Stderr(), "%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
