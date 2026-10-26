package fetch

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// ownerKind reads a CLUE record's scope owner.
var ownerKind = store.Kind{Name: "owner", Identity: []string{"context_id", "uid"}, Strict: true}

// coverageWalkHook runs inside the coverage walk; tests use it to observe the lock.
var coverageWalkHook = func() {}

// ownerCoverage counts the run's distinct (context_id, uid) owners, read from the kind's
// owner store. It walks under the dataset lock so another run's merge cannot delete the
// store version the manifest names. When owners cannot be counted, the note says why.
func (opts PagedOptions) ownerCoverage(kind store.Kind, totalEndpoints *int) (*dataset.Coverage, string) {
	lock := opts.DS.Lock()
	if err := lock.Lock(); err != nil {
		return nil, "owners not counted: " + err.Error()
	}
	defer lock.Unlock()

	m, err := opts.DS.ReadManifest()
	if err != nil {
		return nil, "owners not counted: " + err.Error()
	}
	ref, ok := m.Membership[dataset.MembershipKey(opts.Type, opts.RunID)]
	if !ok {
		return nil, fmt.Sprintf("owners not counted: run %d has no %s membership", opts.RunID, opts.Type)
	}
	ownerStore := kind.OwnerStore
	note := ""
	if ownerStore != kind.Name {
		note = fmt.Sprintf("owners not counted: fetch run %d's %s, then its %s again", opts.RunID, ownerStore, kind.Name)
	}
	owners, resolved, err := walkOwners(opts.DS.Path(ref.File), kind, m.Stores[ownerStore].File, opts.DS, store.MustKind(ownerStore))
	if err != nil {
		return nil, fmt.Sprintf("owners not counted: %s %v", ownerStore, err)
	}
	if !resolved {
		return nil, note
	}
	cov := &dataset.Coverage{WithData: owners}
	if totalEndpoints != nil {
		q := *totalEndpoints
		empty := q - owners
		cov.Queried, cov.Empty = &q, &empty
	}
	return cov, ""
}

// walkOwners counts the distinct owners of the membership's records, reading each from
// the owner store's record with the same doc_id. resolved is false when a member has no
// record there, including when the owner store does not exist yet.
func walkOwners(memberPath string, memberKind store.Kind, storeFile string, ds *dataset.Dataset, storeKind store.Kind) (owners int, resolved bool, err error) {
	members, err := store.OpenMembership(memberPath, memberKind)
	if err != nil {
		return 0, false, err
	}
	defer members.Close()
	first, has, err := members.Next()
	if err != nil {
		return 0, false, err
	}
	if !has {
		return 0, true, nil
	}
	if storeFile == "" {
		return 0, false, nil
	}
	f, err := os.Open(ds.Path(storeFile))
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	coverageWalkHook()
	r := bufio.NewReader(f)

	seen := map[string]bool{}
	var storeKey string
	var storeRec []byte
	storeOK := true
	nextStore := func() error {
		for {
			line, rerr := r.ReadBytes('\n')
			if line = bytes.TrimSpace(line); len(line) > 0 {
				id, ierr := store.IdentityFromRecord(storeKind, line)
				if ierr != nil {
					return ierr
				}
				storeKey, storeRec = storeKind.Key(id), line
				return nil
			}
			if rerr == io.EOF {
				storeOK = false
				return nil
			}
			if rerr != nil {
				return rerr
			}
		}
	}
	if err := nextStore(); err != nil {
		return 0, false, err
	}
	lastDoc := ""
	for key, ok := first, true; ; key, ok, err = members.Next() {
		if err != nil {
			return 0, false, err
		}
		if !ok {
			return len(seen), true, nil
		}
		docKey := storeKind.Key(store.Identity{memberKind.Field(memberKind.Decode(key), "doc_id")})
		if docKey == lastDoc {
			continue
		}
		lastDoc = docKey
		for storeOK && storeKey < docKey {
			if err := nextStore(); err != nil {
				return 0, false, err
			}
		}
		if !storeOK || storeKey != docKey {
			return 0, false, nil
		}
		owner, err := store.IdentityFromRecord(ownerKind, storeRec)
		if err != nil {
			return 0, false, fmt.Errorf("document %s's %w", memberKind.Field(memberKind.Decode(key), "doc_id"), err)
		}
		seen[ownerKind.Key(owner)] = true
	}
}
