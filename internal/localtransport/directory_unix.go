// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const tighteningFile = "ipc-directory-tightening.json"

// InspectDirectory checks before any credential is read and never changes modes.
func InspectDirectory(dir string) (DirectoryStatus, error) {
	f, info, err := openProfileDirectory(dir, false)
	if err != nil {
		return DirectoryStatus{}, err
	}
	defer func() { _ = f.Close() }()
	return inspectDirectory(dir, f, info)
}

func inspectDirectory(dir string, f *os.File, info os.FileInfo) (DirectoryStatus, error) {
	st := info.Sys().(*syscall.Stat_t)
	status := DirectoryStatus{
		Mode: fmt.Sprintf("%04o", info.Mode().Perm()),
		UID:  uint64(st.Uid), GID: uint64(st.Gid), WillTighten: info.Mode().Perm() != 0o700,
	}
	if info.Mode().Perm()&0o022 != 0 {
		return status, directoryModeError(dir)
	}
	if err := checkDirectoryACL(f, true); err != nil {
		return status, err
	}
	if err := checkAncestors(filepath.Clean(dir), info); err != nil {
		return status, err
	}
	if err := checkStoredCredentials(dir); err != nil {
		return status, err
	}
	if err := checkAuthorityFiles(dir); err != nil {
		return status, err
	}
	return status, nil
}

// PrepareDirectory is daemon-startup only. Clients remain strict: they cannot
// make a questionable profile acceptable by changing its permissions themselves.
func PrepareDirectory(dir string, now time.Time) (*DirectoryTightening, error) {
	f, info, err := openProfileDirectory(dir, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	status, err := inspectDirectory(dir, f, info)
	if err != nil || !status.WillTighten {
		return nil, err
	}
	if err := f.Chmod(0o700); err != nil {
		return nil, fmt.Errorf("tighten private data directory: %w; %s", err, directoryRepair(dir))
	}
	notice := &DirectoryTightening{FromMode: status.Mode, At: now.UTC()}
	if err := saveTightening(dir, notice); err != nil {
		// A derived notice cannot take a now-safe board down. Its failure is
		// carried by the same mandatory warning as the permission change.
		notice.NoticeError = err.Error()
	}
	return notice, nil
}

func saveTightening(dir string, notice *DirectoryTightening) error {
	b, err := json.Marshal(notice)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ipc-directory-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, writeErr := f.Write(b)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, tighteningFile))
}

// ConsumeTightening reports a startup change at the next clean filesystem check.
// Losing or removing this derived notice cannot lose coordination state.
func ConsumeTightening(dir string) (*DirectoryTightening, error) {
	if err := CheckDir(dir, false); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, tighteningFile)
	if err := PrivateFile(path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	f, err := os.Open(path) // #nosec G304 -- fixed derived notice within the verified private profile
	if err != nil {
		return nil, err
	}
	var notice DirectoryTightening
	err = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&notice)
	_ = f.Close()
	if err != nil || notice.At.IsZero() {
		return nil, errors.New("invalid derived directory-tightening notice; inspect " + path)
	}
	return &notice, os.Remove(path)
}

func checkAuthorityFiles(dir string) error {
	for _, name := range []string{"dibs.toml", "trusted-certs.pem", "tls-ca.pem"} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !trustedPathOwner(info) || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("profile authority file %s is not protected from another OS user; "+
				"inspect its provenance, then %s", path, credentialRepair(path))
		}
		if err := checkAuthorityACL(path, info); err != nil {
			return err
		}
	}
	return nil
}

func checkStoredCredentials(dir string) error {
	for _, name := range []string{"key", "local.secret", "tls-key.pem", "tls-ca-key.pem", "guest-ca-key.pem"} {
		if err := PrivateFile(filepath.Join(dir, name), true); err != nil {
			return err
		}
	}
	cache := filepath.Join(dir, "acme-cache")
	f, info, err := openProfileDirectory(cache, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if info.Mode().Perm()&0o022 != 0 {
		return directoryModeError(cache)
	}
	if err := checkDirectoryACL(f, true); err != nil {
		return err
	}
	for {
		names, err := f.Readdirnames(64)
		for _, name := range names {
			if err := PrivateFile(filepath.Join(cache, name), true); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
