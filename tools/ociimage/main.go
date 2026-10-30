// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// ociimage writes a container image of dibd as an OCI image layout, so a
// board can run inside a K7 kassette rather than beside one.
//
// K7 accepts an image only as a digest it can verify
// (`k7 admin image ensure 'k7.mesh.invalid/<name>@sha256:<digest>'
// --oci-layout <dir>`), never a registry pull. That makes the digest the
// image's identity, so the build has to be REPRODUCIBLE: the same dibd must
// give the same digest on any machine, or nobody can check that the image a
// deck pins is the one this repository built. Every timestamp, owner and
// mode in the layer is fixed, and gzip carries no name or time.
//
// One layer and no base image: dibd is a static binary and needs nothing
// beside it. /data is the volume (the data directory), /tmp exists because
// the standard library uses it. dibd listens on a non-loopback guest address
// and therefore serves TLS with its own board CA, which is the point: a board
// is secured by Dibs and stays that way inside whatever K7 adds around it.
//
// Prints the manifest digest, which is the reference to give K7.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Port is the board's port inside the guest. The kassette declares it as the
// `dibs` service; the host side is K7's to choose.
const Port = "4777"

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int               `json:"size"`
	Platform    map[string]string `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// layer is the root filesystem as an uncompressed tar, every header fixed.
func layer(binary []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	epoch := time.Unix(0, 0)
	for _, d := range []struct {
		name string
		mode int64
	}{{"data/", 0o700}, {"tmp/", 0o1777}, {"usr/", 0o755}, {"usr/local/", 0o755}, {"usr/local/bin/", 0o755}} {
		h := &tar.Header{Typeflag: tar.TypeDir, Name: d.name, Mode: d.mode, ModTime: epoch, Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
	}
	h := &tar.Header{
		Typeflag: tar.TypeReg, Name: "usr/local/bin/dibd", Mode: 0o755,
		Size: int64(len(binary)), ModTime: epoch, Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(h); err != nil {
		return nil, err
	}
	if _, err := tw.Write(binary); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func compress(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	zw.ModTime = time.Time{} // no time and no name: the header is the same everywhere
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// build writes the layout into out and returns the manifest digest.
func build(binary []byte, out, revision string) (string, error) {
	raw, err := layer(binary)
	if err != nil {
		return "", err
	}
	gz, err := compress(raw)
	if err != nil {
		return "", err
	}
	config, err := json.Marshal(map[string]any{
		"architecture": "arm64",
		"os":           "linux",
		"config": map[string]any{
			"Entrypoint":   []string{"/usr/local/bin/dibd", "-dir", "/data", "-addr", "0.0.0.0:" + Port},
			"Env":          []string{"HOME=/data", "TMPDIR=/tmp", "PATH=/usr/local/bin"},
			"ExposedPorts": map[string]any{Port + "/tcp": map[string]any{}},
			"Volumes":      map[string]any{"/data": map[string]any{}},
			"Labels": map[string]string{
				"org.opencontainers.image.revision": revision,
				"org.opencontainers.image.source":   "https://github.com/agenxy/dibs",
			},
		},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{digest(raw)}},
	})
	if err != nil {
		return "", err
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json", Digest: digest(config), Size: len(config),
		},
		"layers": []descriptor{{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: digest(gz), Size: len(gz),
		}},
	})
	if err != nil {
		return "", err
	}
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []descriptor{{
			MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: digest(manifest), Size: len(manifest),
			Platform:    map[string]string{"architecture": "arm64", "os": "linux"},
			Annotations: map[string]string{"org.opencontainers.image.ref.name": "dibd"},
		}},
	})
	if err != nil {
		return "", err
	}
	blobs := filepath.Join(out, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o750); err != nil {
		return "", err
	}
	for _, b := range [][]byte{gz, config, manifest} {
		// The name is 64 hex characters computed here, so no separator can reach the path.
		name := digest(b)[len("sha256:"):]
		if err := os.WriteFile(filepath.Join(blobs, name), b, 0o600); err != nil { // #nosec G703 -- a hex digest, above
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(out, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(out, "index.json"), index, 0o600); err != nil {
		return "", err
	}
	return digest(manifest), nil
}

func main() {
	dibd := flag.String("dibd", "", "a static linux/arm64 dibd")
	out := flag.String("out", "", "directory to write the OCI image layout into (created; must not exist)")
	revision := flag.String("revision", "", "the commit the binary was built from")
	flag.Parse()
	if *dibd == "" || *out == "" || *revision == "" {
		fmt.Fprintln(os.Stderr, "usage: ociimage -dibd <linux/arm64 dibd> -out <dir> -revision <commit>")
		os.Exit(2)
	}
	if _, err := os.Stat(*out); err == nil {
		fmt.Fprintf(os.Stderr, "ociimage: %s already exists; a layout is written whole or not at all\n", *out)
		os.Exit(1)
	}
	binary, err := os.ReadFile(*dibd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ociimage:", err)
		os.Exit(1)
	}
	d, err := build(binary, *out, *revision)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ociimage:", err)
		os.Exit(1)
	}
	fmt.Println(d)
}
