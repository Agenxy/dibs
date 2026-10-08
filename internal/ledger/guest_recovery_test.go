// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestGuestRecoveryIsExactDomainSeparatedBoardKeyDerivation(t *testing.T) {
	key := bytes.Repeat([]byte{0x27}, 32)
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, key, 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := LoadOrCreateKey(file)
	if err != nil {
		t.Fatal(err)
	}
	first, err := box.GuestRecoveryNonce("guest-worker")
	expected, kerr := hkdf.Key(sha256.New, key, nil, "dibs guest recovery v1\x00guest-worker", 32)
	if err != nil || kerr != nil || first != hex.EncodeToString(expected) {
		t.Fatal("recovery derivation changed its algorithm, salt or domain")
	}
	reloaded, err := LoadOrCreateKey(file)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reloaded.GuestRecoveryNonce("guest-worker")
	other, oerr := reloaded.GuestRecoveryNonce("guest-other")
	if err != nil || oerr != nil || again != first || other == first {
		t.Fatal("reloading changed recovery or different names shared a credential")
	}
	sealed, err := box.SealBytes([]byte("retained board state"))
	if err != nil {
		t.Fatal(err)
	}
	// Controlled replacement proves why arbitrary key replacement is not
	// supported rotation. No production key or board is touched by this test.
	if err := os.WriteFile(file, bytes.Repeat([]byte{0x28}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced, err := LoadOrCreateKey(file)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := replaced.GuestRecoveryNonce("guest-worker")
	if err != nil || changed == first {
		t.Fatal("key replacement unexpectedly preserved recovery")
	}
	if _, err := replaced.OpenBytes(sealed); err == nil {
		t.Fatal("replacement key unexpectedly decrypted retained state")
	}
	if _, err := (*Box)(nil).GuestRecoveryNonce("guest-worker"); err == nil {
		t.Fatal("missing board key invented a recovery credential")
	}
	if _, err := (&Box{}).GuestRecoveryNonce("guest-worker"); err == nil {
		t.Fatal("uninitialized Box invented a recovery credential")
	}
}
