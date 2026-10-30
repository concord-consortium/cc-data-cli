package dataset

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// storeAlternation is the registered store type names as a regexp alternation.
func storeAlternation() string {
	names := store.Names()
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return strings.Join(quoted, "|")
}

// storeFileRe matches a store version file, capturing its type and version.
func storeFileRe() *regexp.Regexp {
	return regexp.MustCompile(`^(` + storeAlternation() + `)\.v(\d+)\.jsonl$`)
}

// memberFileRe matches a membership version file, capturing its type, run and version.
func memberFileRe() *regexp.Regexp {
	return regexp.MustCompile(`^members_(` + storeAlternation() + `)_(\d+)\.v(\d+)\.jsonl$`)
}

// artifactRe matches every final-named artifact a fetch leaves in the dataset folder:
// store and membership versions and report CSVs.
func artifactRe() *regexp.Regexp {
	return regexp.MustCompile(`^((` + storeAlternation() + `)\.v\d+\.jsonl|members_.*\.v\d+\.jsonl|report_\d+.*\.csv)$`)
}

func storeFileName(typ string, version int) string {
	return fmt.Sprintf("%s.v%d.jsonl", typ, version)
}

func memberFileName(typ string, run, version int) string {
	return fmt.Sprintf("members_%s_%d.v%d.jsonl", typ, run, version)
}
