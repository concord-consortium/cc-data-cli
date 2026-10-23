package dataset

import (
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

func TestStoreFileNamesRoundTripForEveryKind(t *testing.T) {
	for _, name := range store.Names() {
		storeFile := storeFileName(name, 7)
		if m := reindexStoreRe.FindStringSubmatch(storeFile); m == nil || m[1] != name || m[2] != "7" {
			t.Fatalf("store file %q parsed as %q", storeFile, m)
		}
		if m := storeVersionRe.FindStringSubmatch(storeFile); m == nil || m[1] != name {
			t.Fatalf("store file %q not matched by the cleanup pattern", storeFile)
		}
		memberFile := memberFileName(name, 584, 7)
		if m := reindexMemberRe.FindStringSubmatch(memberFile); m == nil || m[1] != name || m[2] != "584" || m[3] != "7" {
			t.Fatalf("membership file %q parsed as %q", memberFile, m)
		}
		for _, f := range []string{storeFile, memberFile} {
			if !isArtifactFile(f) || !finalArtifactRe.MatchString(f) {
				t.Fatalf("%q is not recognized as an artifact", f)
			}
		}
	}
	for _, name := range store.Names() {
		prefixed := "x" + name + ".v1.jsonl"
		if reindexStoreRe.MatchString(prefixed) || storeVersionRe.MatchString(prefixed) || isArtifactFile(prefixed) {
			t.Fatalf("%q matched a pattern that must be anchored", prefixed)
		}
		if reindexMemberRe.MatchString("xmembers_" + name + "_1.v1.jsonl") {
			t.Fatalf("membership pattern is not anchored")
		}
	}
	if isArtifactFile("notes.v1.jsonl") {
		t.Fatal("an unregistered stem must not be an artifact")
	}
}
