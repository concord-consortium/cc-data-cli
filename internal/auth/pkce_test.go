package auth

import (
	"regexp"
	"testing"
)

// RFC 7636 Appendix B verifier/challenge vector.
func TestChallengeRFC7636Vector(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := Challenge(verifier); got != want {
		t.Fatalf("Challenge = %q, want %q", got, want)
	}
}

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func TestGenerateVerifierShape(t *testing.T) {
	v, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 43 {
		t.Fatalf("verifier len = %d, want 43", len(v))
	}
	if !challengeRe.MatchString(Challenge(v)) {
		t.Fatalf("challenge %q does not match server regex", Challenge(v))
	}
}

func TestGenerateStateUnique(t *testing.T) {
	a, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateState()
	if a == b {
		t.Fatal("two states collided")
	}
	if a == "" {
		t.Fatal("empty state")
	}
}

func TestBuildAuthURL(t *testing.T) {
	u := buildAuthURL("https://report-server.concord.org", "learn.concord.org", "http://127.0.0.1:5000/callback", "st", "ch")
	for _, needle := range []string{
		"portal=https%3A%2F%2Flearn.concord.org",
		"redirect_uri=http%3A%2F%2F127.0.0.1%3A5000%2Fcallback",
		"state=st",
		"code_challenge=ch",
		"code_challenge_method=S256",
	} {
		if !regexp.MustCompile(regexp.QuoteMeta(needle)).MatchString(u) {
			t.Fatalf("auth URL missing %q: %s", needle, u)
		}
	}
}
