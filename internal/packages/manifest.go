// Package packages is the local side of a Researcher Dashboard package: reading the manifest
// fields the runner acts on, collecting and zipping the package, staging and running it the
// way the runner does, and reading the result files. The package rules themselves, and
// applicability, are report-server's, which cc-data asks rather than copies.
package packages

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName reports whether name fits the catalog's name grammar, which init needs before
// report-server has seen anything.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// URLs is a manifest's applicability patterns, passed to report-server as written.
type URLs struct {
	All  []string `json:"all"`
	Any  []string `json:"any"`
	None []string `json:"none"`
}

// DeclaresPatterns reports whether any sense holds a pattern; a package that declares none
// applies to every scope.
func (u URLs) DeclaresPatterns() bool {
	return len(u.All)+len(u.Any)+len(u.None) > 0
}

// Manifest is the part of manifest.json the runner acts on. Everything else, and every limit,
// is report-server's to check (cmd's validate call).
type Manifest struct {
	Name                    string `json:"name"`
	Title                   string `json:"title"`
	Version                 string `json:"version"`
	Description             string `json:"description,omitempty"`
	URLs                    URLs   `json:"urls"`
	CluePrepull             bool   `json:"clue_prepull"`
	Entrypoint              string `json:"entrypoint"`
	ExpectedDurationSeconds int    `json:"expected_duration_seconds"`
}

// ReadManifest reads the fields a run acts on, refusing an entrypoint outside files and a
// duration that is not a positive integer; every other rule is report-server's.
func ReadManifest(data []byte, files map[string]bool) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest.json: %v", err)
	}
	if !files[m.Entrypoint] || unsafePath(m.Entrypoint) {
		return m, fmt.Errorf("manifest.json: entrypoint %q is not a file in the package", m.Entrypoint)
	}
	if m.ExpectedDurationSeconds < 1 {
		return m, fmt.Errorf("manifest.json: expected_duration_seconds must be a positive integer")
	}
	return m, nil
}

// unsafePath is report-server's Archive.unsafe_path?: absolute, a drive letter, or a ".." segment.
func unsafePath(name string) bool {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return true
	}
	if len(name) >= 2 && name[1] == ':' && unicode.IsLetter(rune(name[0])) {
		return true
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}
