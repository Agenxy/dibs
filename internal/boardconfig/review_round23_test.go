package boardconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dibs.toml that is a symlink to nothing is a broken configuration, not an
// absent one: defaults would replace the configured address.
func TestADanglingConfigSymlinkIsNotAnAbsentConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere.toml"), filepath.Join(dir, "dibs.toml")); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Load returned %v for a dangling dibs.toml: the defaults quietly replace the "+
			"configured address and the directory's secret goes to whatever answers there", err)
	}
	if _, err := Load(t.TempDir()); err != nil {
		t.Errorf("a directory with no dibs.toml at all is still the defaults: %v", err)
	}
}
