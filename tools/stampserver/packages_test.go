package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The registry entry names the bundle the release attached, with the digest
// of the bytes actually attached; publishes WITHOUT a packages block when the
// release has no bundle, stale block included; and refuses to publish at all
// when the release could not be examined or its digest file disagrees with
// its bundle. Issue #44, and the Codex review of #109, which found the first
// version reading the digest file on trust and calling every error "no
// asset".
func TestThePackagesBlockNamesTheReleasedBundleOrNothing(t *testing.T) {
	oldAssets, oldFetch := releaseAssets, fetchAsset
	t.Cleanup(func() { releaseAssets, fetchAsset = oldAssets, oldFetch })
	t.Setenv("GITHUB_REPOSITORY", "Agenxy/dibs")
	t.Setenv("GITHUB_REF_TYPE", "tag")
	t.Setenv("GITHUB_REF_NAME", "v0.0.8")

	dir := t.TempDir()
	path := filepath.Join(dir, "server.json")
	stale := `{"name":"io.github.Agenxy/dibs","version":"0.0.0",` +
		`"packages":[{"registryType":"mcpb","fileSha256":"stale","identifier":"old"}]}`
	write := func() {
		if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		raw, _ := os.ReadFile(path)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		return doc
	}
	bundle := []byte("PK\x03\x04 not really a zip, but these are the bytes the release holds")
	sum := sha256.Sum256(bundle)
	want := hex.EncodeToString(sum[:])
	serve := func(digestLine string) {
		fetchAsset = func(_, tag, name, dest string) error {
			if tag != "v0.0.8" {
				return errors.New("wrong tag " + tag)
			}
			switch name {
			case bundleAsset:
				return os.WriteFile(dest, bundle, 0o600)
			case digestAsset:
				return os.WriteFile(dest, []byte(digestLine), 0o600)
			}
			return errors.New("no such asset " + name)
		}
	}

	// The bundle and a digest file that agrees with it.
	var asked string
	releaseAssets = func(repo, tag string) ([]string, error) {
		asked = repo + "@" + tag
		return []string{"dibs_darwin_arm64.tar.gz", bundleAsset, digestAsset}, nil
	}
	serve(want + "  dibs.mcpb\n")
	write()
	if err := run(path, ""); err != nil {
		t.Fatal(err)
	}
	doc := read()
	if asked != "Agenxy/dibs@v0.0.8" {
		t.Errorf("asked %q for the assets, want the tag of the version being published", asked)
	}
	pkgs, _ := doc["packages"].([]any)
	if len(pkgs) != 1 {
		t.Fatalf("packages = %v, want one mcpb entry", doc["packages"])
	}
	pkg := pkgs[0].(map[string]any)
	if pkg["registryType"] != "mcpb" || pkg["fileSha256"] != want || pkg["version"] != "0.0.8" ||
		pkg["identifier"] != "https://github.com/Agenxy/dibs/releases/download/v0.0.8/dibs.mcpb" {
		t.Errorf("package = %v, want the digest of the bytes attached (%s)", pkg, want)
	}
	if tr, _ := pkg["transport"].(map[string]any); tr["type"] != "stdio" {
		t.Errorf("transport = %v, want stdio", pkg["transport"])
	}

	// The bundle alone, no digest file: still the digest of the bytes.
	releaseAssets = func(string, string) ([]string, error) { return []string{bundleAsset}, nil }
	write()
	if err := run(path, ""); err != nil {
		t.Fatal(err)
	}
	if pkgs, _ := read()["packages"].([]any); len(pkgs) != 1 || pkgs[0].(map[string]any)["fileSha256"] != want {
		t.Errorf("without a digest file, packages = %v, want the bundle hashed anyway", read()["packages"])
	}

	// A digest file that DISAGREES with the bundle: not published.
	releaseAssets = func(string, string) ([]string, error) { return []string{bundleAsset, digestAsset}, nil }
	serve("deadbeef  dibs.mcpb\n")
	write()
	if err := run(path, ""); err == nil {
		t.Error("a release whose .sha256 disagrees with its bundle was stamped")
	}
	if doc := read(); doc["version"] != "0.0.0" {
		t.Errorf("the manifest was rewritten (%v) on a refused stamp", doc["version"])
	}

	// No bundle on the release: no packages block, the stale one gone, the
	// version still stamped.
	releaseAssets = func(string, string) ([]string, error) { return []string{"dibs_darwin_arm64.tar.gz"}, nil }
	write()
	if err := run(path, ""); err != nil {
		t.Fatal(err)
	}
	doc = read()
	if _, has := doc["packages"]; has {
		t.Errorf("a release without a bundle was published with a packages block: %v", doc["packages"])
	}
	if doc["version"] != "0.0.8" {
		t.Errorf("version = %v, want 0.0.8 stamped regardless", doc["version"])
	}

	// A release that could not be examined is neither: the stamp fails,
	// rather than publishing an install path's absence on a network error.
	releaseAssets = func(string, string) ([]string, error) { return nil, errors.New("gh: connection reset") }
	write()
	if err := run(path, ""); err == nil {
		t.Error("a release that could not be listed was published without its packages block")
	}
}
