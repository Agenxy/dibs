package boardconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWakeSocketsIsASetting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake]\nsockets = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Wake.Sockets == nil || *cfg.Wake.Sockets {
		t.Fatal("[wake] sockets = false did not parse as off")
	}
	if cfg2, _ := Load(t.TempDir()); cfg2.Wake.Sockets != nil {
		t.Error("absent must read as the default, not as a stated value")
	}
}
