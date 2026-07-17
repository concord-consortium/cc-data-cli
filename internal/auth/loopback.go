package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"time"
)

const successPage = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>cc-data</title></head>
<body style="font-family:sans-serif"><h1>Login complete</h1><p>You can return to your terminal.</p></body></html>`

const errorPage = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>cc-data</title></head>
<body style="font-family:sans-serif"><h1>Login error</h1><p>This callback did not match the pending login. You can close this window.</p></body></html>`

// Loopback is the localhost callback listener for the PKCE flow.
type Loopback struct {
	listener net.Listener
	server   *http.Server
	port     int
	state    string
	codeCh   chan string
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
		codeCh:   make(chan string, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", lb.handleCallback)
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
	code := q.Get("code")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, successPage)
	select {
	case l.codeCh <- code:
	default:
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
	case code := <-l.codeCh:
		return code, nil
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
