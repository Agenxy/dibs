// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// VerifyNativePeer checks kernel-supplied audit evidence against the bundled
// notifier. Neither local.secret, a PID, nor a participant-selected file grants
// this authority. The native Security framework resolves the dynamic guest.
func VerifyNativePeer(ctx context.Context, audit []byte) error {
	if runtime.GOOS != "darwin" || len(audit) != 32 || helper() == "" {
		return errors.New("native notifier attestation unavailable; open the installed Dibs app or use the board")
	}
	return verifyNativePeerUsing(ctx, helper(), audit)
}

func verifyNativePeerUsing(ctx context.Context, binary string, audit []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// #nosec G204 -- installed bundled helper; audit evidence uses stdin only.
	cmd := exec.CommandContext(ctx, binary, "--verify-notification-peer")
	cmd.Stdin = bytes.NewReader(audit)
	raw, err := cmd.Output()
	var result struct {
		Attested bool `json:"attested"`
	}
	if err != nil {
		return fmt.Errorf("native notifier code identity refused; "+
			"reopen with the installed Dibs app or use the board: %w", err)
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &result) != nil || !result.Attested {
		return errors.New("native notifier returned no attestation; reopen with the installed Dibs app or use the board")
	}
	return nil
}
