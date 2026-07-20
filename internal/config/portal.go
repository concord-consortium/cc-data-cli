package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ProductionPortal is the one portal whose firebase source is report-service-pro;
// every other portal maps to report-service-dev.
const ProductionPortal = "learn.concord.org"

const (
	firebaseProd = "report-service-pro"
	firebaseDev  = "report-service-dev"
)

// NormalizePortal reduces a portal value to hostname form: scheme stripped,
// lowercased, trailing slash removed, port preserved for dev portals.
func NormalizePortal(portal string) (string, error) {
	p := strings.TrimSpace(portal)
	if p == "" {
		return "", fmt.Errorf("portal is empty")
	}
	if !strings.Contains(p, "://") {
		p = "https://" + p
	}
	u, err := url.Parse(p)
	if err != nil {
		return "", fmt.Errorf("invalid portal %q: %w", portal, err)
	}
	host := strings.ToLower(u.Host)
	if host == "" {
		return "", fmt.Errorf("invalid portal %q: no host", portal)
	}
	return host, nil
}

// PortalOrigin re-expands a normalized hostname to an https origin, the form the
// server's /auth/cli portal query param requires.
func PortalOrigin(host string) string {
	return "https://" + host
}

// PortalFolder encodes a portal host into a filesystem-safe folder name, so a
// dev portal's port (localhost:8080) does not carry an illegal ':' into a path
// component. Applied on every platform for portability; the real host is kept in
// the credentials and manifest.
func PortalFolder(host string) string {
	return strings.ReplaceAll(host, ":", "_")
}

// FirebaseSource maps a portal host to its firebase project.
func FirebaseSource(host string) string {
	if host == ProductionPortal {
		return firebaseProd
	}
	return firebaseDev
}

// splitHostPort returns the host without its port, and whether a port was present.
func splitHostPort(host string) (string, bool) {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return host, false
	}
	return h, true
}
