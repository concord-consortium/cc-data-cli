// Package dataset owns dataset refs, the manifest schema, and the dataset CRUD
// operations.
package dataset

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/config"
)

var nameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// reservedSchemaNames are DuckDB reserved/built-in schema names a dataset must
// not use, since each dataset becomes a schema in multi-dataset queries.
var reservedSchemaNames = map[string]bool{
	"main":               true,
	"temp":               true,
	"system":             true,
	"information_schema": true,
	"pg_catalog":         true,
}

// Ref is a resolved dataset reference.
type Ref struct {
	Portal string // real host
	Name   string
}

func (r Ref) String() string { return r.Portal + "/" + r.Name }

// ParseRef resolves "<portal>/<name>" or a bare "<name>" (under defaultPortal).
// The name is not validated here beyond splitting; callers that create datasets
// validate it with ValidateName.
func ParseRef(raw, defaultPortal string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, fmt.Errorf("dataset ref is empty")
	}
	// Strip an optional scheme so the first-slash split names the dataset, not
	// the "//" of a scheme.
	var scheme string
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme, raw = raw[:i+3], raw[i+3:]
	}
	var portalPart, name string
	if i := strings.Index(raw, "/"); i >= 0 {
		portalPart, name = raw[:i], raw[i+1:]
	} else {
		name = raw
		if defaultPortal == "" {
			return Ref{}, fmt.Errorf("no portal in ref %q and no default_portal configured", raw)
		}
		portalPart = defaultPortal
	}
	host, err := config.NormalizePortal(scheme + portalPart)
	if err != nil {
		return Ref{}, err
	}
	if name == "" {
		return Ref{}, fmt.Errorf("dataset ref %q has no name", raw)
	}
	return Ref{Portal: host, Name: name}, nil
}

// Dir returns the dataset directory under a data root, using the filesystem
// folder encoding for the portal host.
func (r Ref) Dir(dataRoot string) string {
	return filepath.Join(dataRoot, config.PortalFolder(r.Portal), "datasets", r.Name)
}

// PortalDatasetsDir returns the datasets directory for a portal under a data root.
func PortalDatasetsDir(dataRoot, portalHost string) string {
	return filepath.Join(dataRoot, config.PortalFolder(portalHost), "datasets")
}

// ValidateName enforces the dataset name alphabet and the reserved-schema
// exclusions; used by create and rename.
func ValidateName(name string) error {
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("invalid dataset name %q: must match ^[a-z0-9][a-z0-9_-]{0,62}$", name)
	}
	if reservedSchemaNames[name] {
		return fmt.Errorf("dataset name %q is a reserved DuckDB schema name", name)
	}
	return nil
}
