// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/procexec"
)

func TestV3ConsentReaderFencePrecedesRecordAndSurvivesRollback(t *testing.T) {
	for _, direction := range []string{"survivor", "coordinator"} {
		t.Run(direction, func(t *testing.T) {
			f := recoveryFixture(t, "2026-07-28")
			for name, body := range map[string]string{"node_id": "merge-fixture", "local.secret": strings.Repeat("a", 64)} {
				if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f.stop()
			assertV3RollbackReader(t, f.dir, false)
			f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
			request := recoveryRequest
			approver := f.lead
			if direction == "coordinator" {
				request, approver = coordinatorRecoveryRequest, f.into
			}
			serial := request(f)
			f.stop()
			assertV3MarkerBeforeData(t, f.dir)
			assertV3RollbackReader(t, f.dir, true)
			f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
			approved := f.call("respond", map[string]any{"token": approver["token"], "msg_serial": serial, "disposition": "approve"})
			if approved["recovered"] == nil {
				t.Fatal("current reader lost pending consent after refused rollback")
			}
			f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
			f.stop()
			assertV3RollbackReader(t, f.dir, true)
		})
	}
}

func assertV3MarkerBeforeData(t *testing.T, dir string) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	marker, data := uint64(0), uint64(0)
	for scan.Scan() {
		var line ledger.Line
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if err := box.DecryptOp(line.Op); err != nil {
			t.Fatal(err)
		}
		if line.Op.Kind == core.OpRequireV17Features && slices.Contains(line.Op.RequiredFeatures, "mailbox-recovery-consent/v3") {
			marker = line.S
		}
		if line.Op.Recovery != nil && strings.HasPrefix(line.Op.Recovery.Fence, "v3:") {
			data = line.S
			break
		}
	}
	if err := scan.Err(); err != nil || marker == 0 || data <= marker {
		t.Fatalf("v3 data preceded its reader fence: marker=%d data=%d err=%v", marker, data, err)
	}
}

func assertV3RollbackReader(t *testing.T, dir string, refuse bool) {
	t.Helper()
	old := os.Getenv("DIBS_TEST_RECOVERY_V3_OLD_DAEMON")
	if old == "" {
		return // external old-binary qualification is reported separately
	}
	path := filepath.Join(dir, "ledger.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := procexec.Command(ctx, old, "-dir", dir, "-check", "-addr", "127.0.0.1:4777")
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_NOTIFY=off")
	out, runErr := cmd.CombinedOutput()
	t.Logf("rollback reader refuse=%v err=%v output=%s", refuse, runErr, out)
	if refuse && (runErr == nil || !strings.Contains(string(out), "mailbox-recovery-consent/v3")) || !refuse && runErr != nil {
		t.Fatalf("rollback reader gave the wrong verdict: refuse=%v err=%v output=%s", refuse, runErr, out)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rollback preflight changed persistent ledger bytes", err)
	}
}

func TestRecoveryExpiryRemainsStaleWithCorrectiveRequestHint(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			question := f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "expires before consent", "deadline_s": 1})["msg_serial"]
			if r, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpMarkDelivered, MsgSerials: []uint64{uint64(question.(float64))}}); err != nil || r["changed"] != true {
				t.Fatalf("setup: response window did not start: %v %v", r, err)
			}
			request := recoveryRequest(f)
			<-time.After(1100 * time.Millisecond)
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep}); err != nil {
				t.Fatal(err)
			}
			r := f.raw("respond", map[string]any{"token": f.lead["token"], "msg_serial": request, "disposition": "approve"})
			hint, _ := r["hint"].(string)
			if r["code"] != "E_RECOVERY_STALE" || !strings.Contains(hint, "recover_agent(dry_run:true)") || !strings.Contains(hint, "request again") {
				t.Fatalf("expiry must refuse with a fresh-preview/request remedy: %v", r)
			}
			message := f.call("read_mail", map[string]any{"token": f.lead["token"], "msg_serial": question})["message"].(map[string]any)
			if message["state"] != core.MsgStateExpiredDormant {
				t.Fatalf("setup: question did not actually expire: %v", message["state"])
			}
			if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": question}); r["code"] == nil {
				t.Fatal("stale expiry approval moved mail")
			}
		})
	}
}
