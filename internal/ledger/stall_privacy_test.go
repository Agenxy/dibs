// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/procexec"
)

func TestStallNoticeBodyIsEncrypted(t *testing.T) {
	box, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	// Literal kind lets this guard run unchanged against the pre-feature code.
	op := &core.Op{Kind: "stall_notified", Body: "private declaration quoted in a stall"}
	plain := op.Body
	if err := box.EncryptOp(op); err != nil {
		t.Fatal(err)
	}
	if op.Body == plain {
		t.Fatal("stall notice left private declaration content in plaintext")
	}
	if err := box.DecryptOp(op); err != nil || op.Body != plain {
		t.Fatalf("decrypted notice: %q, %v", op.Body, err)
	}
}

func TestFinishedWakeNoticeSurvivesEncryptedReplay(t *testing.T) {
	l, path := newLedger(t)
	state := core.NewState("test", core.DefaultLimits())
	for _, id := range []string{"sender", "worker", "dibs"} {
		apply(t, state, l, &core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token"}, t0)
	}
	source := apply(t, state, l, &core.Op{
		Kind: core.OpSendMessage, AgentID: "sender", To: "worker",
		MsgType: core.MsgHandoff, Body: "private source",
	}, t0)["msg_serial"].(uint64)
	const body = "private finished execution notice"
	receipt := apply(t, state, l, &core.Op{
		Kind: core.OpFinishedWakeNotified, AgentID: "dibs", To: "sender",
		MsgType: core.MsgNotify, Body: body, MsgSerial: source,
	}, t0)["msg_serial"].(uint64)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || bytes.Contains(raw, []byte(body)) {
		t.Fatalf("finished notice was not sealed: %v", err)
	}
	replayed := reopen(t, path)
	if replayed.Messages[source].FinishedWakeNotice != receipt || replayed.Messages[source].Consumed ||
		replayed.Messages[receipt].Body != body {
		t.Fatal("encrypted replay lost the source or its sender notice")
	}
	if old := os.Getenv("DIBS_TEST_FINISHED_WAKE_OLD_DAEMON"); old != "" {
		dir := filepath.Dir(path)
		for name, content := range map[string]string{"node_id": "test", "local.secret": strings.Repeat("a", 64)} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := procexec.Command(ctx, old, "-dir", dir, "-check", "-addr", "127.0.0.1:4777")
		cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_NOTIFY=off")
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unknown op kind \"finished_wake_notified\"") {
			t.Fatalf("old daemon did not refuse the new record for its intended reason: %v %s", err, out)
		}
		t.Log("old daemon refused the new ledger record explicitly")
	}
}
