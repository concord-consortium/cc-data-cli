package packages

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed template/run.py
var runStub []byte

// Init writes manifest.json and run.py into dir, creating it if needed. It reads nothing in
// dir except to refuse when either file already exists, in which case it writes neither.
func Init(dir, name string) ([]string, error) {
	if name == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		name = filepath.Base(abs)
		if !ValidName(name) {
			return nil, fmt.Errorf("the directory name %q is not a package name; pass --name (^[a-z0-9][a-z0-9-]{0,62}$)", name)
		}
	} else if !ValidName(name) {
		return nil, fmt.Errorf("--name %q must match ^[a-z0-9][a-z0-9-]{0,62}$", name)
	}
	for _, f := range []string{"manifest.json", "run.py"} {
		if _, err := os.Lstat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s already exists in %s; init writes only into a package that has neither file", f, dir)
		}
	}
	manifest := Manifest{
		Name: name, Title: name, Version: "0.1.0",
		Description: "One line saying what this package shows.",
		URLs:        URLs{All: []string{}, Any: []string{}, None: []string{}},
		Entrypoint:  "run.py", ExpectedDurationSeconds: 300,
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "run.py"), runStub, 0o755); err != nil {
		return nil, err
	}
	return []string{"manifest.json", "run.py"}, nil
}
