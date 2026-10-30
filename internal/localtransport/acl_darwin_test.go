// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryModeCannotConcealAnEveryoneWriteACL(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "dibs-acl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "board")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir, false); err != nil {
		t.Fatal("setup: ordinary private directory failed:", err)
	}
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow add_file,search", dir).CombinedOutput(); err != nil {
		t.Fatalf("setup: could not install the real filesystem ACL: %v %s", err, out)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("setup: ACL did not leave the checked mode at 0700: %v %v", info, err)
	}
	if err := CheckDir(dir, false); err == nil {
		t.Fatal("0700 mode concealed an ACL granting everyone directory write access")
	}
}

func TestInheritedWriteACLDoesNotBecomePrivateByModeAlone(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "dibs-acl-inherit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow add_file,search,directory_inherit", parent).CombinedOutput(); err != nil {
		t.Fatalf("setup: inherited ACL: %v %s", err, out)
	}
	dir := filepath.Join(parent, "board")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listing, err := exec.Command("/bin/ls", "-le", "-d", dir).CombinedOutput()
	if err != nil || !strings.Contains(string(listing), "inherited") {
		t.Fatalf("setup: the child did not inherit the actual grant: %v %s", err, listing)
	}
	if err := CheckDir(dir, false); err == nil {
		t.Fatal("inherited write grant passed private-profile admission")
	}
}

func TestDenyOnlyACLRemainsAValidPrivateProfile(t *testing.T) {
	dir := privateDir(t)
	if out, err := exec.Command("/bin/chmod", "+a", "everyone deny delete", dir).CombinedOutput(); err != nil {
		t.Fatalf("setup: benign deny ACL: %v %s", err, out)
	}
	if err := CheckDir(dir, false); err != nil {
		t.Fatalf("deny-only ACL was rejected: %v", err)
	}
	listing, err := exec.Command("/bin/ls", "-le", "-d", dir).CombinedOutput()
	if err != nil || !strings.Contains(string(listing), "everyone deny delete") {
		t.Fatalf("checking the profile changed its ACL: %v %s", err, listing)
	}
}

func TestCredentialReadACLIsNotPrivateDespiteMode0600(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "local.secret")
	if err := os.WriteFile(path, []byte("fixture-only-never-production"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow read", path).CombinedOutput(); err != nil {
		t.Fatalf("setup: credential read ACL: %v %s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("setup: read ACL changed the checked mode: %v %v", info, err)
	}
	if err := PrivateFile(path, false); err == nil || !strings.Contains(err.Error(), "ls -le") {
		t.Fatalf("credential ACL bypassed private-file admission or lost review guidance: %v", err)
	}
}

func TestProfileAuthorityWriteACLIsRefusedBeforeTrustIsRead(t *testing.T) {
	for _, name := range []string{"dibs.toml", "trusted-certs.pem", "tls-ca.pem"} {
		t.Run(name, func(t *testing.T) {
			dir := privateDir(t)
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte("fixture authority bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := CheckDir(dir, false); err != nil {
				t.Fatal("setup: clean authority file was rejected:", err)
			}
			if out, err := exec.Command("/bin/chmod", "+a", "everyone allow write", path).CombinedOutput(); err != nil {
				t.Fatalf("setup: authority write ACL: %v %s", err, out)
			}
			if err := CheckDir(dir, false); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("authority ACL could reach config/CA reads: %v", err)
			}
		})
	}
}
