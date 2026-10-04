package engine

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// Run the identical read-only probe against baseline and new code on the same
// private copy. Print only its canonical digest, never credentials or bodies.
func TestCopiedLegacyLedgerStateFingerprint(t *testing.T) {
	dir := os.Getenv("DIBS_TEST_PREUPGRADE_LEDGER_DIR")
	if dir == "" {
		t.Skip("requires private real-ledger copy")
	}
	if !strings.HasPrefix(filepath.Base(dir), "dibs-inline-upgrade.") {
		t.Fatal("use a private explicitly named copy, never the live board")
	}
	if _, err := os.Stat(filepath.Join(dir, "key")); err != nil {
		t.Fatal("existing copy key required")
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.OpenReadOnly(filepath.Join(dir, "ledger.jsonl"), "legacy-replay-probe", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}()
	st := core.NewState("legacy-replay-probe", core.DefaultLimits())
	if _, err := journal.Replay(st); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	// The only new State field is an additive zero before upgrade. Normalize
	// just that field, refusing any nonzero value instead of hiding a change.
	if value, ok := fields["ReviewReadCutoff"]; ok && string(value) != "0" {
		t.Fatal("fixture has already upgraded")
	}
	delete(fields, "ReviewReadCutoff")
	canonical, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(canonical))
	if expected := os.Getenv("DIBS_TEST_LEGACY_STATE_SHA256"); expected != "" && digest != expected {
		t.Fatal("legacy replay state differs from the measured baseline")
	}
	t.Logf("legacy serial=%d canonical-state-sha256=%s (read-only)", st.Serial, digest)
}

// The identical probe can run against the prior build through a Go overlay.
// Normalize ONLY the new snapshot; all pre-existing replayable state must match.
func TestCopiedUpgradedLedgerSnapshotFingerprint(t *testing.T) {
	dir := os.Getenv("DIBS_TEST_PREUPGRADE_LEDGER_DIR")
	if dir == "" {
		t.Skip("requires private encrypted current-ledger copy")
	}
	if !strings.HasPrefix(filepath.Base(dir), "dibs-inline-upgrade.") {
		t.Fatal("use a private explicitly named copy, never the live board")
	}
	if _, err := os.Stat(filepath.Join(dir, "key")); err != nil {
		t.Fatal("existing copy key required")
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.OpenReadOnly(filepath.Join(dir, "ledger.jsonl"), "snapshot-replay-probe", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	}()
	st := core.NewState("snapshot-replay-probe", core.DefaultLimits())
	if _, err := journal.Replay(st); err != nil {
		t.Fatal(err)
	}
	if st.ReviewReadCutoff == 0 {
		t.Fatal("fixture must contain the unreleased upgrade op")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if field := fields["legacy_ack_at_cutoff"]; field != nil {
		if err := json.Unmarshal(field, &snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("DIBS_TEST_REQUIRE_CUTOFF_SNAPSHOT") == "true" && len(snapshot) == 0 {
		t.Fatal("replay did not reconstruct the additive cutoff snapshot")
	}
	delete(fields, "legacy_ack_at_cutoff")
	canonical, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(canonical))
	if expected := os.Getenv("DIBS_TEST_LEGACY_STATE_SHA256"); expected != "" && digest != expected {
		t.Fatal("snapshot changed pre-existing replayable state")
	}
	t.Logf("copied current serial=%d canonical-state-sha256=%s snapshot-identities=%d (read-only)", st.Serial, digest, len(snapshot))
}
