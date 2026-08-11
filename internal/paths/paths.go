// Package paths resolves the Lanes data directory in one place, so the
// daemon and CLI can never disagree about which instance they mean.
package paths

import (
	"os"
	"path/filepath"
)

// DataDir returns the active data directory: LANES_DIR if set, else ~/.lanes.
//
// ~/.lanes, and never anywhere under Desktop, Documents or Downloads: those are
// TCC-protected on macOS, so a daemon reading them raises a folder-access
// prompt, and because TCC keys consent to the binary's identity, every rebuild
// invalidates the grant and asks again. A development instance belongs in
// LANES_DIR, not in a path compiled into the binary.
func DataDir() string {
	if d := os.Getenv("LANES_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lanes"
	}
	return filepath.Join(home, ".lanes")
}
