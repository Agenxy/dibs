package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bundle holds the macOS server, launched by a manifest the MCPB spec can
// read, with the executable bit kept: an agent that installs it has a
// runnable Dibs and not a name to guess from. Issue #44. And it carries NO
// other platform: a manifest selects by operating system, so a Linux entry
// would hand an arm64 host the amd64 build, and the compatibility list must
// not promise what the bundle cannot run.
func TestTheBundleCarriesARunnableServerPerPlatform(t *testing.T) {
	dist := t.TempDir()
	for _, dir := range []string{"dibs_darwin_arm64_v8.0", "dibd_darwin_arm64_v8.0", "dibs_linux_amd64_v1", "dibd_linux_amd64_v1"} {
		bin := strings.SplitN(dir, "_", 2)[0]
		if err := os.MkdirAll(filepath.Join(dist, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, dir, bin), []byte("#!fake "+dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dist, "dibs.mcpb")
	sum, err := Build(dist, "0.0.8", out)
	if err != nil {
		t.Fatal(err)
	}
	// The digest is checked against the archive, not against itself.
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	real := sha256.Sum256(raw)
	if want := hex.EncodeToString(real[:]); sum != want {
		t.Errorf("digest = %q, want the sha256 of the bundle written, %s", sum, want)
	}
	if side, err := os.ReadFile(out + ".sha256"); err != nil || string(side) != sum+"  dibs.mcpb\n" {
		t.Errorf("the .sha256 beside the bundle = %q %v, want %q", side, err, sum+"  dibs.mcpb\n")
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	got := map[string]os.FileMode{}
	body := map[string]string{}
	for _, f := range zr.File {
		got[f.Name] = f.Mode()
		rc, _ := f.Open()
		b, _ := readAll(rc)
		_ = rc.Close()
		body[f.Name] = string(b)
	}
	manifest := []byte(body["manifest.json"])
	// The bytes under each path are THAT platform's build: the fixtures are
	// distinguishable so that a Linux binary filed under the macOS path
	// would be caught, which paths and modes alone do not do.
	for path, want := range map[string]string{
		"server/darwin-arm64/dibs": "#!fake dibs_darwin_arm64_v8.0",
		"server/darwin-arm64/dibd": "#!fake dibd_darwin_arm64_v8.0",
	} {
		if got[path]&0o111 == 0 {
			t.Errorf("%s: mode %v, want the executable bit kept (present: %v)", path, got[path], got[path] != 0)
		}
		if body[path] != want {
			t.Errorf("%s holds %q, want the darwin arm64 build %q", path, body[path], want)
		}
	}
	for name := range got {
		if strings.HasPrefix(name, "server/") && !strings.HasPrefix(name, "server/darwin-arm64/") {
			t.Errorf("the bundle carries %s: a manifest cannot select by architecture, so any "+
				"Linux binary in it is the wrong one for half of Linux", name)
		}
	}
	var m struct {
		ManifestVersion string `json:"manifest_version"`
		Version         string `json:"version"`
		Server          struct {
			Type      string `json:"type"`
			MCPConfig struct {
				Command   string                    `json:"command"`
				Args      []string                  `json:"args"`
				Overrides map[string]map[string]any `json:"platform_overrides"`
			} `json:"mcp_config"`
		} `json:"server"`
		Compatibility struct {
			Platforms []string `json:"platforms"`
		} `json:"compatibility"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.ManifestVersion != "0.2" || m.Version != "0.0.8" || m.Server.Type != "binary" {
		t.Errorf("manifest = %+v, want a 0.2 binary manifest at 0.0.8", m)
	}
	if !strings.Contains(m.Server.MCPConfig.Command, "${__dirname}/server/darwin-arm64/dibs") ||
		len(m.Server.MCPConfig.Args) != 1 || m.Server.MCPConfig.Args[0] != "mcp-stdio" {
		t.Errorf("mcp_config = %+v, want the bridge launched from inside the bundle", m.Server.MCPConfig)
	}
	if len(m.Server.MCPConfig.Overrides) != 0 {
		t.Errorf("platform_overrides = %v, want none: every override is a platform this bundle "+
			"would run the wrong binary on", m.Server.MCPConfig.Overrides)
	}
	if len(m.Compatibility.Platforms) != 1 || m.Compatibility.Platforms[0] != "darwin" {
		t.Errorf("compatibility.platforms = %v, want darwin alone, so a host that cannot run "+
			"the bundle is told before it installs it", m.Compatibility.Platforms)
	}
	// A dist with a binary missing is a refusal, not a bundle with a hole,
	// and a failed rebuild leaves the previous bundle and digest in place
	// rather than a truncated file at the name the release uploads.
	if err := os.RemoveAll(filepath.Join(dist, "dibd_darwin_arm64_v8.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(dist, "0.0.8", out); err == nil {
		t.Error("a dist missing dibd produced a bundle")
	}
	if again, err := os.ReadFile(out); err != nil || string(again) != string(raw) {
		t.Errorf("a failed rebuild disturbed the bundle at %s (%d bytes, %v; had %d)", out, len(again), err, len(raw))
	}
	if left, _ := filepath.Glob(filepath.Join(dist, ".mcpbundle-*")); len(left) != 0 {
		t.Errorf("a failed build left its temporary file behind: %v", left)
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}
