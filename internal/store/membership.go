package store

import (
	"bufio"
	"bytes"
	"encoding/json"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

// appendMembershipLine writes the kind's identity columns in order as one JSON line,
// omitting an empty optional column, byte-identical to json.Encoder's output for answers
// and history.
func appendMembershipLine(buf *bytes.Buffer, k Kind, names [][]byte, id Identity) {
	buf.WriteByte('{')
	first := true
	for i, col := range k.Identity {
		if k.Optional[col] && id[i] == "" {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		value, _ := json.Marshal(id[i])
		buf.Write(names[i])
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteString("}\n")
}

// columnNames is the kind's identity column names, JSON-encoded once per file.
func columnNames(k Kind) [][]byte {
	names := make([][]byte, len(k.Identity))
	for i, col := range k.Identity {
		names[i], _ = json.Marshal(col)
	}
	return names
}

// WriteMembershipFile writes an identity set as JSONL atomically.
func WriteMembershipFile(path string, k Kind, ids []Identity) error {
	var buf bytes.Buffer
	names := columnNames(k)
	for _, id := range ids {
		appendMembershipLine(&buf, k, names, id)
	}
	return fsutil.WriteFileAtomic0600(path, buf.Bytes())
}

// ReadMembershipFile reads an identity set from a membership JSONL file.
func ReadMembershipFile(path string, k Kind) ([]Identity, error) {
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
		id, err := IdentityFromRecord(k, line)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, sc.Err()
}
