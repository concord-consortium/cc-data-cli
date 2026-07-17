package auth

import (
	"context"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoopbackStateMismatchThenGenuine(t *testing.T) {
	lb, err := StartLoopback("goodstate")
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()

	// A bad-state callback is answered but does not consume or stop the listener.
	badResp, err := http.Get(lb.RedirectURI() + "?state=bad&code=evil")
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatch status = %d", badResp.StatusCode)
	}

	// The genuine callback still succeeds afterward.
	go func() {
		time.Sleep(10 * time.Millisecond)
		resp, _ := http.Get(lb.RedirectURI() + "?state=goodstate&code=realcode")
		if resp != nil {
			resp.Body.Close()
		}
	}()
	code, err := lb.Wait(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != "realcode" {
		t.Fatalf("code = %q, want realcode", code)
	}
}

func TestLoopbackTimeout(t *testing.T) {
	lb, err := StartLoopback("s")
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	_, err = lb.Wait(context.Background(), 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRedirectURIShape(t *testing.T) {
	lb, _ := StartLoopback("s")
	defer lb.Close()
	if !strings.HasPrefix(lb.RedirectURI(), "http://127.0.0.1:") || !strings.HasSuffix(lb.RedirectURI(), "/callback") {
		t.Fatalf("redirect_uri shape wrong: %s", lb.RedirectURI())
	}
}

// TestNoMathRandInAuth asserts no source file in internal/auth imports math/rand,
// which would pass every functional PKCE test while collapsing the security
// property.
func TestNoMathRandInAuth(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "math/rand" || strings.HasPrefix(strings.Trim(imp.Path.Value, `"`), "math/rand/") {
				t.Fatalf("%s imports %s; PKCE tokens must come from crypto/rand", e.Name(), imp.Path.Value)
			}
		}
	}
}
