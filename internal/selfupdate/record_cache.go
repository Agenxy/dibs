// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import (
	"context"
	"crypto/sha256"
	"sync"
)

// ReleaseEvidenceCache is derived, process-local and rebuildable. Every read
// snapshots and hashes the bounded record again; only exact unchanged bytes
// and the same build version reuse a successful verification. No mtime assumption,
// online refresh, ledger state or persisted boolean supplies authority.
type ReleaseEvidenceCache struct {
	mu           sync.Mutex
	set          bool
	hash         [sha256.Size]byte
	buildVersion string
	verified     VerifiedRelease
}

// Load verifies a new bounded record snapshot, or reuses an unchanged success.
// It must be called outside the engine's writer and before secret disclosure.
func (c *ReleaseEvidenceCache) Load(ctx context.Context, dir, buildVersion string) (VerifiedRelease, error) {
	v, raw, err := readReleaseRecord(dir)
	// Deliberately coalesce concurrent mints behind one bounded verification;
	// this serialization is off the engine writer and never holds its lock.
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.set, c.verified = false, VerifiedRelease{}
		return VerifiedRelease{}, err
	}
	hash := sha256.Sum256(raw)
	if c.set && c.hash == hash && c.buildVersion == buildVersion {
		return c.verified, nil
	}
	c.set, c.verified = false, VerifiedRelease{}
	if err = v.verifyOffline(ctx, buildVersion); err != nil {
		// A process exit cannot distinguish cryptographic refusal from local
		// I/O/tool failure or a crash. Never cache a verdict inferred from it.
		return VerifiedRelease{}, err
	}
	c.set, c.hash, c.buildVersion, c.verified = true, hash, buildVersion, v
	return v, nil
}
