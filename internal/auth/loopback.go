package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// pageTemplate is the shared shell for the login result pages, styled to match
// the report-server (light-teal background, orange top accent, brand-teal
// heading, and the server's own logo served at /logo.png). It is built only from
// static strings, so no response page ever reflects query values.
const pageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>cc-data · Report Server</title>
<style>
:root { color-scheme: light; }
* { box-sizing: border-box; }
body {
  margin: 0; min-height: 100vh;
  display: flex; align-items: center; justify-content: center;
  background: #b7e2ec;
  font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  color: #18181b;
}
.card {
  width: 420px; max-width: calc(100% - 32px);
  background: #fff; border-radius: 12px;
  border-top: 4px solid #ea6d2f;
  box-shadow: 0 10px 30px rgba(0,0,0,.12);
  overflow: hidden;
}
.brand {
  display: flex; align-items: center; gap: 12px;
  padding: 18px 28px; border-bottom: 1px solid #eef2f4;
}
.brand img { width: 36px; height: 36px; display: block; }
.brand .name { font-weight: 700; font-size: 18px; line-height: 1.15; }
.brand .sub { font-size: 12px; color: #6b7280; margin-top: 2px; }
.content { padding: 26px 28px 30px; }
.content h1 { margin: 0 0 8px; font-size: 22px; color: {{HEADING_COLOR}}; }
.content p { margin: 0; font-size: 15px; line-height: 1.55; color: #374151; }
</style>
</head>
<body>
<main class="card">
  <div class="brand">
    <img src="/logo.png" alt="Report Server logo" width="36" height="36">
    <div>
      <div class="name">Report Server</div>
      <div class="sub">cc-data CLI</div>
    </div>
  </div>
  <div class="content">
    <h1>{{HEADING}}</h1>
    <p>{{MESSAGE}}</p>
  </div>
</main>
</body>
</html>`

// authPage renders the shared shell with a heading colour, heading, and message.
func authPage(headingColor, heading, message string) string {
	return strings.NewReplacer(
		"{{HEADING_COLOR}}", headingColor,
		"{{HEADING}}", heading,
		"{{MESSAGE}}", message,
	).Replace(pageTemplate)
}

var successPage = authPage("#0592af", "Login complete", "You can return to your terminal.")

var errorPage = authPage("#ea6d2f", "Login error", "The login did not complete. You can close this window and return to your terminal.")

// Loopback is the localhost callback listener for the PKCE flow.
type Loopback struct {
	listener net.Listener
	server   *http.Server
	port     int
	state    string
	resultCh chan callbackResult
}

// callbackResult carries the outcome of a state-matched callback: a code, or an
// error describing why the login failed (an OAuth error param or a missing code).
type callbackResult struct {
	code string
	err  error
}

// StartLoopback binds 127.0.0.1 on a random port and serves the /callback route
// until a callback whose state matches arrives (or Wait's timeout fires). A
// mismatched callback is answered with a static error page and neither stops the
// listener nor consumes anything, so a local process racing junk to the port
// cannot break the login. Neither response page reflects query values.
func StartLoopback(state string) (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	lb := &Loopback{
		listener: ln,
		port:     ln.Addr().(*net.TCPAddr).Port,
		state:    state,
		resultCh: make(chan callbackResult, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", lb.handleCallback)
	mux.HandleFunc("/logo.png", serveLogo)
	lb.server = &http.Server{Handler: mux}
	go lb.server.Serve(ln)
	return lb, nil
}

func (l *Loopback) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	gotState := q.Get("state")
	if subtle.ConstantTimeCompare([]byte(gotState), []byte(l.state)) != 1 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, errorPage)
		return
	}
	// The state matched, so this is the real authorization server responding.
	// An OAuth error param, or a callback with no code, is a genuine login
	// failure: show the error page and fail Wait fast instead of claiming
	// "Login complete" and then hanging until the timeout.
	code := q.Get("code")
	if oauthErr := q.Get("error"); oauthErr != "" || code == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, errorPage)
		l.deliver(callbackResult{err: callbackError(oauthErr, q.Get("error_description"))})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, successPage)
	l.deliver(callbackResult{code: code})
}

// serveLogo serves the bundled Report Server logo referenced by the result pages.
func serveLogo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(logoAsset)
}

// deliver posts the first matched result without blocking the handler.
func (l *Loopback) deliver(res callbackResult) {
	select {
	case l.resultCh <- res:
	default:
	}
}

// callbackError builds a clear failure message from the OAuth error params.
func callbackError(oauthErr, desc string) error {
	switch {
	case oauthErr != "" && desc != "":
		return fmt.Errorf("authorization server returned error %q: %s", oauthErr, desc)
	case oauthErr != "":
		return fmt.Errorf("authorization server returned error %q", oauthErr)
	default:
		return fmt.Errorf("authorization callback contained no code")
	}
}

// RedirectURI is the exact callback URI the server validates against.
func (l *Loopback) RedirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback", l.port)
}

// Wait blocks until a matching callback delivers a code, the timeout elapses, or
// the context is cancelled.
func (l *Loopback) Wait(ctx context.Context, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-l.resultCh:
		return res.code, res.err
	case <-timer.C:
		return "", fmt.Errorf("timed out after %s waiting for the login callback", timeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close shuts down the listener.
func (l *Loopback) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	l.server.Shutdown(ctx)
}
