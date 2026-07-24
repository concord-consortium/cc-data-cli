package cmd

import (
	"os"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
)

// testEnv builds the positional environment argument for a case.
func testEnv(t *testing.T, name string) *config.Environment {
	t.Helper()
	e, ok := config.LookupEnvironment(name)
	if !ok {
		t.Fatalf("no environment named %q", name)
	}
	return &e
}

func TestResolveLoginTarget(t *testing.T) {
	staging := &config.Config{DefaultPortal: config.StagingPortal}
	cases := []struct {
		name       string
		cfg        *config.Config
		env        *config.Environment
		portalFlag string
		serverFlag string
		wantPortal string
		wantServer string
		// wantOrigin defaults to https + wantPortal; set it only where the
		// scheme is the point of the case.
		wantOrigin string
		// wantIgnored is the server_url the pairing outranked, "" where nothing
		// was overridden. Every spelling of a portal has to agree on this as
		// well as on the server, so it is pinned case by case rather than only
		// where an override happens.
		wantIgnored string
	}{
		{
			name:       "no argument, no config: production defaults",
			cfg:        &config.Config{},
			wantPortal: config.ProductionPortal,
			wantServer: config.DefaultServerURL,
		},
		{
			// The paired server follows the resolved host, so a default_portal
			// spelled out in full still lands on that portal's own server.
			name:       "no argument: configured default_portal pairs its server",
			cfg:        staging,
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "a full staging hostname pairs the staging server",
			cfg:        &config.Config{},
			portalFlag: config.StagingPortal,
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "a full staging URL pairs the staging server",
			cfg:        &config.Config{},
			portalFlag: "https://learn.portal.staging.concord.org/",
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			// An unrecognized portal has nothing to pair with and falls back.
			name:       "an unknown portal falls back to the default server",
			cfg:        &config.Config{},
			portalFlag: "learn.example.concord.org",
			wantPortal: "learn.example.concord.org",
			wantServer: config.DefaultServerURL,
		},
		{
			// ...unless an environment was named outright, which is what keeps a
			// dev portal on a non-default port from silently exchanging its code
			// at the production report server.
			name:       "a named environment serves a portal it does not claim",
			cfg:        &config.Config{},
			env:        testEnv(t, "dev"),
			portalFlag: "localhost:3005",
			wantPortal: "localhost:3005",
			wantServer: config.DevServer,
			wantOrigin: "http://localhost:3005",
		},
		{
			name:       "a named environment serves an unclaimed hostname",
			cfg:        &config.Config{},
			env:        testEnv(t, "staging"),
			portalFlag: "learn.example.concord.org",
			wantPortal: "learn.example.concord.org",
			wantServer: config.StagingServer,
		},
		{
			// The named environment outranks server_url the same way a pairing
			// does, so the override is reported rather than passed over.
			name:        "a named environment beats server_url for an unclaimed portal",
			cfg:         &config.Config{ServerURL: config.StagingServer},
			env:         testEnv(t, "prod"),
			portalFlag:  "learn.example.concord.org",
			wantPortal:  "learn.example.concord.org",
			wantServer:  config.DefaultServerURL,
			wantIgnored: config.StagingServer,
		},
		{
			// A known portal brings its own server, outranking server_url...
			name:        "the pairing beats configured server_url",
			cfg:         &config.Config{ServerURL: config.StagingServer},
			portalFlag:  config.ProductionPortal,
			wantPortal:  config.ProductionPortal,
			wantServer:  config.DefaultServerURL,
			wantIgnored: config.StagingServer,
		},
		{
			// ...in the alias spelling of that same portal...
			name:        "the pairing beats server_url for a --portal alias too",
			cfg:         &config.Config{ServerURL: config.StagingServer},
			portalFlag:  "prod",
			wantPortal:  config.ProductionPortal,
			wantServer:  config.DefaultServerURL,
			wantIgnored: config.StagingServer,
		},
		{
			// ...and for an environment named outright. All three spellings of
			// a portal must land on the same server in every configuration, and
			// report the override the same way: the positional form is the one
			// most people type, so it cannot be the one that stays silent.
			name:        "the pairing beats server_url for a named environment",
			cfg:         &config.Config{ServerURL: config.StagingServer},
			env:         testEnv(t, "prod"),
			wantPortal:  config.ProductionPortal,
			wantServer:  config.DefaultServerURL,
			wantIgnored: config.StagingServer,
		},
		{
			// server_url keeps the job no environment covers: a portal that has
			// no paired server of its own.
			name:       "configured server_url serves a portal no environment claims",
			cfg:        &config.Config{ServerURL: config.StagingServer},
			portalFlag: "learn.example.concord.org",
			wantPortal: "learn.example.concord.org",
			wantServer: config.StagingServer,
		},
		{
			name:       "positional staging pairs portal and server",
			cfg:        &config.Config{},
			env:        testEnv(t, "staging"),
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "positional prod matches the bare-login default",
			cfg:        &config.Config{},
			env:        testEnv(t, "prod"),
			wantPortal: config.ProductionPortal,
			wantServer: config.DefaultServerURL,
		},
		{
			name:       "positional dev uses the loopback pair",
			cfg:        &config.Config{},
			env:        testEnv(t, "dev"),
			wantPortal: config.DevPortal,
			wantServer: config.DevServer,
			wantOrigin: "http://" + config.DevPortal,
		},
		{
			// An explicit https on a loopback portal survives to the auth URL;
			// the stored portal identity stays the bare host either way.
			name:       "an https loopback portal keeps its scheme",
			cfg:        &config.Config{ServerURL: config.DevServer},
			portalFlag: "https://localhost:3001",
			wantPortal: "localhost:3001",
			wantServer: config.DevServer,
			wantOrigin: "https://localhost:3001",
		},
		{
			name:       "positional wins over default_portal",
			cfg:        &config.Config{DefaultPortal: config.ProductionPortal},
			env:        testEnv(t, "staging"),
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "--portal alias carries its paired server",
			cfg:        &config.Config{},
			portalFlag: "staging",
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "--server alias sets only the server",
			cfg:        &config.Config{},
			serverFlag: "staging",
			wantPortal: config.ProductionPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "mixed --portal staging --server dev",
			cfg:        &config.Config{},
			portalFlag: "staging",
			serverFlag: "dev",
			wantPortal: config.StagingPortal,
			wantServer: config.DevServer,
		},
		{
			name:       "flags override the positional environment",
			cfg:        &config.Config{},
			env:        testEnv(t, "prod"),
			portalFlag: "staging",
			serverFlag: "dev",
			wantPortal: config.StagingPortal,
			wantServer: config.DevServer,
		},
		{
			name:       "--portal overrides the positional pair, server included",
			cfg:        &config.Config{},
			env:        testEnv(t, "prod"),
			portalFlag: "staging",
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "full hostname and URL still work",
			cfg:        &config.Config{},
			portalFlag: "https://learn.portal.staging.concord.org/",
			serverFlag: "https://report-server.concordqa.org",
			wantPortal: config.StagingPortal,
			wantServer: config.StagingServer,
		},
		{
			name:       "a literal --server wins over the --portal alias pair",
			cfg:        &config.Config{},
			portalFlag: "staging",
			serverFlag: "https://report-server.concord.org",
			wantPortal: config.StagingPortal,
			wantServer: config.DefaultServerURL,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveLoginTarget(c.cfg, c.env, c.portalFlag, c.serverFlag)
			if err != nil {
				t.Fatal(err)
			}
			if got.portal.Host() != c.wantPortal {
				t.Errorf("portal = %q, want %q", got.portal.Host(), c.wantPortal)
			}
			if got.server != c.wantServer {
				t.Errorf("server = %q, want %q", got.server, c.wantServer)
			}
			wantOrigin := c.wantOrigin
			if wantOrigin == "" {
				wantOrigin = "https://" + c.wantPortal
			}
			if got.portalOrigin != wantOrigin {
				t.Errorf("portalOrigin = %q, want %q", got.portalOrigin, wantOrigin)
			}
			if got.ignoredServerURL != c.wantIgnored {
				t.Errorf("ignoredServerURL = %q, want %q", got.ignoredServerURL, c.wantIgnored)
			}
		})
	}
}

// TestResolveLoginTargetReportsNoSpuriousOverride covers the cases that must
// stay silent about server_url. The cases that do report an override are pinned
// by wantIgnored in the table above, once per spelling of the portal.
func TestResolveLoginTargetReportsNoSpuriousOverride(t *testing.T) {
	silent := []struct {
		name       string
		cfg        *config.Config
		env        *config.Environment
		portalFlag string
		serverFlag string
	}{
		// Nothing was configured, so nothing was overridden.
		{"no server_url", &config.Config{}, nil, config.ProductionPortal, ""},
		{"no server_url, positional", &config.Config{}, testEnv(t, "prod"), "", ""},
		// server_url already agrees with the pairing.
		{"server_url matches the pairing", &config.Config{ServerURL: config.StagingServer}, nil, config.StagingPortal, ""},
		{"server_url matches the pairing, positional", &config.Config{ServerURL: config.StagingServer}, testEnv(t, "staging"), "", ""},
		// server_url was used, not overridden: this portal has no environment.
		{"server_url is the one used", &config.Config{ServerURL: config.StagingServer}, nil, "learn.example.concord.org", ""},
		// An explicit --server is the user's own choice, not an override.
		{"explicit --server", &config.Config{ServerURL: config.StagingServer}, nil, config.ProductionPortal, config.DefaultServerURL},
		{"explicit --server, positional", &config.Config{ServerURL: config.StagingServer}, testEnv(t, "prod"), "", config.DefaultServerURL},
	}
	for _, c := range silent {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveLoginTarget(c.cfg, c.env, c.portalFlag, c.serverFlag)
			if err != nil {
				t.Fatal(err)
			}
			if got.ignoredServerURL != "" {
				t.Errorf("ignoredServerURL = %q, want no override reported", got.ignoredServerURL)
			}
		})
	}
}

// TestResolveLoginTargetRejectsAliasTypo pins that a near-miss alias on --portal
// is an error rather than a portal host of its own; the positional slot already
// refused it, and the flag must not be the way around that.
func TestResolveLoginTargetRejectsAliasTypo(t *testing.T) {
	if _, err := resolveLoginTarget(&config.Config{}, nil, "stagng", ""); err == nil {
		t.Fatal("a misspelled alias on --portal should be rejected")
	}
}

// TestResolveLoginTargetRejectsDisallowedServer checks that a literal --server
// is still allowlist-checked after alias expansion. The complementary guarantee,
// that no alias can expand to a disallowed origin in the first place, comes from
// config.TestEnvironmentPairsAreValid.
func TestResolveLoginTargetRejectsDisallowedServer(t *testing.T) {
	if _, err := resolveLoginTarget(&config.Config{}, nil, "", "https://evil.example.com"); err == nil {
		t.Fatal("a non-allowlisted server origin should be rejected")
	}
}

func TestResolveEnvironmentArg(t *testing.T) {
	got, err := resolveEnvironmentArg(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("no argument should resolve to no environment, got %+v", got)
	}

	got, err = resolveEnvironmentArg([]string{"STAGING"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Portal.Host() != config.StagingPortal {
		t.Fatalf("alias should match case-insensitively, got %+v", got)
	}

	// A hostname in the positional slot is a typo, not a portal.
	if _, err := resolveEnvironmentArg([]string{"learn.concord.org"}); err == nil {
		t.Fatal("an unknown environment should be a usage error")
	}
}

func withStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })
	go func() {
		w.WriteString(content)
		w.Close()
	}()
}

func TestResolveTokenStdin(t *testing.T) {
	withStdin(t, "ccd_piped_token\n")
	got, err := resolveToken(nil, "-")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ccd_piped_token" {
		t.Fatalf("stdin token = %q", got)
	}
}

func TestResolveTokenDirect(t *testing.T) {
	got, err := resolveToken(nil, "ccd_direct")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ccd_direct" {
		t.Fatalf("direct token = %q", got)
	}
}

func TestResolveTokenEmptyMeansPKCE(t *testing.T) {
	got, err := resolveToken(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("empty flag should mean PKCE, got %q", got)
	}
}
