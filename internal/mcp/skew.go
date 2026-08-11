package mcp

import (
	"os"
	"time"

	"github.com/agenxy/lanes/internal/build"
)

// daemonStarted is when this process began serving.
//
// It exists because the version string cannot answer the question people
// actually have. In development both sides read `devel`, so comparing them
// says nothing, and the failure that keeps happening is not a version mismatch
// at all: it is a daemon that is still running code from before the last build.
// `lanes` and `lanesd` are separate processes, and installing a new binary does
// not restart the one already serving, so a fix can be built, installed, and
// completely absent from every answer the board gives, with no error anywhere
// and nothing on either side that disagrees.
//
// That cost real time in this project more than once, most expensively when a
// daemon predating a panel fix kept serving the old template while the
// repository, the tests and the freshly installed binary all agreed the fix was
// in. A timestamp makes it a subtraction: if the binary on disk is newer than
// the process serving, the process is stale.
var daemonStarted = time.Now()

// serverBuildInfo is what the daemon says about itself, beyond a version.
func serverBuildInfo() map[string]any {
	info := map[string]any{
		"name":       "lanes",
		"version":    build.Version,
		"started_at": daemonStarted.UTC().Format(time.RFC3339),
		// The panel's content hash, so "which panel is this daemon serving" is
		// answerable without rendering anything. A screenshot could not answer it;
		// this can.
		"panel_build": panelBuild,
	}
	// The mtime of the binary this process is RUNNING, which is not necessarily
	// the one now on disk at that path: replacing a file does not change what an
	// already-started process executes. Reported so a client can subtract.
	if self, err := os.Executable(); err == nil {
		if st, serr := os.Stat(self); serr == nil {
			info["binary_mtime"] = st.ModTime().UTC().Format(time.RFC3339)
			info["binary_path"] = self
		}
	}
	return info
}
