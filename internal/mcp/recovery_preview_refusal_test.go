// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

func TestCoordinatorRecoveryExactIDIgnoresRetiredNamesake(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			retired := f.call("register", map[string]any{
				"name": f.source["agent_id"], "nonce": "retired-namesake",
				"session_id": "retired-session", "harness": "Codex", "surface": "cli",
			})
			if retired["agent_id"] == f.source["agent_id"] || retired["agent_id"] == f.into["agent_id"] {
				t.Fatal("setup: namesake did not get a separate identity")
			}
			private := f.call("send", map[string]any{"token": f.lead["token"], "to": retired["agent_id"], "type": "question", "body": "namesake-only mail"})["msg_serial"]
			f.call("sign_off", map[string]any{"token": retired["token"]})
			retiredRefusal := f.raw("send", map[string]any{"token": f.lead["token"], "to": retired["agent_id"], "type": "notify", "body": "before recovery"})
			if retiredRefusal["code"] != "E_NO_AGENT" {
				t.Fatalf("setup: namesake did not close: %v", retiredRefusal)
			}
			f.call("check_in", map[string]any{"token": f.into["token"]})
			before, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			plan := f.call("merge_agents", f.args())
			if plan["mode"] != "recovery_request" || plan["recovery_refusal"] != nil {
				t.Fatalf("exact source ID lost precedence to a retired namesake: %v", plan)
			}
			after, err := f.e.Board(context.Background())
			if err != nil || before["serial"] != after["serial"] {
				t.Fatalf("preview changed replayable state: %v", err)
			}
			request := f.call("merge_agents", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"], "fence": plan["fence"]})["msg_serial"]
			if request == nil {
				t.Fatal("proposal did not reach ordinary send")
			}
			f.call("respond", map[string]any{"token": f.into["token"], "msg_serial": request, "disposition": "approve"})
			f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
			for _, phase := range []string{"live", "replayed", "snapshot"} {
				if phase != "live" {
					restartRecoveredNamesakeFixture(f, phase == "snapshot")
				}
				// As in production, the source's stable ID and current name are
				// the SAME string. Both closed rows retain that name after approval.
				for _, address := range []any{f.source["agent_id"], "worker"} {
					future := f.call("send", map[string]any{"token": f.lead["token"], "to": address, "type": "question", "body": "future source mail " + phase})["msg_serial"]
					f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": future})
				}
				if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": private}); r["code"] == nil {
					t.Fatal("recovering the source also exposed the retired namesake's mailbox")
				}
				if r := f.raw("send", map[string]any{"token": f.lead["token"], "to": retired["agent_id"], "type": "notify", "body": "must not reroute namesake ID"}); r["code"] != retiredRefusal["code"] || r["message"] != retiredRefusal["message"] {
					t.Fatalf("retired namesake's exact ID changed meaning in %s: %v", phase, r)
				}
			}
		})
	}
}

func restartRecoveredNamesakeFixture(f *supportedMergeFixture, snapshot bool) {
	f.t.Helper()
	f.stop()
	if snapshot {
		box, err := ledger.LoadOrCreateKey(filepath.Join(f.dir, "key"))
		if err != nil {
			f.t.Fatal(err)
		}
		path := filepath.Join(f.dir, "ledger.jsonl")
		led, err := ledger.Open(path, "merge-fixture", box)
		if err != nil {
			f.t.Fatal(err)
		}
		// Compact ONLY this stopped private fixture, using the production
		// Replay path. No timestamp, field or snapshot is forged by the test.
		led.KeepHistory(time.Nanosecond)
		_, replayErr := led.Replay(core.NewState("merge-fixture", core.DefaultLimits()))
		closeErr := led.Close()
		if replayErr != nil || closeErr != nil {
			f.t.Fatalf("compact fixture: replay=%v close=%v", replayErr, closeErr)
		}
		if header, ok, err := ledger.SnapshotAnchor(path); err != nil || !ok || header.Serial == 0 {
			f.t.Fatalf("setup: no real snapshot anchor: %v %v %v", header, ok, err)
		}
	}
	f.srv, f.e, f.stop = supportedMergeServer(f.t, f.dir)
}

func TestCoordinatorMergePreviewExplainsRecoveryRefusal(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := recoveryFixture(t, version)
			f.call("bind_session", map[string]any{"token": f.source["token"], "session_id": "source-still-bound"})
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
				t.Fatal(err)
			}
			f.call("check_in", map[string]any{"token": f.into["token"]})
			plan := f.call("merge_agents", f.args())
			forced := f.raw("merge_agents", map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"], "dry_run": true, "fence": "recovery:probe"})
			if forced["code"] != "E_RECOVERY_NEEDS_HUMAN" {
				t.Fatalf("setup: source binding did not refuse recovery: %v", forced)
			}
			refusal, ok := plan["recovery_refusal"].(map[string]any)
			if !ok {
				t.Fatalf("coordinator merge preview swallowed the recovery refusal: %v", plan)
			}
			for _, key := range []string{"code", "message", "hint"} {
				if fmt.Sprint(refusal[key]) == "" || refusal[key] != forced[key] {
					t.Errorf("preview %s=%v, actual refusal=%v", key, refusal[key], forced[key])
				}
			}
			if plan["coordinator_eligible"] != false || plan["human_request"] == nil || plan["mode"] == "recovery_request" {
				t.Fatalf("refusal changed the ordinary merge plan: %v", plan)
			}
		})
	}
}
