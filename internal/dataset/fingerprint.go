package dataset

import (
	"fmt"
	"os"
	"path/filepath"
)

// FingerprintAbsent is recorded for a declared input that is not on disk. It
// cannot collide with a real fingerprint, which is always "<size>-<mtime>". A
// union view keeps declaring a CSV that has gone missing, so this is a normal
// state rather than an error, and recording it is what lets Fresh tell "absent
// then and absent now" from "absent then, present now".
const FingerprintAbsent = "absent"

// Fingerprint identifies a file's content cheaply, as size and modification
// time. This is what Go's own build cache uses. It cannot distinguish a rewrite
// that preserved both, which a real re-fetch does not do; a content hash would
// be exact and would cost a full read of every input, which at 15 GB defeats
// the purpose of materializing.
func Fingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return FingerprintAbsent
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// FingerprintInputs fingerprints each dataset-relative path, deduplicated, in
// the form Materialized.Inputs records and Fresh compares against.
func FingerprintInputs(dir string, files []string) map[string]string {
	out := make(map[string]string, len(files))
	for _, rel := range files {
		if _, seen := out[rel]; seen {
			continue
		}
		out[rel] = Fingerprint(filepath.Join(dir, rel))
	}
	return out
}

// Fresh reports whether the Parquet may be read: it was built from the view
// definition the caller now has, the view's current inputs are exactly the
// recorded ones, and each still fingerprints the same. The caller supplies the
// current list and signature because only the engine knows either.
//
// Comparing the set, not just the recorded entries, is load-bearing. A run
// downloaded since the materialization adds a file to a union view, and a
// Parquet built without it is stale even though every recorded input still
// matches perfectly.
//
// The signature is what the input fingerprints cannot see. A cc-data release
// that changes a view's SQL, or a reindex that re-stamps a zero FetchedAt and so
// reorders a dimension view's dedupe, both leave every input byte untouched
// while changing what the view returns.
func (mat Materialized) Fresh(dir string, current []string, signature string) bool {
	if mat.Signature != signature {
		return false
	}
	seen := make(map[string]bool, len(current))
	for _, rel := range current {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		recorded, ok := mat.Inputs[rel]
		if !ok || Fingerprint(filepath.Join(dir, rel)) != recorded {
			return false
		}
	}
	return len(seen) == len(mat.Inputs)
}
