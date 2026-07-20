package auth

import (
	"context"
	"errors"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
)

// StatusResult is the structured auth-status output the CLI and MCP render.
type StatusResult struct {
	DefaultPortal string         `json:"default_portal,omitempty"`
	Portals       []PortalStatus `json:"portals"`
}

// PortalStatus is one portal's offline metadata plus optional --check fields.
type PortalStatus struct {
	Portal   string    `json:"portal"`
	Backend  string    `json:"backend"`
	Server   string    `json:"server"`
	StoredAt time.Time `json:"stored_at"`

	Checked         bool       `json:"checked"`
	Valid           bool       `json:"valid,omitempty"`
	MetadataUnknown bool       `json:"metadata_unknown,omitempty"`
	Label           *string    `json:"label"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	ReportAccess    *bool      `json:"report_access,omitempty"`
	CheckError      string     `json:"check_error,omitempty"`
}

// Status renders stored credentials offline; with check it additionally
// introspects each token. It never fails on a per-portal validity outcome.
func Status(ctx context.Context, check bool) (StatusResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return StatusResult{}, err
	}
	var store creds.Store
	infos, err := store.List()
	if err != nil {
		return StatusResult{}, err
	}

	res := StatusResult{DefaultPortal: cfg.DefaultPortal, Portals: []PortalStatus{}}
	for _, info := range infos {
		ps := PortalStatus{
			Portal:   info.Portal,
			Backend:  info.Backend,
			Server:   info.Server,
			StoredAt: info.StoredAt,
		}
		if check {
			checkPortal(ctx, info.Portal, &ps)
		}
		res.Portals = append(res.Portals, ps)
	}
	return res, nil
}

func checkPortal(ctx context.Context, portal string, ps *PortalStatus) {
	ps.Checked = true
	client, err := api.ForPortal(portal)
	if err != nil {
		ps.CheckError = err.Error()
		return
	}
	info, err := client.CurrentToken(ctx)
	if err == nil {
		ps.Valid = true
		ps.Label = info.Label
		ps.CreatedAt = &info.CreatedAt
		ps.LastUsedAt = info.LastUsedAt
		ps.ReportAccess = &info.ReportAccess
		return
	}

	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Code == api.CodeNotFound {
		// Older server without introspection: fall back to a validity-only probe.
		ps.MetadataUnknown = true
		if probeErr := client.ProbeReports(ctx); probeErr == nil {
			ps.Valid = true
		} else {
			ps.CheckError = probeErr.Error()
		}
		return
	}
	if errors.As(err, &apiErr) && apiErr.Code == api.CodeNotAuthed {
		ps.Valid = false
		return
	}
	ps.CheckError = err.Error()
}
