package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Main is the release candidate. Enter through the shipped help command so
// an incomplete adapter cannot become an accidental ready-to-use promise.
func TestGuestBridgeHelpNamesIncompleteFeature(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build fixture command: %v %s", err, out)
	}
	for _, args := range [][]string{
		{"mcp-stdio", "--help"},
		{"mcp-stdio", "--guest", "--help"},
	} {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("help failed: %v %s", err, out)
		}
		for _, fact := range []string{
			"INCOMPLETE", "not ready for guest use", "issuer-exported recipe",
			"not available yet", "No harness or cloud runtime has been accepted",
		} {
			if !strings.Contains(string(out), fact) {
				t.Errorf("help %v omits release boundary %q", args, fact)
			}
		}
	}
}
