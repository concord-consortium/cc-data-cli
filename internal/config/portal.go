package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// ProductionPortal is the portal a login targets when nothing names another.
const ProductionPortal = "learn.concord.org"

// The portal hosts and report-server origins behind the environment aliases.
// A portal is stored in normalized (scheme-less) host form; Portal.Origin adds
// the scheme, so the loopback dev portal needs no scheme here.
const (
	StagingPortal = "learn.portal.staging.concord.org"
	DevPortal     = "localhost:3000"

	StagingServer = "https://report-server.concordqa.org"
	DevServer     = "http://localhost:4000"
)

// Portal is a portal host that has been through one of this package's parsers.
// No other package can build one from a bare string, so a portal value cannot
// reach a credential key, a dataset folder, or an auth URL without having been
// parsed exactly once. Identity is the host alone, so Portals compare with ==
// and work as map keys.
type Portal struct{ host string }

// Host is the normalized portal host: lowercased, scheme-less, port preserved.
func (p Portal) Host() string { return p.host }

func (p Portal) String() string { return p.host }

// IsZero reports the absence of a portal, which is how an unset default_portal
// travels.
func (p Portal) IsZero() bool { return p.host == "" }

// Origin re-expands the host to an origin, the form the server's /auth/cli
// portal query param requires. A loopback dev portal gets http, the same
// exception ValidateServerURL makes for loopback server origins; every other
// portal is https. Deriving the scheme here rather than carrying it in the host
// keeps the credential key, the manifest portal, and Folder on a bare host.
func (p Portal) Origin() string {
	if isLoopbackHostPort(p.host) {
		return "http://" + p.host
	}
	return "https://" + p.host
}

// Folder encodes the host into one filesystem-safe path component, so a dev
// portal's port (localhost:8080) does not carry an illegal ':' into a path.
// Applied on every platform for portability; the real host is kept in the
// credentials and manifest. That this is a single component, never a traversal,
// is guaranteed upstream by checkHostSyntax rather than by escaping here.
func (p Portal) Folder() string { return strings.ReplaceAll(p.host, ":", "_") }

// ParsePortalTarget parses a portal the CLI is about to talk to: an environment
// alias, a hostname, or a URL. Aliases expand, and the result must look like a
// portal rather than a near-miss alias. It also returns the origin the auth flow
// should send for it, which differs from Origin only for a loopback portal named
// with an explicit https://: a local portal may be served over either scheme, so
// "https://localhost:3001" is honored rather than downgraded, while a scheme on
// any other portal is ignored so "http://learn.concord.org" cannot downgrade a
// real portal login.
//
// A single-label non-loopback host is refused with *UnshapedHostError, which
// carries the parsed portal so a caller that can vouch for it (auth, holding a
// credential stored under that host) can accept it anyway.
//
// The alias lookup happens on the parsed host rather than the raw input, so
// every spelling that normalizes to an alias expands: checking the raw string
// first would let "https://staging" slip past as a literal host.
func ParsePortalTarget(v string) (Portal, string, error) {
	p, err := parseHost(v)
	if err != nil {
		return Portal{}, "", err
	}
	if env, ok := LookupEnvironment(p.host); ok {
		p = env.Portal
	}
	if err := checkPortalShape(p); err != nil {
		return Portal{}, "", err
	}
	if isLoopbackHostPort(p.host) && hasHTTPSScheme(v) {
		return p, "https://" + p.host, nil
	}
	return p, p.Origin(), nil
}

// ParsePortalIdentity parses a portal that names something on disk: a dataset
// ref's portal, or default_portal. An alias is refused rather than expanded.
// Expanding it would give one dataset folder two names, and leaving it literal
// would file data under a portal that can never hold a credential, which reads
// downstream as a NOT_AUTHENTICATED that logging in does not fix.
//
// The single-label shape check that ParsePortalTarget applies is deliberately
// not applied here: a portal on disk is an identity that must stay whatever it
// was written as, and an older build stored credentials under single-label
// hosts.
//
// The alias lookup happens on the parsed host rather than the raw input, so a
// scheme cannot smuggle one through: "https://staging" normalizes to "staging"
// and is refused exactly as the bare spelling is. The refusal is
// *AliasPortalError, which carries the literal portal, so a caller holding a
// folder that already exists under it can still reach it.
func ParsePortalIdentity(v string) (Portal, error) {
	p, err := parseHost(v)
	if err != nil {
		return Portal{}, err
	}
	if env, ok := LookupEnvironment(p.host); ok {
		return Portal{}, &AliasPortalError{Portal: p, Env: env}
	}
	return p, nil
}

// AliasPortalError is an identity that normalizes to an environment alias name.
// It is a policy refusal, not a safety one: the portal it carries is a perfectly
// good path component, it just names an environment rather than a host, so a
// caller that can vouch for it (an existing dataset folder) may accept it.
type AliasPortalError struct {
	Portal Portal
	Env    Environment
}

func (e *AliasPortalError) Error() string {
	return fmt.Sprintf("portal %q is an environment alias; use the full hostname %q instead",
		e.Portal.Host(), e.Env.Portal.Host())
}

// AdoptStoredPortal takes a portal host cc-data itself wrote (a credential key,
// a dataset folder) back as a Portal without re-parsing it. This is the one
// deliberate way around the parsers: the value is already an on-disk identity,
// and refusing it now would strand the data and credentials it names.
func AdoptStoredPortal(host string) Portal { return Portal{host: host} }

// MustPortal parses a portal host that is already in normalized form, panicking
// if it is not. It is a parser, not a way around one: an unsafe value cannot
// survive it. Use it for constants and test fixtures; use ParsePortalIdentity
// for anything a user typed.
func MustPortal(host string) Portal {
	p, err := ParsePortalIdentity(host)
	if err != nil {
		panic(fmt.Sprintf("portal %q does not parse: %v", host, err))
	}
	if p.host != host {
		panic(fmt.Sprintf("portal %q is not in normalized form (got %q)", host, p.host))
	}
	return p
}

// parseHost reduces a portal value to hostname form: scheme stripped,
// lowercased, trailing slash removed, port preserved for dev portals.
func parseHost(v string) (Portal, error) {
	p := strings.TrimSpace(v)
	if p == "" {
		return Portal{}, fmt.Errorf("portal is empty")
	}
	if !strings.Contains(p, "://") {
		p = "https://" + p
	}
	u, err := url.Parse(p)
	if err != nil {
		return Portal{}, fmt.Errorf("invalid portal %q: %w", v, err)
	}
	host := strings.ToLower(u.Host)
	if host == "" {
		return Portal{}, fmt.Errorf("invalid portal %q: no host", v)
	}
	if err := checkHostSyntax(host); err != nil {
		return Portal{}, fmt.Errorf("invalid portal %q: %w", v, err)
	}
	return Portal{host: host}, nil
}

// hostSyntax is a hostname or dotted IPv4 literal: it must start and end
// alphanumeric, so "." and ".." and anything leading with a separator are out.
// Underscore is excluded on purpose: Portal.Folder encodes the port separator as
// "_", so a host containing one would decode back to a different host.
var hostSyntax = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// checkHostSyntax rejects anything not shaped like a host. This is what makes a
// portal safe to use as a path component: url.Parse happily reports "." and ".."
// as the host of "https://../../escaped", and Folder passes them through, so a
// dataset ref like "../wildfire" would otherwise resolve outside the data root
// and reach MkdirAll and os.RemoveAll. Checking here covers every caller at
// once, which is the point of there being one parser.
func checkHostSyntax(host string) error {
	h := host
	if hostOnly, port, err := net.SplitHostPort(host); err == nil {
		// A trailing colon parses as an empty port. Left in, it would key a
		// credential and name a folder distinct from the same host without it,
		// and reach the auth URL as the malformed origin "http://localhost:".
		if port == "" {
			return fmt.Errorf("%q has an empty port", host)
		}
		h = hostOnly
	} else {
		h = unbracket(host)
	}
	if h == "" {
		return fmt.Errorf("no host")
	}
	if strings.HasPrefix(host, "[") {
		// A bracketed IPv6 literal survives Portal.Folder only as "[__1]", which
		// decodes back to something else again. Loopback development is served by
		// localhost and 127.0.0.1, so the literal buys nothing worth an
		// unrepresentable folder name. Server origins are unaffected: they are
		// URLs, never paths, and ValidateServerURL still accepts "http://[::1]".
		return fmt.Errorf("%q is an IPv6 literal; use localhost or 127.0.0.1", host)
	}
	if !hostSyntax.MatchString(h) {
		return fmt.Errorf("%q is not a hostname", host)
	}
	return nil
}

// UnshapedHostError is a portal that parses as a host but is a single label that
// is not loopback. Once the environment aliases exist, "stagng" is a misspelled
// alias far more often than a portal, and nothing downstream would catch it: the
// value would key a credential, name a dataset folder, and reach the auth URL as
// "https://stagng". The parsed portal travels on the error so auth can still
// accept one that a stored credential vouches for.
type UnshapedHostError struct{ Portal Portal }

func (e *UnshapedHostError) Error() string {
	return fmt.Sprintf("unknown environment %q: expected one of %s (use a full hostname for a portal)",
		e.Portal.Host(), strings.Join(environmentNames, ", "))
}

// checkPortalShape passes a dotted name, an IP literal, and loopback, so only
// the spellings that cannot be a real portal are turned away.
func checkPortalShape(p Portal) error {
	h := splitHostPort(p.host)
	if strings.Contains(h, ".") || strings.Contains(h, ":") || isLoopback(h) {
		return nil
	}
	return &UnshapedHostError{Portal: p}
}

// Environment is one environment alias: a portal and the report server it is
// operationally paired with. Naming the pair in one place is what keeps a
// staging portal from being pointed at the production server by accident.
type Environment struct {
	Portal Portal
	Server string
}

// The three environments. Each is spelled several ways below; the pair itself
// is defined once here so the spellings cannot drift apart.
var (
	// mustHost runs as this package initializes, so a table entry that is
	// misspelled or not already in normalized form fails the first time any
	// command or test binary runs rather than at login.
	envProduction  = Environment{Portal: mustHost(ProductionPortal), Server: DefaultServerURL}
	envStaging     = Environment{Portal: mustHost(StagingPortal), Server: StagingServer}
	envDevelopment = Environment{Portal: mustHost(DevPortal), Server: DevServer}
)

// mustHost is MustPortal without the alias check, which the table cannot use
// because the alias check reads the table.
func mustHost(host string) Portal {
	p, err := parseHost(host)
	if err != nil {
		panic(fmt.Sprintf("environment portal %q does not parse: %v", host, err))
	}
	if p.host != host {
		panic(fmt.Sprintf("environment portal %q is not in normalized form (got %q)", host, p.host))
	}
	return p
}

// environments maps every accepted spelling to its portal/server pair. Names
// are matched case-insensitively. Both the short and the long form of each
// environment are accepted so nobody has to remember which one this CLI picked.
var environments = map[string]Environment{
	"prod":        envProduction,
	"production":  envProduction,
	"stage":       envStaging,
	"staging":     envStaging,
	"dev":         envDevelopment,
	"development": envDevelopment,
}

// environmentNames lists the canonical (short) alias names in presentation
// order, for help text and error messages; map iteration order would shuffle
// them, and listing every synonym would bury the three real choices.
var environmentNames = []string{"prod", "staging", "dev"}

// EnvironmentNames returns the canonical alias names in a stable order. The
// longer spellings are accepted by LookupEnvironment but deliberately left out
// of help text.
func EnvironmentNames() []string {
	return append([]string(nil), environmentNames...)
}

// LookupEnvironment resolves an alias name to its portal/server pair. Any value
// that is not an alias (a full host or URL) reports false.
func LookupEnvironment(name string) (Environment, bool) {
	env, ok := environments[strings.ToLower(strings.TrimSpace(name))]
	return env, ok
}

// environmentsByPortal indexes the same pairs by portal, so a portal spelled out
// in full can find the server it belongs with. It is derived from environments
// rather than restated, so a new environment cannot end up with aliases that
// resolve but a hostname that pairs with nothing. A collision panics as this
// package initializes, which is the first thing any command or test binary does,
// rather than letting a login pick a different server on different runs.
var environmentsByPortal = func() map[Portal]Environment {
	m, err := buildEnvironmentsByPortal(environments)
	if err != nil {
		panic(err)
	}
	return m
}()

// buildEnvironmentsByPortal inverts an alias table onto its portals. Two
// environments sharing a portal but not a server would make the winner depend on
// map iteration order, so that is an error rather than a silent pick. It takes
// the table as an argument so the collision can be exercised directly; the
// package-level index cannot be, since a colliding table would panic before any
// test runs.
func buildEnvironmentsByPortal(envs map[string]Environment) (map[Portal]Environment, error) {
	m := make(map[Portal]Environment, len(envs))
	for _, env := range envs {
		if prior, ok := m[env.Portal]; ok && prior != env {
			return nil, fmt.Errorf("portal %q is claimed by two environments with different servers (%q and %q)",
				env.Portal.Host(), prior.Server, env.Server)
		}
		m[env.Portal] = env
	}
	return m, nil
}

// EnvironmentForPortal finds the environment a portal belongs to. This is the
// reverse of LookupEnvironment: it keys on the portal rather than the alias
// spelling, so "--portal learn.portal.staging.concord.org" can be paired with the
// staging report server exactly as "--portal staging" is.
func EnvironmentForPortal(p Portal) (Environment, bool) {
	env, ok := environmentsByPortal[p]
	return env, ok
}

// ExpandServerAlias expands an environment alias to its report-server origin,
// returning any other value unchanged. Portal and server aliases expand
// independently, which is what makes a mixed "--portal staging --server dev"
// meaningful. The result still goes through
// ValidateServerURL, so an alias is not a way around the origin allowlist.
func ExpandServerAlias(v string) string {
	if env, ok := LookupEnvironment(v); ok {
		return env.Server
	}
	return v
}

func hasHTTPSScheme(v string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "https://")
}

// isLoopbackHostPort reports whether a host that may carry a port is loopback.
func isLoopbackHostPort(host string) bool {
	return isLoopback(strings.ToLower(splitHostPort(host)))
}

// splitHostPort returns the host without its port. A bracketed IPv6 literal
// loses its brackets either way, so "[::1]" and "[::1]:3000" both reduce to the
// "::1" the loopback check compares against. Only a properly bracketed literal
// is unwrapped: a stray bracket leaves the host as it was found rather than
// trimming it into something that looks loopback to the caller.
func splitHostPort(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return unbracket(host)
	}
	return h
}

func unbracket(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}
