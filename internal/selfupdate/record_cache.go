package selfupdate

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
)

// ReleaseEvidenceCache is derived, process-local and rebuildable. Every read
// snapshots and hashes the bounded record again; only exact unchanged bytes
// and the same build version reuse a verification result. No mtime assumption,
// online refresh, ledger state or persisted boolean supplies authority.
type ReleaseEvidenceCache struct {
	mu           sync.Mutex
	set          bool
	hash         [sha256.Size]byte
	buildVersion string
	verified     VerifiedRelease
	err          error
}

// Load verifies a new bounded record snapshot, or reuses an unchanged verdict.
// It must be called outside the engine's writer and before secret disclosure.
func (c *ReleaseEvidenceCache) Load(ctx context.Context, dir, buildVersion string) (VerifiedRelease, error) {
	v, raw, err := readReleaseRecord(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.set, c.verified, c.err = false, VerifiedRelease{}, nil
		return VerifiedRelease{}, err
	}
	hash := sha256.Sum256(raw)
	if c.set && c.hash == hash && c.buildVersion == buildVersion {
		return c.verified, c.err
	}
	c.set, c.verified, c.err = false, VerifiedRelease{}, nil
	if err = v.verifyOffline(ctx, buildVersion); err != nil {
		// Missing/broken tools and caller cancellation are not signature
		// verdicts. Caching those would strand a live daemon after tool repair.
		if ctx.Err() == nil && !errors.Is(err, errCosignUnavailable) {
			c.set, c.hash, c.buildVersion, c.err = true, hash, buildVersion, err
		}
		return VerifiedRelease{}, err
	}
	c.set, c.hash, c.buildVersion, c.verified = true, hash, buildVersion, v
	return v, nil
}
