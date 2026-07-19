package dataset

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// AttachmentsDir is the subdirectory holding downloaded attachment files.
const AttachmentsDir = "attachments"

// AttachRef is one attachment reference found while scanning a record.
type AttachRef struct {
	Source      string
	PublicPath  string
	ContentType string
	Name        string
	Collection  string // store type
	DocID       string // id for answers, history_id for history
	State       bool   // referenced by an __attachment__ marker in the state
	Identity    store.Identity
}

// FileID12 is the first 12 hex of sha256("<source>|<publicPath>").
func FileID12(source, publicPath string) string {
	sum := sha256.Sum256([]byte(source + "|" + publicPath))
	return hex.EncodeToString(sum[:])[:12]
}

// AttachmentFileName is the on-disk relative path for a ref.
func AttachmentFileName(source, publicPath, name string) string {
	return filepath.Join(AttachmentsDir, FileID12(source, publicPath)+"_"+SanitizeFilename(name))
}

var keepRunes = regexp.MustCompile(`[^A-Za-z0-9._-]`)
var windowsReserved = regexp.MustCompile(`^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$`)

// SanitizeFilename makes a client-written attachment name safe and portable on
// every OS: strip path segments, keep [A-Za-z0-9._-] (others become _), strip
// trailing dots/spaces, and prefix a Windows reserved device basename.
func SanitizeFilename(name string) string {
	// Strip any path segments (both separators) so only a basename remains.
	base := strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	// Strip Windows-illegal trailing dots/spaces before mapping other runes.
	base = strings.TrimRight(base, ". ")
	base = keepRunes.ReplaceAllString(base, "_")
	base = strings.TrimRight(base, ". ")
	if base == "" || base == "." || base == ".." {
		base = "_"
	}
	// Windows treats a reserved device name as reserved even with an extension
	// (CON.txt), so match the stem before the first dot, not the whole basename.
	stem := base
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if windowsReserved.MatchString(stem) {
		base = "_" + base
	}
	return base
}

// firebaseSource returns the dataset portal's firebase source.
func (d *Dataset) firebaseSource() string {
	return config.FirebaseSource(d.Ref.Portal)
}

// ScanRecordAttachments extracts attachment refs from one record.
func (d *Dataset) ScanRecordAttachments(typ string, record []byte) []AttachRef {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(record, &obj); err != nil {
		return nil
	}
	attachments := decodeAttachmentsMap(obj["attachments"])
	if len(attachments) == 0 {
		return nil
	}
	id, _ := store.IdentityFromRecord(typ, record)
	docID := jsonString(obj["id"])
	if typ == store.TypeHistory {
		docID = jsonString(obj["history_id"])
	}
	stateNames := stateAttachmentNames(obj["report_state"])
	source := d.firebaseSource()

	refs := make([]AttachRef, 0, len(attachments))
	for name, meta := range attachments {
		refs = append(refs, AttachRef{
			Source:      source,
			PublicPath:  meta.PublicPath,
			ContentType: meta.ContentType,
			Name:        name,
			Collection:  typ,
			DocID:       docID,
			State:       stateNames[name],
			Identity:    id,
		})
	}
	return refs
}

type attachmentMeta struct {
	PublicPath  string `json:"publicPath"`
	ContentType string `json:"contentType"`
}

// decodeAttachmentsMap reads the attachments map leniently (ignoring extra
// fields like folder).
func decodeAttachmentsMap(raw json.RawMessage) map[string]attachmentMeta {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]attachmentMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// stateAttachmentNames finds attachment names referenced by __attachment__
// markers inside the double-decoded state.
func stateAttachmentNames(raw json.RawMessage) map[string]bool {
	names := map[string]bool{}
	if len(raw) == 0 {
		return names
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return names
	}
	walkAttachmentMarkers(v, names)
	return names
}

func walkAttachmentMarkers(v any, names map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "__attachment__" {
				if s, ok := val.(string); ok {
					names[s] = true
				}
			}
			walkAttachmentMarkers(val, names)
		}
	case []any:
		for _, e := range t {
			walkAttachmentMarkers(e, names)
		}
	}
}

// ScanRunAttachments scans one run's records (store joined to membership) for the
// types that exist; it errors only when neither answers nor history exists.
func (d *Dataset) ScanRunAttachments(run int) (refs []AttachRef, scanned []string, err error) {
	m, err := d.ReadManifest()
	if err != nil {
		return nil, nil, err
	}
	for _, typ := range []string{store.TypeAnswers, store.TypeHistory} {
		ref, ok := m.Membership[MembershipKey(typ, run)]
		if !ok {
			continue
		}
		ids, rerr := store.ReadMembershipFile(d.Path(ref.File))
		if rerr != nil {
			return nil, nil, rerr
		}
		keys := map[string]bool{}
		for _, id := range ids {
			keys[id.Key(typ)] = true
		}
		st := m.Stores[typ]
		if st.File == "" {
			continue
		}
		found, serr := d.scanStore(typ, st.File, keys)
		if serr != nil {
			return nil, nil, serr
		}
		refs = append(refs, found...)
		scanned = append(scanned, typ)
	}
	return refs, scanned, nil
}

// scanAllAttachments scans the full answers and history stores named by m.
func (d *Dataset) scanAllAttachments(m *Manifest) ([]AttachRef, error) {
	var refs []AttachRef
	for _, typ := range []string{store.TypeAnswers, store.TypeHistory} {
		st := m.Stores[typ]
		if st.File == "" {
			continue
		}
		found, serr := d.scanStore(typ, st.File, nil)
		if serr != nil {
			return nil, serr
		}
		refs = append(refs, found...)
	}
	return refs, nil
}

// scanStore scans a store file; when keys is non-nil, only records whose identity
// key is in the set are considered.
func (d *Dataset) scanStore(typ, file string, keys map[string]bool) ([]AttachRef, error) {
	// A store named by the manifest must be on disk (the durable-write order
	// repoints the manifest only after the store rename), so fail closed rather
	// than treat a missing store as zero references: this scan drives a GC that
	// deletes unreferenced files.
	f, err := os.Open(d.Path(file))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var refs []AttachRef
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		trimmed := trimLine(line)
		if len(trimmed) > 0 {
			include := true
			if keys != nil {
				id, _ := store.IdentityFromRecord(typ, trimmed)
				include = keys[id.Key(typ)]
			}
			if include {
				refs = append(refs, d.ScanRecordAttachments(typ, trimmed)...)
			}
		}
		if rerr != nil {
			// io.EOF is the normal end of the last (unterminated) line; a real
			// read error must not be swallowed into a truncated, GC-driving scan.
			if rerr == io.EOF {
				break
			}
			return nil, rerr
		}
	}
	return refs, nil
}

// RebuildAttachmentIndex rescans the current stores, rebuilds the manifest
// attachment index over files present on disk, and garbage-collects unreferenced
// attachment files. Caller must hold the per-dataset lock.
func (d *Dataset) RebuildAttachmentIndex(m *Manifest) error {
	refs, err := d.scanAllAttachments(m)
	if err != nil {
		return err
	}
	// Group refs by (source, publicPath).
	byKey := map[string]*AttachmentFile{}
	referencedFiles := map[string]bool{}
	for _, r := range refs {
		key := r.Source + "|" + r.PublicPath
		relFile := AttachmentFileName(r.Source, r.PublicPath, r.Name)
		referencedFiles[filepath.Base(relFile)] = true
		af := byKey[key]
		if af == nil {
			af = &AttachmentFile{
				ID12:        FileID12(r.Source, r.PublicPath),
				Name:        r.Name,
				Source:      r.Source,
				PublicPath:  r.PublicPath,
				ContentType: r.ContentType,
				File:        relFile,
			}
			byKey[key] = af
		}
		if r.State {
			af.State = true
		}
		af.Refs = append(af.Refs, AttachmentRef{
			Type:           r.Collection,
			SourceKey:      r.Identity.SourceKey,
			RemoteEndpoint: r.Identity.RemoteEndpoint,
			QuestionID:     r.Identity.QuestionID,
			HistoryID:      r.Identity.HistoryID,
		})
	}

	// Keep only entries whose file is on disk; set size from stat.
	index := make([]AttachmentFile, 0, len(byKey))
	for _, af := range byKey {
		fi, statErr := os.Stat(d.Path(af.File))
		if statErr != nil {
			continue
		}
		af.Size = fi.Size()
		index = append(index, *af)
	}
	m.Attachments = index

	// GC: delete files no current record references.
	d.gcAttachments(referencedFiles)
	return nil
}

func (d *Dataset) gcAttachments(referenced map[string]bool) {
	dir := d.Path(AttachmentsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") {
			os.Remove(filepath.Join(dir, name))
			continue
		}
		if !referenced[name] {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func trimLine(line []byte) []byte {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}
