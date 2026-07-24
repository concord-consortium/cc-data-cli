package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The portal matrix. Every shape a portal value can arrive in, crossed with the
// two parsers, one row per shape. This table is the point: the bugs this package
// has shipped were all a call site reaching a parser that handled its shape
// differently than the author assumed, and a matrix is the only form in which
// "what does X do to Y" is answerable by reading rather than by searching.
//
// target is what ParsePortalTarget returns: a portal you are about to talk to,
// so aliases expand and a near-miss alias is refused. identity is what
// ParsePortalIdentity returns: a portal that names something on disk, so aliases
// are refused and single-label hosts survive. "" in either column means the
// parser must reject the value.
var portalMatrix = []struct {
	name string
	in   string

	target       string // expected host, "" to expect an error
	targetOrigin string // expected auth-flow origin, checked when target is set
	identity     string // expected host, "" to expect an error
}{
	// Aliases: a target expands them, an identity refuses them. An identity is
	// also a folder name, so expanding would give one dataset two names and
	// leaving it literal would file data under a portal that can never hold a
	// credential.
	{"short alias", "staging", StagingPortal, "https://" + StagingPortal, ""},
	{"long alias", "production", ProductionPortal, "https://" + ProductionPortal, ""},
	{"alias, mixed case", "STAGING", StagingPortal, "https://" + StagingPortal, ""},
	{"alias, padded", "  dev  ", DevPortal, "http://" + DevPortal, ""},
	{"loopback alias", "dev", DevPortal, "http://" + DevPortal, ""},
	// A scheme must not smuggle an alias past either parser: the lookup runs on
	// the normalized host, not the raw string.
	{"alias with a scheme", "https://staging", StagingPortal, "https://" + StagingPortal, ""},
	{"alias with an http scheme", "http://prod", ProductionPortal, "https://" + ProductionPortal, ""},
	{"alias with scheme, mixed case", "HTTPS://STAGING", StagingPortal, "https://" + StagingPortal, ""},
	{"alias with a scheme and a slash", "https://staging/", StagingPortal, "https://" + StagingPortal, ""},
	// An explicit https on the loopback alias is still honored.
	{"loopback alias with https", "https://dev", DevPortal, "https://" + DevPortal, ""},

	// Hostnames and URLs normalize identically on both sides.
	{"hostname", ProductionPortal, ProductionPortal, "https://" + ProductionPortal, ProductionPortal},
	{"hostname, mixed case", "Learn.Concord.Org", ProductionPortal, "https://" + ProductionPortal, ProductionPortal},
	{"URL", "https://learn.concord.org", ProductionPortal, "https://" + ProductionPortal, ProductionPortal},
	{"URL with trailing slash", "https://learn.concord.org/", ProductionPortal, "https://" + ProductionPortal, ProductionPortal},
	{"URL with path", "https://learn.concord.org/foo", ProductionPortal, "https://" + ProductionPortal, ProductionPortal},
	{"unknown but well-formed host", "learn.example.concord.org", "learn.example.concord.org", "https://learn.example.concord.org", "learn.example.concord.org"},

	// http on a real portal is ignored rather than honored, so naming
	// "http://learn.concord.org" cannot downgrade a real login.
	{"http on a real portal", "http://learn.concord.org", ProductionPortal, "https://" + ProductionPortal, ProductionPortal},
	// A loopback portal may legitimately be served either way, so an explicit
	// https survives to the auth URL.
	{"loopback with port", "localhost:3000", DevPortal, "http://" + DevPortal, DevPortal},
	{"explicit https loopback", "https://localhost:3001", "localhost:3001", "https://localhost:3001", "localhost:3001"},
	{"explicit https loopback, mixed case scheme", "HTTPS://127.0.0.1:3001", "127.0.0.1:3001", "https://127.0.0.1:3001", "127.0.0.1:3001"},
	// An IPv6 literal and an underscore host are both refused because
	// Portal.Folder could not encode them reversibly; localhost and 127.0.0.1
	// cover every loopback case that matters.
	{"IPv6 loopback", "[::1]:3000", "", "", ""},
	{"IPv6 loopback, no port", "[::1]", "", "", ""},
	{"underscore in a host", "host_3000", "", "", ""},
	{"underscore in a label", "a_b.concord.org", "", "", ""},
	{"non-loopback IP", "192.168.1.10:3000", "192.168.1.10:3000", "https://192.168.1.10:3000", "192.168.1.10:3000"},

	// A single label that is not loopback is a misspelled alias far more often
	// than a portal, so a target refuses it. An identity keeps it: an older
	// build stored credentials under such hosts, and the folders they name must
	// stay reachable.
	{"near-miss alias", "stagng", "", "", "stagng"},
	{"legacy single-label host", "myportal", "", "", "myportal"},
	{"single label, loopback", "localhost", "localhost", "http://localhost", "localhost"},

	// Path traversal. url.Parse reports ".." as the host of "https://../x", and
	// Folder passes a host straight into a path component, so both parsers have
	// to refuse these or a dataset ref lands outside the data root.
	{"dot-dot", "..", "", "", ""},
	{"dot", ".", "", "", ""},
	{"traversal", "../../escaped", "", "", ""},
	{"traversal via URL", "https://../x", "", "", ""},
	{"leading dot", ".hidden.org", "", "", ""},
	// url.Parse keeps only the host, which then falls to the single-label rule.
	{"slash-bearing", "a/b", "", "", "a"},

	// A trailing colon parses as an empty port, which would key a credential and
	// name a folder distinct from the same host without it.
	{"empty port", "localhost:", "", "", ""},
	{"empty port on a hostname", "learn.concord.org:", "", "", ""},

	// Nothing at all.
	{"empty", "", "", "", ""},
	{"whitespace", "   ", "", "", ""},
}

func TestPortalMatrix(t *testing.T) {
	for _, c := range portalMatrix {
		t.Run(c.name, func(t *testing.T) {
			portal, origin, err := ParsePortalTarget(c.in)
			switch {
			case c.target == "" && err == nil:
				t.Errorf("ParsePortalTarget(%q) = %q, want an error", c.in, portal.Host())
			case c.target != "" && err != nil:
				t.Errorf("ParsePortalTarget(%q) error: %v", c.in, err)
			case c.target != "":
				if portal.Host() != c.target {
					t.Errorf("ParsePortalTarget(%q) = %q, want %q", c.in, portal.Host(), c.target)
				}
				if origin != c.targetOrigin {
					t.Errorf("ParsePortalTarget(%q) origin = %q, want %q", c.in, origin, c.targetOrigin)
				}
			}

			identity, err := ParsePortalIdentity(c.in)
			switch {
			case c.identity == "" && err == nil:
				t.Errorf("ParsePortalIdentity(%q) = %q, want an error", c.in, identity.Host())
			case c.identity != "" && err != nil:
				t.Errorf("ParsePortalIdentity(%q) error: %v", c.in, err)
			case c.identity != "":
				if identity.Host() != c.identity {
					t.Errorf("ParsePortalIdentity(%q) = %q, want %q", c.in, identity.Host(), c.identity)
				}
			}
		})
	}
}

// TestPortalFolderStaysOneComponent is the property the matrix's traversal rows
// exist to protect: whatever a parser accepts must be a single path component,
// because Ref.Dir joins it straight into the data root and dataset delete calls
// os.RemoveAll on the result.
func TestPortalFolderStaysOneComponent(t *testing.T) {
	root := filepath.Join("/data", "root")
	check := func(t *testing.T, in string, p Portal) {
		t.Helper()
		folder := p.Folder()
		if strings.ContainsAny(folder, `/\`) || folder == "." || folder == ".." {
			t.Errorf("portal from %q has folder %q, not a single path component", in, folder)
		}
		joined := filepath.Join(root, folder, "datasets", "x")
		if !strings.HasPrefix(filepath.Clean(joined), filepath.Clean(root)+string(filepath.Separator)) {
			t.Errorf("portal from %q resolves outside the data root: %q", in, joined)
		}
	}
	// Whatever the parsers actually return, not what the table says they should:
	// asserting on the expected strings would hold even if a parser returned
	// something else entirely.
	for _, c := range portalMatrix {
		t.Run(c.name, func(t *testing.T) {
			if p, _, err := ParsePortalTarget(c.in); err == nil {
				check(t, c.in, p)
			}
			if p, err := ParsePortalIdentity(c.in); err == nil {
				check(t, c.in, p)
			}
		})
	}
}

// TestUnshapedHostErrorCarriesThePortal pins the one escape hatch: a shape
// refusal hands back the parsed portal so auth can accept a host that a stored
// credential vouches for, and it names the environments, since a typo'd alias is
// what lands here.
func TestUnshapedHostErrorCarriesThePortal(t *testing.T) {
	_, _, err := ParsePortalTarget("stagng")
	var unshaped *UnshapedHostError
	if !errors.As(err, &unshaped) {
		t.Fatalf("want *UnshapedHostError, got %T: %v", err, err)
	}
	if unshaped.Portal.Host() != "stagng" {
		t.Errorf("error carries portal %q, want %q", unshaped.Portal.Host(), "stagng")
	}
	if !strings.Contains(err.Error(), "expected one of") {
		t.Errorf("error should list the environments: %v", err)
	}
}

// TestIdentityRejectionNamesTheHostname keeps the alias refusal actionable: the
// message has to carry the hostname to use, since the whole failure mode is a
// researcher typing the environment name they read in the guide.
func TestIdentityRejectionNamesTheHostname(t *testing.T) {
	for _, in := range []string{"staging", "https://staging", "STAGING"} {
		_, err := ParsePortalIdentity(in)
		if err == nil {
			t.Fatalf("%q should be refused as an identity", in)
		}
		if !strings.Contains(err.Error(), StagingPortal) {
			t.Errorf("%q error should name the hostname to use: %v", in, err)
		}
		// The refusal is typed and carries the literal portal, which is what
		// lets an existing dataset folder under it stay reachable.
		var alias *AliasPortalError
		if !errors.As(err, &alias) {
			t.Fatalf("%q: want *AliasPortalError, got %T", in, err)
		}
		if alias.Portal.Host() != "staging" || alias.Env.Portal.Host() != StagingPortal {
			t.Errorf("%q carries portal %q / env %q", in, alias.Portal.Host(), alias.Env.Portal.Host())
		}
	}
}

func TestPortalOriginAndFolder(t *testing.T) {
	cases := []struct{ host, origin, folder string }{
		{ProductionPortal, "https://" + ProductionPortal, ProductionPortal},
		{"localhost:3000", "http://localhost:3000", "localhost_3000"},
		// Adopted only: the parsers refuse an IPv6 literal, but a host stored by
		// an older build still has to render.
		{"[::1]:3000", "http://[::1]:3000", "[__1]_3000"},
		{"portal.example.org:8443", "https://portal.example.org:8443", "portal.example.org_8443"},
	}
	for _, c := range cases {
		p := AdoptStoredPortal(c.host)
		if got := p.Origin(); got != c.origin {
			t.Errorf("Portal(%q).Origin() = %q, want %q", c.host, got, c.origin)
		}
		if got := p.Folder(); got != c.folder {
			t.Errorf("Portal(%q).Folder() = %q, want %q", c.host, got, c.folder)
		}
	}
}

func TestMustPortalRejectsUnnormalized(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustPortal should panic on a value that is not already normalized")
		}
	}()
	MustPortal("https://learn.concord.org/")
}

func TestLookupEnvironment(t *testing.T) {
	cases := []struct {
		name       string
		wantPortal string
		wantServer string
	}{
		// Both the short and the long spelling of each environment resolve to
		// the same pair; neither form is privileged.
		{"prod", ProductionPortal, DefaultServerURL},
		{"production", ProductionPortal, DefaultServerURL},
		{"stage", StagingPortal, StagingServer},
		{"staging", StagingPortal, StagingServer},
		{"dev", DevPortal, DevServer},
		{"development", DevPortal, DevServer},
		{" Staging ", StagingPortal, StagingServer},
	}
	for _, c := range cases {
		env, ok := LookupEnvironment(c.name)
		if !ok {
			t.Fatalf("LookupEnvironment(%q) not found", c.name)
		}
		if env.Portal.Host() != c.wantPortal || env.Server != c.wantServer {
			t.Errorf("LookupEnvironment(%q) = %+v, want portal %q server %q", c.name, env, c.wantPortal, c.wantServer)
		}
	}

	for _, name := range []string{"", "learn.concord.org", "stagin"} {
		if _, ok := LookupEnvironment(name); ok {
			t.Errorf("LookupEnvironment(%q) should not match an alias", name)
		}
	}
}

func TestEnvironmentNamesAreResolvable(t *testing.T) {
	for _, name := range EnvironmentNames() {
		if _, ok := LookupEnvironment(name); !ok {
			t.Errorf("EnvironmentNames listed %q but LookupEnvironment does not know it", name)
		}
	}
}

// TestEveryEnvironmentIsNamed walks the alias table rather than the curated
// name list, so an environment added to environments but never given a
// canonical name fails here instead of shipping invisible: nothing would print
// it in help or error text, and the only way to reach it would be to already
// know the spelling.
func TestEveryEnvironmentIsNamed(t *testing.T) {
	named := map[Environment]bool{}
	for _, name := range EnvironmentNames() {
		env, ok := LookupEnvironment(name)
		if !ok {
			continue // TestEnvironmentNamesAreResolvable reports this.
		}
		named[env] = true
	}
	for name, env := range environments {
		if !named[env] {
			t.Errorf("environment %+v (spelled %q) is in no EnvironmentNames entry", env, name)
		}
		if _, ok := environmentsByPortal[env.Portal]; !ok {
			t.Errorf("environment %+v (spelled %q) has no entry in environmentsByPortal", env, name)
		}
	}
}

func TestEnvironmentPairsAreValid(t *testing.T) {
	// Every alias must survive the same validation the flags apply, so a typo in
	// the table fails here rather than at login time. The portal side is checked
	// as the package initializes (mustHost), so this covers the server side.
	for name, env := range environments {
		origin, err := ValidateServerURL(env.Server)
		if err != nil {
			t.Errorf("environment %q server %q is not an allowed origin: %v", name, env.Server, err)
		}
		if origin != env.Server {
			t.Errorf("environment %q server %q is not canonical (got %q)", name, env.Server, origin)
		}
	}
}

// TestBuildEnvironmentsByPortalRejectsCollisions pins that no two environments
// may claim the same portal with different servers. The index is built by
// ranging over a map, so such a pair would make the winner depend on iteration
// order and send the same "--portal <hostname>" login to different servers on
// different runs. The real table is checked as this package initializes, which
// panics rather than failing a test, so the collision itself is exercised here
// against a table built for it.
func TestBuildEnvironmentsByPortalRejectsCollisions(t *testing.T) {
	if _, err := buildEnvironmentsByPortal(environments); err != nil {
		t.Fatalf("the shipped environments should build: %v", err)
	}

	staging := MustPortal(StagingPortal)
	colliding := map[string]Environment{
		"one": {Portal: staging, Server: StagingServer},
		"two": {Portal: staging, Server: DefaultServerURL},
	}
	_, err := buildEnvironmentsByPortal(colliding)
	if err == nil {
		t.Fatal("two environments claiming one portal should be rejected")
	}
	if !strings.Contains(err.Error(), StagingPortal) {
		t.Errorf("the error should name the contested portal: %v", err)
	}

	// Two spellings of the same pair are not a collision; that is how every
	// environment's short and long names coexist.
	agreeing := map[string]Environment{
		"stage":   {Portal: staging, Server: StagingServer},
		"staging": {Portal: staging, Server: StagingServer},
	}
	if _, err := buildEnvironmentsByPortal(agreeing); err != nil {
		t.Errorf("two spellings of one environment should build: %v", err)
	}
}

func TestEnvironmentForPortal(t *testing.T) {
	// A portal spelled out in full finds the same pair its alias would.
	for _, name := range EnvironmentNames() {
		want, _ := LookupEnvironment(name)
		got, ok := EnvironmentForPortal(want.Portal)
		if !ok {
			t.Errorf("EnvironmentForPortal(%q) not found", want.Portal.Host())
			continue
		}
		if got != want {
			t.Errorf("EnvironmentForPortal(%q) = %+v, want %+v", want.Portal.Host(), got, want)
		}
	}

	if _, ok := EnvironmentForPortal(MustPortal("learn.example.concord.org")); ok {
		t.Error("an unknown portal should not resolve to an environment")
	}
	// The reverse index is keyed by portal, not by alias name.
	if _, ok := EnvironmentForPortal(AdoptStoredPortal("staging")); ok {
		t.Error("an alias name is not a portal host")
	}
}

func TestExpandServerAlias(t *testing.T) {
	if got := ExpandServerAlias("staging"); got != StagingServer {
		t.Errorf("ExpandServerAlias(staging) = %q", got)
	}
	// Portal and server aliases are independent: each expands only its own side.
	if got := ExpandServerAlias("dev"); got != DevServer {
		t.Errorf("ExpandServerAlias(dev) = %q", got)
	}
	// Non-aliases pass through untouched, so full origins keep working.
	for _, v := range []string{"", "https://report-server.concord.org"} {
		if got := ExpandServerAlias(v); got != v {
			t.Errorf("ExpandServerAlias(%q) = %q, want unchanged", v, got)
		}
	}
}
