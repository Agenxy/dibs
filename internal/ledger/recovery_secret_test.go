// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestRecoveryCandidatesNeverEncodeInRegisterOp(t *testing.T) {
	candidates := []string{"unselected-candidate-secret-a", "unselected-candidate-secret-b"}
	op := &core.Op{
		Kind: core.OpRegister, Name: "worker", Nonce: "selected-register-secret",
		NewToken: "register-token", RecoveryNonces: candidates,
	}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	assertRecoverySecretsAbsent(t, raw, candidates)
	led, path := newLedger(t)
	if err := led.Append(1, t0, op); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertRecoverySecretsAbsent(t, onDisk, append(candidates, op.Nonce, op.NewToken))
}

func assertRecoverySecretsAbsent(t *testing.T, encoded []byte, secrets []string) {
	t.Helper()
	if bytes.Contains(encoded, []byte("recovery_nonces")) || bytes.Contains(encoded, []byte("RecoveryNonces")) {
		t.Fatal("candidate group reached the encoded op")
	}
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("credential appeared in encoded bytes")
		}
	}
}
