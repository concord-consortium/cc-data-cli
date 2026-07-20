package auth

import (
	"io"
	"os"

	"github.com/pkg/browser"
)

// openBrowser is a seam so tests can follow the auth URL themselves.
var openBrowser = browser.OpenURL

// RedirectBrowserOutput points the browser-launcher's stdout/stderr at w, so
// xdg-open chatter never lands on the CLI's stdout and breaks the one-JSON-line
// stream contract. Call before opening the browser.
func RedirectBrowserOutput(w io.Writer) {
	browser.Stdout = w
	browser.Stderr = w
}

// OpenBrowser opens rawURL in the user's browser via the seam. Setting
// CC_DATA_NO_BROWSER suppresses the open so a driver (the e2e script) can follow
// the printed URL itself.
func OpenBrowser(rawURL string) error {
	if os.Getenv("CC_DATA_NO_BROWSER") != "" {
		return nil
	}
	return openBrowser(rawURL)
}
