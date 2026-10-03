package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/selfupdate"
)

// checkMembers enters after the real release tool, not at a hand-built digest.
// It checks the exact checksums.txt that the signing step receives against the
// final executable bytes inside EVERY published guest archive, after codesign.
func checkMembers(root string) error {
	// #nosec G304 -- root is repoRoot(), and both trailing path components are fixed.
	b, err := os.ReadFile(filepath.Join(root, "dist", selfupdate.ChecksumsName))
	if err != nil {
		return err
	}
	sums, err := selfupdate.GuestReleaseDigests(string(b))
	if err != nil {
		return err
	}
	archives, err := filepath.Glob(filepath.Join(root, "dist", "dibs_*.tar.gz"))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, archive := range archives {
		key, err := memberKeyForArchive(filepath.Base(archive))
		if err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("more than one archive supplies %s; use a clean snapshot", key)
		}
		seen[key] = true
		want, ok := sums[key]
		if !ok {
			return fmt.Errorf("%s names no digest for %s", selfupdate.ChecksumsName, key)
		}
		got, err := packagedMemberDigest(archive)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s: %s digest does not match the packaged executable", archive, key)
		}
	}
	if len(seen) != 3 {
		return fmt.Errorf("checked %d guest archives, expected all 3 published targets", len(seen))
	}
	fmt.Println("archivecheck: all 3 packaged guest CLI digests match the signing input")
	return nil
}

func memberKeyForArchive(name string) (string, error) {
	for _, target := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		if strings.HasSuffix(name, "_"+target[0]+"_"+target[1]+".tar.gz") {
			return selfupdate.GuestMemberName(target[0], target[1])
		}
	}
	return "", fmt.Errorf("%s is not a published guest target", name)
}

func packagedMemberDigest(archive string) (string, error) {
	// #nosec G304 -- an archive globbed under this checkout's dist/, not remote input.
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	digest := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if digest == "" {
				return "", fmt.Errorf("%s contains no dibs member", archive)
			}
			return digest, nil
		}
		if err != nil {
			return "", err
		}
		if h.Name != "dibs" {
			continue
		}
		if digest != "" || h.Typeflag != tar.TypeReg || h.Mode&0o111 == 0 || h.Size <= 0 || h.Size > 64<<20 {
			return "", fmt.Errorf("%s has a duplicate, nonregular, inert or oversized dibs member", archive)
		}
		hash := sha256.New()
		// The header was bounded above; do not let a compressed member turn an
		// unbounded copy into release-gate memory or CPU exhaustion.
		if _, err = io.CopyN(hash, tr, h.Size); err != nil {
			return "", err
		}
		digest = hex.EncodeToString(hash.Sum(nil))
	}
}
