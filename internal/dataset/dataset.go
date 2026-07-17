package dataset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// ErrBusy is returned when a mutating command cannot take the dataset locks.
var ErrBusy = errors.New("dataset is busy: another cc-data command is writing to it")

// ErrNotFound is returned when a dataset directory has no manifest.
var ErrNotFound = errors.New("dataset not found")

// clock is a seam so tests can pin time.
var defaultClock = time.Now
var clock = defaultClock

// Dataset is an on-disk dataset with its process-wide lock guards.
type Dataset struct {
	Ref     Ref
	Dir     string
	dsLock  *store.DatasetLock
	actLock *store.ActivityLock
}

// Open returns a handle for a ref under a data root; it does not create anything.
func Open(dataRoot string, ref Ref) *Dataset {
	dir := ref.Dir(dataRoot)
	return &Dataset{
		Ref:     ref,
		Dir:     dir,
		dsLock:  store.DatasetLockFor(dir),
		actLock: store.ActivityLockFor(dir),
	}
}

// Exists reports whether the dataset's manifest is present.
func (d *Dataset) Exists() bool {
	_, err := os.Stat(filepath.Join(d.Dir, ManifestFile))
	return err == nil
}

// Path joins a dataset-relative path to the dataset directory.
func (d *Dataset) Path(rel string) string { return filepath.Join(d.Dir, rel) }

// Lock returns the per-dataset guard.
func (d *Dataset) Lock() *store.DatasetLock { return d.dsLock }

// Activity returns the whole-fetch activity guard.
func (d *Dataset) Activity() *store.ActivityLock { return d.actLock }

// ReadManifest reads and migrates the dataset's manifest.
func (d *Dataset) ReadManifest() (*Manifest, error) {
	if !d.Exists() {
		return nil, ErrNotFound
	}
	return ReadManifestFile(d.Dir)
}

// WriteManifest writes the manifest atomically; the per-dataset guard must be
// held (the merge and mutation critical sections hold it).
func (d *Dataset) WriteManifest(m *Manifest) error {
	if !d.dsLock.Held() {
		return fmt.Errorf("internal error: WriteManifest called without the per-dataset lock held")
	}
	return writeManifestFile(d.Dir, m)
}

// Create initializes a new dataset directory and manifest. The name must already
// be validated by the caller.
func Create(dataRoot string, ref Ref, description string) (*Dataset, error) {
	if err := ValidateName(ref.Name); err != nil {
		return nil, err
	}
	d := Open(dataRoot, ref)
	if d.Exists() {
		return nil, fmt.Errorf("dataset %s already exists", ref)
	}
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return nil, err
	}
	m := &Manifest{
		Version:     CurrentManifestVersion,
		Name:        ref.Name,
		Description: description,
		CreatedAt:   clock().UTC(),
		Stores:      map[string]Store{},
		Membership:  map[string]MembershipRef{},
	}
	if err := writeManifestFile(d.Dir, m); err != nil {
		return nil, err
	}
	return d, nil
}

// lockMutation acquires the activity (exclusive) then per-dataset lock, both
// non-blocking, returning a release func or ErrBusy.
func (d *Dataset) lockMutation() (func(), error) {
	ok, err := d.actLock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBusy
	}
	ok, err = d.dsLock.TryLock()
	if err != nil {
		d.actLock.Unlock()
		return nil, err
	}
	if !ok {
		d.actLock.Unlock()
		return nil, ErrBusy
	}
	return func() {
		d.dsLock.Unlock()
		d.actLock.Unlock()
	}, nil
}

// Edit updates the dataset description under the mutation locks.
func (d *Dataset) Edit(description string) error {
	release, err := d.lockMutation()
	if err != nil {
		return err
	}
	defer release()
	m, err := d.ReadManifest()
	if err != nil {
		return err
	}
	m.Description = description
	return writeManifestFile(d.Dir, m)
}

// Rename renames the dataset folder and manifest name under the mutation locks.
func (d *Dataset) Rename(dataRoot string, newName string) (*Dataset, error) {
	if err := ValidateName(newName); err != nil {
		return nil, err
	}
	release, err := d.lockMutation()
	if err != nil {
		return nil, err
	}
	newRef := Ref{Portal: d.Ref.Portal, Name: newName}
	newDir := newRef.Dir(dataRoot)
	if _, statErr := os.Stat(newDir); statErr == nil {
		release()
		return nil, fmt.Errorf("dataset %s already exists", newRef)
	}
	m, err := d.ReadManifest()
	if err != nil {
		release()
		return nil, err
	}
	m.Name = newName
	if err := writeManifestFile(d.Dir, m); err != nil {
		release()
		return nil, err
	}
	// Release locks before moving the folder (the lock files move with it).
	release()
	if err := os.Rename(d.Dir, newDir); err != nil {
		return nil, err
	}
	return Open(dataRoot, newRef), nil
}

// Delete removes the entire dataset folder after taking the exclusive activity
// lock and releasing its own handles.
func (d *Dataset) Delete() error {
	ok, err := d.actLock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	// Release the flock handle before removing the folder it lives in.
	d.actLock.Unlock()
	return os.RemoveAll(d.Dir)
}

// UpsertDownload records or replaces a download entry under the per-dataset lock.
func (d *Dataset) UpsertDownload(dl Download) error {
	lock := d.Lock()
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	m, err := d.ReadManifest()
	if err != nil {
		return err
	}
	for i := range m.Downloads {
		if sameDownload(m.Downloads[i], dl) {
			m.Downloads[i] = dl
			return d.WriteManifest(m)
		}
	}
	m.Downloads = append(m.Downloads, dl)
	return d.WriteManifest(m)
}

// UpdateManifest applies fn to the manifest under the per-dataset lock.
func (d *Dataset) UpdateManifest(fn func(*Manifest) error) error {
	lock := d.Lock()
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	m, err := d.ReadManifest()
	if err != nil {
		return err
	}
	if err := fn(m); err != nil {
		return err
	}
	return d.WriteManifest(m)
}

func sameDownload(a, b Download) bool {
	if a.Type != b.Type || a.RunID != b.RunID {
		return false
	}
	if (a.JobID == nil) != (b.JobID == nil) {
		return false
	}
	if a.JobID != nil && *a.JobID != *b.JobID {
		return false
	}
	return true
}

// Purge deletes all downloaded artifacts and clears the manifest holdings while
// keeping the dataset shell. Lock files are never removed.
func (d *Dataset) Purge() error {
	release, err := d.lockMutation()
	if err != nil {
		return err
	}
	defer release()

	m, err := d.ReadManifest()
	if err != nil {
		return err
	}
	if err := d.deleteArtifacts(); err != nil {
		return err
	}
	m.Stores = map[string]Store{}
	m.Membership = map[string]MembershipRef{}
	m.Downloads = nil
	m.Attachments = nil
	return writeManifestFile(d.Dir, m)
}

// deleteArtifacts removes stores, segments, membership files, CSVs, and the
// attachments directory, but never a lock file.
func (d *Dataset) deleteArtifacts() error {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".lock") {
			continue
		}
		if e.IsDir() {
			if name == "segments" || name == "attachments" {
				if err := os.RemoveAll(filepath.Join(d.Dir, name)); err != nil {
					return err
				}
			}
			continue
		}
		if isArtifactFile(name) {
			if err := os.Remove(filepath.Join(d.Dir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

var artifactFileRe = regexp.MustCompile(`^(answers\.v\d+\.jsonl|history\.v\d+\.jsonl|members_.*\.v\d+\.jsonl|report_\d+.*\.csv)$`)

func isArtifactFile(name string) bool {
	return artifactFileRe.MatchString(name)
}

// AutoName generates a {date}_{slug} name (slug from description) or a
// {date}_{n} counter scanning existing names.
func AutoName(dataRoot, portalHost, description string) (string, error) {
	date := clock().UTC().Format("2006-01-02")
	if slug := kebab(description); slug != "" {
		return date + "_" + slug, nil
	}
	existing := map[string]bool{}
	dir := PortalDatasetsDir(dataRoot, portalHost)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				existing[e.Name()] = true
			}
		}
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s_%d", date, n)
		if !existing[candidate] {
			return candidate, nil
		}
	}
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func kebab(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}
