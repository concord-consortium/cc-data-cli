package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

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

// WriteMembership streams the index's identities to path as key-ordered JSONL,
// atomically.
func (idx *SegmentIndex) WriteMembership(path string) error {
	names := columnNames(idx.kind)
	return fsutil.WriteAtomic0600(path, func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 64*1024)
		var line bytes.Buffer
		for _, e := range idx.entries {
			line.Reset()
			appendMembershipLine(&line, idx.kind, names, idx.kind.Decode(e.key))
			if _, err := bw.Write(line.Bytes()); err != nil {
				return err
			}
		}
		return bw.Flush()
	})
}

// WriteMembershipWith streams the union of the index's identities and prev's to path
// as key-ordered JSONL, atomically, each key once.
func (idx *SegmentIndex) WriteMembershipWith(path string, prev *MembershipReader) error {
	names := columnNames(idx.kind)
	return fsutil.WriteAtomic0600(path, func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 64*1024)
		var line bytes.Buffer
		emit := func(key string) error {
			line.Reset()
			appendMembershipLine(&line, idx.kind, names, idx.kind.Decode(key))
			_, err := bw.Write(line.Bytes())
			return err
		}
		pk, pok, err := prev.Next()
		if err != nil {
			return err
		}
		i := 0
		for pok || i < len(idx.entries) {
			var key string
			switch {
			case !pok || (i < len(idx.entries) && idx.entries[i].key < pk):
				key = idx.entries[i].key
				i++
			case i < len(idx.entries) && idx.entries[i].key == pk:
				key = pk
				i++
				if pk, pok, err = prev.Next(); err != nil {
					return err
				}
			default:
				key = pk
				if pk, pok, err = prev.Next(); err != nil {
					return err
				}
			}
			if err := emit(key); err != nil {
				return err
			}
		}
		return bw.Flush()
	})
}

// ErrMembershipOrder is a membership file whose keys are not strictly increasing.
var ErrMembershipOrder = errors.New("not in identity order")

// MembershipReader streams a membership file's identity keys and fails on a key that
// is not above the one before it, which the covered walk relies on to never drop a held
// record.
type MembershipReader struct {
	f    *os.File
	sc   *bufio.Scanner
	k    Kind
	path string
	last string
	read bool
}

// OpenMembership opens a membership file for streaming.
func OpenMembership(path string, k Kind) (*MembershipReader, error) {
	f, err := fsutil.OpenReplaceTarget(path)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &MembershipReader{f: f, sc: sc, k: k, path: path}, nil
}

// Next returns the next identity key; ok is false at the end of the file.
func (r *MembershipReader) Next() (key string, ok bool, err error) {
	for r.sc.Scan() {
		line := bytes.TrimSpace(r.sc.Bytes())
		if len(line) == 0 {
			continue
		}
		id, err := IdentityFromRecord(r.k, line)
		if err != nil {
			return "", false, fmt.Errorf("membership %s: %w", r.path, err)
		}
		key := r.k.Key(id)
		if r.read && key <= r.last {
			return "", false, fmt.Errorf("membership %s: %w", r.path, ErrMembershipOrder)
		}
		r.last, r.read = key, true
		return key, true, nil
	}
	return "", false, r.sc.Err()
}

// Close releases the file.
func (r *MembershipReader) Close() error { return r.f.Close() }

// ReadMembershipFile reads a whole membership file's identity set.
func ReadMembershipFile(path string, k Kind) ([]Identity, error) {
	r, err := OpenMembership(path, k)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var ids []Identity
	for {
		key, ok, err := r.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return ids, nil
		}
		ids = append(ids, k.Decode(key))
	}
}
