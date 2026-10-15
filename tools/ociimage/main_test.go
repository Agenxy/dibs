// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The digest is the image's identity to K7, so two builds of one binary must
// agree byte for byte: otherwise nobody can check that the image a deck pins
// is the one this repository built.
func TestTheSameBinaryGivesTheSameDigest(t *testing.T) {
	bin := []byte("\x7fELF stand-in for a static dibd")
	a, err := build(bin, filepath.Join(t.TempDir(), "a"), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	b, err := build(bin, filepath.Join(t.TempDir(), "b"), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("two builds of one binary disagree: %s vs %s", a, b)
	}
	c, err := build(append(bin, '!'), filepath.Join(t.TempDir(), "c"), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("a different binary produced the same digest")
	}
}

// Every reference in the layout resolves to a blob with that digest, the
// config names the uncompressed layer, and the layer holds dibd where the
// entrypoint looks for it. A runtime checks all of this and refuses the image
// otherwise, which is a worse place to find out.
func TestTheLayoutIsSelfConsistent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "img")
	bin := []byte("\x7fELF stand-in for a static dibd")
	md, err := build(bin, out, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	blob := func(d string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(out, "blobs", "sha256", d[len("sha256:"):]))
		if err != nil {
			t.Fatal(err)
		}
		if digest(b) != d {
			t.Fatalf("blob %s holds content with digest %s", d, digest(b))
		}
		return b
	}
	var index struct{ Manifests []descriptor }
	raw, _ := os.ReadFile(filepath.Join(out, "index.json"))
	if err := json.Unmarshal(raw, &index); err != nil || len(index.Manifests) != 1 || index.Manifests[0].Digest != md {
		t.Fatalf("index does not name the printed manifest: %s", raw)
	}
	var manifest struct {
		Config descriptor
		Layers []descriptor
	}
	if err := json.Unmarshal(blob(md), &manifest); err != nil || len(manifest.Layers) != 1 {
		t.Fatal("manifest", err)
	}
	var config struct {
		Config struct{ Entrypoint []string }
		Rootfs struct {
			DiffIDs []string `json:"diff_ids"`
		}
	}
	if err := json.Unmarshal(blob(manifest.Config.Digest), &config); err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(blob(manifest.Layers[0].Digest)))
	if err != nil {
		t.Fatal(err)
	}
	layerTar, _ := io.ReadAll(zr)
	if len(config.Rootfs.DiffIDs) != 1 || config.Rootfs.DiffIDs[0] != digest(layerTar) {
		t.Fatal("the config's diff_id is not the uncompressed layer")
	}
	tr := tar.NewReader(bytes.NewReader(layerTar))
	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("no %s in the layer", config.Config.Entrypoint[0])
		}
		if "/"+h.Name == config.Config.Entrypoint[0] {
			got, _ := io.ReadAll(tr)
			if !bytes.Equal(got, bin) || h.Mode&0o111 == 0 {
				t.Fatal("the entrypoint is not the binary given, or not executable")
			}
			return
		}
	}
}
