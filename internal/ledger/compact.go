// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// KeepHistory sets how much ledger history the next Replay keeps. Lines
// older than that are folded into the snapshot and removed from the file,
// at boot, before anything is served: compacting a live ledger would move
// the byte offsets the mail-history index already handed out. Zero, the
// default, keeps everything; dibd sets [ledger] history (24h by default).
func (l *Ledger) KeepHistory(d time.Duration) { l.keep = d }

func (l *Ledger) compactionCutoff() (time.Time, bool) {
	if l.keep <= 0 || l.readOnly || l.path == "" {
		return time.Time{}, false
	}
	return l.now().Add(-l.keep), true
}

func (l *Ledger) now() time.Time {
	if l.clock != nil {
		return l.clock()
	}
	return time.Now()
}

// dropPrefix rewrites the ledger without its first cut bytes, which the
// snapshot beside it already holds: temp file, fsync, rename, directory
// fsync, then the handle moves to the new file. A crash before the rename
// leaves the full ledger beside the newer snapshot, which Replay reads by
// checking the old prefix's chain against the snapshot's anchor.
func (l *Ledger) dropPrefix(cut int64) error {
	end, err := l.f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	dir := filepath.Dir(l.path)
	tmp, err := os.CreateTemp(dir, ".ledger-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = io.Copy(tmp, io.NewSectionReader(l.f, cut, end-cut)); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("compact ledger: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("reopen compacted ledger: %w", err)
	}
	old := l.f
	l.f = f
	return old.Close()
}

// Anchor is the snapshot the ledger continues from after Replay, if any.
func (l *Ledger) Anchor() (SnapshotHeader, bool) { return l.anchor, l.anchored }

// SnapshotAnchor is the snapshot beside a ledger file, read from its
// plaintext header: no key needed. ok is false when there is none.
func SnapshotAnchor(ledgerPath string) (SnapshotHeader, bool, error) {
	return readSnapshotHeader(snapshotPath(ledgerPath))
}
