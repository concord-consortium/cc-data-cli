package store

import (
	"bufio"
	"bytes"
	"encoding/json"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

// WriteMembershipFile writes an identity set as JSONL atomically.
func WriteMembershipFile(path string, ids []Identity) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, id := range ids {
		if err := enc.Encode(id); err != nil {
			return err
		}
	}
	return fsutil.WriteFileAtomic0600(path, buf.Bytes())
}

// ReadMembershipFile reads an identity set from a membership JSONL file.
func ReadMembershipFile(path string) ([]Identity, error) {
	f, err := fsutil.OpenReplaceTarget(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var ids []Identity
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var id Identity
		if err := json.Unmarshal(line, &id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, sc.Err()
}
