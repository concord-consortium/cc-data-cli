package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
	"github.com/zalando/go-keyring"
)

// TestOriginRuleUsesCredentialServer proves the client base URL is the portal
// credential's recorded server, never the global config server_url.
func TestOriginRuleUsesCredentialServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	keyring.MockInit()

	// The credential's server is the one that actually serves the request.
	var hitCredServer bool
	credServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCredServer = true
		if r.Header.Get("Authorization") != "Bearer ccd_portalA" {
			t.Errorf("wrong bearer: %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{}`))
	}))
	defer credServer.Close()

	// config points somewhere else entirely; it must not be consulted.
	cfg := &config.Config{ServerURL: "https://report-server.concordqa.org"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	var store creds.Store
	if err := store.Save("learn.concord.org", "ccd_portalA", credServer.URL); err != nil {
		t.Fatal(err)
	}

	cl, err := ForPortal("learn.concord.org")
	if err != nil {
		t.Fatal(err)
	}
	if cl.BaseURL != credServer.URL {
		t.Fatalf("base URL = %q, want the credential server %q", cl.BaseURL, credServer.URL)
	}
	if err := cl.getJSON(context.Background(), "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !hitCredServer {
		t.Fatal("request did not go to the credential's server")
	}
}

func TestForPortalNotAuthedWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	keyring.MockInit()
	_, err := ForPortal("missing.concord.org")
	cliErr := err
	if cliErr == nil {
		t.Fatal("expected NOT_AUTHENTICATED")
	}
}
