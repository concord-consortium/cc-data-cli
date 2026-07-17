// Package creds stores per-portal API tokens: OS keychain first, with a
// credentials.json fallback, plus the local metadata offline auth status renders.
package creds

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

const CurrentVersion = 1

const (
	BackendKeyring = "keyring"
	BackendFile    = "file"
)

// CredFile is the on-disk ~/.config/cc-data/credentials.json.
type CredFile struct {
	Version int                   `json:"version"`
	Portals map[string]PortalCred `json:"portals"`
}

// PortalCred is one portal's stored credential metadata. Token is inline only
// when Backend is "file".
type PortalCred struct {
	Token    string    `json:"token,omitempty"`
	Backend  string    `json:"backend"`
	Server   string    `json:"server"`
	StoredAt time.Time `json:"stored_at"`
}

// Store is the credential API.
type Store struct{}

// now is a seam so tests can pin the stored-at timestamp.
var now = time.Now

func credPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func readFile() (*CredFile, error) {
	path, err := credPath()
	if err != nil {
		return nil, err
	}
	data, err := fsutil.ReadReplaceTarget(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CredFile{Version: CurrentVersion, Portals: map[string]PortalCred{}}, nil
		}
		return nil, err
	}
	var cf CredFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, err
	}
	if cf.Portals == nil {
		cf.Portals = map[string]PortalCred{}
	}
	if cf.Version == 0 {
		cf.Version = CurrentVersion
	}
	return &cf, nil
}

func writeFile(cf *CredFile) error {
	dir, err := config.ConfigDir()
	if err != nil {
		return err
	}
	if err := fsutil.EnsureDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "credentials.json")
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic0600(path, data)
}

// Save stores a token for a portal, preferring the OS keychain and falling back
// to the inline credentials file with a one-line stderr note.
func (Store) Save(portal, token, server string) error {
	cf, err := readFile()
	if err != nil {
		return err
	}
	pc := PortalCred{Server: server, StoredAt: now().UTC()}
	if err := keyringSet(keyringService, portal, token); err != nil {
		pc.Backend = BackendFile
		pc.Token = token
		output.Warnf("OS keychain unavailable (%v); storing token in credentials.json (0600)", err)
	} else {
		pc.Backend = BackendKeyring
		// If a prior file-backed token existed, drop the inline copy.
		pc.Token = ""
	}
	cf.Portals[portal] = pc
	return writeFile(cf)
}

// Token returns the stored token for a portal.
func (Store) Token(portal string) (string, error) {
	cf, err := readFile()
	if err != nil {
		return "", err
	}
	pc, ok := cf.Portals[portal]
	if !ok {
		return "", ErrNotFound
	}
	if pc.Backend == BackendKeyring {
		return keyringGet(keyringService, portal)
	}
	return pc.Token, nil
}

// Delete removes a portal's credential from the keychain and the metadata file.
func (Store) Delete(portal string) error {
	cf, err := readFile()
	if err != nil {
		return err
	}
	pc, ok := cf.Portals[portal]
	if !ok {
		return nil
	}
	if pc.Backend == BackendKeyring {
		if err := keyringDelete(keyringService, portal); err != nil && !errors.Is(err, keyringErrNotFound) {
			return err
		}
	}
	delete(cf.Portals, portal)
	return writeFile(cf)
}

// PortalInfo is one portal's offline-renderable metadata.
type PortalInfo struct {
	Portal   string
	Backend  string
	Server   string
	StoredAt time.Time
}

// List returns stored portals sorted by host, without touching the network.
func (Store) List() ([]PortalInfo, error) {
	cf, err := readFile()
	if err != nil {
		return nil, err
	}
	out := make([]PortalInfo, 0, len(cf.Portals))
	for portal, pc := range cf.Portals {
		out = append(out, PortalInfo{Portal: portal, Backend: pc.Backend, Server: pc.Server, StoredAt: pc.StoredAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Portal < out[j].Portal })
	return out, nil
}

// ErrNotFound is returned when a portal has no stored credential.
var ErrNotFound = errors.New("no stored credential for portal")
