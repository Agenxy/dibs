package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagedMembersMatchTheChecksumSigningInput(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	if err := os.Mkdir(dist, 0o700); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, target := range []string{"darwin_arm64", "linux_amd64", "linux_arm64"} {
		body := []byte("final packaged bytes including the signature: " + target)
		archive := filepath.Join(dist, "dibs_0.0.0-SNAPSHOT_"+target+".tar.gz")
		writeArchive(t, archive, map[string]file{"dibs": {body, 0o755}})
		fmt.Fprintf(&sums, "%x  members/%s/dibs\n", sha256.Sum256(body), target)
	}
	path := filepath.Join(dist, "checksums.txt")
	write := func(b string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(sums.String())
	if err := checkMembers(root); err != nil {
		t.Fatal("setup: sound packaged/checksum input refused:", err)
	}
	for name, text := range map[string]string{
		"missing":   "",
		"duplicate": sums.String() + sums.String(),
		"altered":   strings.Repeat("0", 64) + "  members/darwin_arm64/dibs\n" + sums.String()[strings.Index(sums.String(), "\n")+1:],
		"malformed": strings.Repeat("g", 64) + "  members/darwin_arm64/dibs\n",
	} {
		t.Run(name, func(t *testing.T) {
			write(text)
			if err := checkMembers(root); err == nil {
				t.Fatal("invalid checksum signing input admitted")
			}
		})
	}
	write(sums.String())
	archive := filepath.Join(dist, "dibs_0.0.0-SNAPSHOT_linux_arm64.tar.gz")
	writeArchive(t, archive, map[string]file{"dibs": {[]byte("changed after checksum calculation"), 0o755}})
	if err := checkMembers(root); err == nil {
		t.Fatal("changed packaged executable admitted")
	}
}

func TestMemberDigestRefusesAnUnusableArchiveMember(t *testing.T) {
	for name, files := range map[string]map[string]file{
		"missing": {"dibd": {[]byte("daemon"), 0o755}},
		"inert":   {"dibs": {[]byte("image"), 0o644}},
		"empty":   {"dibs": {nil, 0o755}},
	} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "dibs_fixture_linux_arm64.tar.gz")
			writeArchive(t, archive, files)
			if _, err := packagedMemberDigest(archive); err == nil {
				t.Fatal("unusable member admitted")
			}
		})
	}
}
