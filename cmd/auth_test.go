package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/auth"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/output"
)

// TestRenderStatusShowsServer pins the report server in the table, not only in
// --json. A run id belongs to a portal and to the server behind it, so this is
// the column that answers "am I pointed at the environment that has my run?".
func TestRenderStatusShowsServer(t *testing.T) {
	res := auth.StatusResult{
		Portals: []auth.PortalStatus{
			{Portal: config.StagingPortal, Server: config.StagingServer, Backend: "keychain", StoredAt: time.Unix(0, 0)},
			{Portal: config.DevPortal, Backend: "file", StoredAt: time.Unix(0, 0)},
		},
	}
	for _, check := range []bool{false, true} {
		var buf bytes.Buffer
		restore := output.SetStreams(&buf, &buf)
		renderStatus(res, check)
		restore()

		got := buf.String()
		lines := strings.Split(strings.TrimSpace(got), "\n")
		if len(lines) != 3 {
			t.Fatalf("check=%v: want a header and two rows, got:\n%s", check, got)
		}
		if !strings.Contains(lines[0], "SERVER") {
			t.Errorf("check=%v: header has no SERVER column: %q", check, lines[0])
		}
		if !strings.Contains(lines[1], config.StagingServer) {
			t.Errorf("check=%v: staging row omits its server: %q", check, lines[1])
		}
		// A credential with no recorded server reads as "-" rather than blank.
		if !strings.Contains(lines[2], " - ") {
			t.Errorf("check=%v: a serverless credential should show -: %q", check, lines[2])
		}
	}
}
