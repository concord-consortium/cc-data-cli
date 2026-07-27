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
	Portal config.Portal
	Name   string
}

func (r Ref) String() string { return r.Portal.Host() + "/" + r.Name }

// ParseRefForConfig resolves a ref against the configured default portal. Every
// caller goes through this rather than reading cfg.DefaultPortal directly, so
// the raw config string never reaches a Ref unparsed.
func ParseRefForConfig(cfg *config.Config, raw string) (Ref, error) {
	defaultPortal, err := cfg.DefaultPortalValue()
	if err != nil {
		return Ref{}, err
	}
	return ParseRef(raw, defaultPortal)
}

// ParseRefForExisting resolves a ref for any command that names a dataset
// already on disk (everything but create), making one exception ParseRefForConfig
// does not: a portal ParsePortalIdentity refuses is accepted when a dataset
// actually exists under it.
//
// dataset list builds its rows from folder names and never parses them, so
// without this a folder an earlier, laxer build wrote could be visible and
// untouchable, removable only with rm -rf. That covers two kinds of stranded
// folder: an environment-alias name, and a host shape 0.1.0's NormalizePortal
// accepted but this build rejects (an underscore, an IPv6 literal). Both route
// through config.AdoptExistingPortal, which refuses anything that is not a single
// safe path component, so a traversal can never reach os.RemoveAll by claiming
// the folder exists. When no folder exists the original strict refusal stands,
// since for a genuinely new dataset the fix is to name the hostname.
func ParseRefForExisting(cfg *config.Config, dataRoot, raw string) (Ref, error) {
	defaultPortal, err := cfg.DefaultPortalValue()
	if err != nil {
		return Ref{}, err
	}
	portalValue, name, err := splitRef(raw, defaultPortal)
	if err != nil {
		return Ref{}, err
	}
	portal, portalErr := config.ParsePortalIdentity(portalValue)
	if portalErr == nil {
		return buildRef(portal, name, raw)
	}
	adopted, ok := config.AdoptExistingPortal(portalValue)
	if !ok {
		return Ref{}, portalErr
	}
	// Validate the name before consulting the disk, so an invalid name still
	// reports as one rather than as the portal refusal. If the name is fine but no
	// folder exists, the strict refusal stands.
	ref, err := buildRef(adopted, name, raw)
	if err != nil {
		return Ref{}, err
	}
	if !Open(dataRoot, ref).Exists() {
		return Ref{}, portalErr
	}
	return ref, nil
}

// ParseRef resolves "<portal>/<name>" or a bare "<name>" (under defaultPortal).
// Neither half can carry path-traversal segments into filesystem sinks
// (os.RemoveAll, MkdirAll) or MCP dataset_create: the name is validated with
// ValidateName, and the portal by config.ParsePortalIdentity, which is also what
// refuses an environment alias here.
func ParseRef(raw string, defaultPortal config.Portal) (Ref, error) {
	portalValue, name, err := splitRef(raw, defaultPortal)
	if err != nil {
		return Ref{}, err
	}
	portal, err := config.ParsePortalIdentity(portalValue)
	if err != nil {
		return Ref{}, err
	}
	return buildRef(portal, name, raw)
}

// splitRef splits a ref into the portal value to parse and the dataset name,
// without deciding whether either is acceptable. ParseRefForExisting reuses it
// to recover the name after the portal has been refused.
func splitRef(raw string, defaultPortal config.Portal) (portalValue, name string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("dataset ref is empty")
	}
	// Strip an optional scheme so the first-slash split names the dataset, not
	// the "//" of a scheme.
	var scheme string
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme, raw = raw[:i+3], raw[i+3:]
	}
	var portalPart string
	if i := strings.Index(raw, "/"); i >= 0 {
		portalPart, name = raw[:i], raw[i+1:]
	} else {
		name = raw
		if defaultPortal.IsZero() {
			return "", "", fmt.Errorf("no portal in ref %q and no default_portal configured", raw)
		}
		portalPart = defaultPortal.Host()
	}
	return scheme + portalPart, name, nil
}

// buildRef validates both halves and assembles the ref. It is the single choke
// point every Ref passes through, so it also rejects a zero portal: a Ref{}
// portal would make Ref.Dir resolve to <root>/datasets/<name>, a location
// dataset list never scans. Every caller supplies a parsed or adopted portal, so
// this only fires on a programming error.
func buildRef(portal config.Portal, name, raw string) (Ref, error) {
	if portal.IsZero() {
		return Ref{}, fmt.Errorf("dataset ref %q has no portal", raw)
	}
	if name == "" {
		return Ref{}, fmt.Errorf("dataset ref %q has no name", raw)
	}
	if err := ValidateName(name); err != nil {
		return Ref{}, err
	}
	return Ref{Portal: portal, Name: name}, nil
}

// Dir returns the dataset directory under a data root, using the filesystem
// folder encoding for the portal host.
func (r Ref) Dir(dataRoot string) string {
	return filepath.Join(dataRoot, r.Portal.Folder(), "datasets", r.Name)
}

// PortalDatasetsDir returns the datasets directory for a portal under a data root.
func PortalDatasetsDir(dataRoot string, portal config.Portal) string {
	return filepath.Join(dataRoot, portal.Folder(), "datasets")
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
