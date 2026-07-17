package config

import "testing"

func TestNormalizePortal(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://learn.concord.org/", "learn.concord.org"},
		{"https://learn.concord.org", "learn.concord.org"},
		{"learn.concord.org", "learn.concord.org"},
		{"HTTPS://Learn.Concord.ORG/", "learn.concord.org"},
		{"http://localhost:8080", "localhost:8080"},
		{"localhost:8080", "localhost:8080"},
		{"learn.portal.staging.concord.org", "learn.portal.staging.concord.org"},
	}
	for _, c := range cases {
		got, err := NormalizePortal(c.in)
		if err != nil {
			t.Fatalf("NormalizePortal(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("NormalizePortal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePortalEmpty(t *testing.T) {
	if _, err := NormalizePortal("  "); err == nil {
		t.Fatal("empty portal should error")
	}
}

func TestPortalFolderEncoding(t *testing.T) {
	if got := PortalFolder("localhost:8080"); got != "localhost_8080" {
		t.Fatalf("PortalFolder(localhost:8080) = %q, want localhost_8080", got)
	}
	if got := PortalFolder("learn.concord.org"); got != "learn.concord.org" {
		t.Fatalf("PortalFolder(learn.concord.org) = %q, want unchanged", got)
	}
}

func TestPortalOrigin(t *testing.T) {
	if got := PortalOrigin("localhost:8080"); got != "https://localhost:8080" {
		t.Fatalf("PortalOrigin = %q", got)
	}
}

func TestFirebaseSource(t *testing.T) {
	if FirebaseSource("learn.concord.org") != "report-service-pro" {
		t.Fatal("production portal should map to report-service-pro")
	}
	if FirebaseSource("learn.portal.staging.concord.org") != "report-service-dev" {
		t.Fatal("non-production portal should map to report-service-dev")
	}
	if FirebaseSource("localhost:8080") != "report-service-dev" {
		t.Fatal("dev portal should map to report-service-dev")
	}
}
