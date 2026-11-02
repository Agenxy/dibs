// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestCoordinatorRecoveryFenceSeparatesSurvivorTrafficFromMovedInputs(t *testing.T) {
	cases := []struct {
		name   string
		stale  bool
		change func(*supportedMergeFixture)
	}{
		{"survivor-mail-wake-read-ack", false, func(f *supportedMergeFixture) {
			if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.into["agent_id"].(string)}}); err != nil {
				f.t.Fatal(err)
			}
			mail := f.call("send", map[string]any{"token": f.lead["token"], "to": f.into["agent_id"], "type": "notify", "body": "ordinary new survivor mail"})["msg_serial"]
			assertRecoverySurvivorStatus(f, core.StatusDormant)
			f.call("check_in", map[string]any{"token": f.into["token"]})
			assertRecoverySurvivorStatus(f, core.StatusActive)
			f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": mail})
			f.call("ack", map[string]any{"token": f.into["token"], "msg_serial": mail})
		}},
		{"survivor-outgoing-to-peer", false, func(f *supportedMergeFixture) {
			f.call("send", map[string]any{"token": f.into["token"], "to": f.lead["agent_id"], "type": "notify", "body": "unrelated outgoing mail"})
		}},
		{"source-incoming-by-id", true, func(f *supportedMergeFixture) {
			f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "notify", "body": "source custody changed"})
		}},
		{"source-incoming-by-name", true, func(f *supportedMergeFixture) {
			f.call("send", map[string]any{"token": f.lead["token"], "to": "source-label", "type": "notify", "body": "source custody changed"})
		}},
		{"survivor-outgoing-to-source", true, func(f *supportedMergeFixture) {
			f.call("send", map[string]any{"token": f.into["token"], "to": f.source["agent_id"], "type": "notify", "body": "still source incoming"})
		}},
		{"survivor-slot", true, func(f *supportedMergeFixture) {
			f.call("declare", map[string]any{"token": f.into["token"], "text": "new survivor work"})
		}},
		{"survivor-claim", true, func(f *supportedMergeFixture) {
			r := f.raw("claim", map[string]any{"token": f.into["token"], "path": "/recovery-fence-fixture", "mode": "shared"})
			if r["granted"] != true {
				f.t.Fatalf("setup: claim not granted: %v", r)
			}
		}},
		{"survivor-binding", true, func(f *supportedMergeFixture) {
			f.call("bind_session", map[string]any{"token": f.into["token"], "session_id": "changed-survivor-binding"})
		}},
	}
	for _, era := range []string{"2026-07-28", "2025-11-25"} {
		for _, tc := range cases {
			t.Run(era+"/"+tc.name, func(t *testing.T) {
				f := recoveryFixture(t, era)
				// A distinct label makes the name and stable-ID routes different
				// inputs; two spellings of the same ID would not test resolution.
				f.call("update", map[string]any{"token": f.source["token"], "name": "source-label"})
				if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
					t.Fatal(err)
				}
				f.call("check_in", map[string]any{"token": f.into["token"]})
				proposal := coordinatorRecoveryRequest(f)
				tc.change(f)
				f.call("check_in", map[string]any{"token": f.into["token"]})
				f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": proposal})
				before, err := f.e.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				r := f.raw("respond", map[string]any{"token": f.into["token"], "msg_serial": proposal, "disposition": "approve"})
				if tc.stale {
					if r["code"] != "E_RECOVERY_STALE" {
						t.Fatalf("changed input must stale: %v", r)
					}
					after, err := f.e.Board(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if before["serial"] != after["serial"] {
						t.Fatal("refused consent changed replayable state")
					}
					if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); r["code"] == nil {
						t.Fatal("refusal exposed source mail")
					}
					return
				}
				if r["recovered"] == nil {
					t.Fatalf("unmoved survivor traffic invalidated consent: %v", r)
				}
				f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
				f.stop()
				f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
				f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
			})
		}
	}
}

func assertRecoverySurvivorStatus(f *supportedMergeFixture, want core.AgentStatus) {
	f.t.Helper()
	b, err := f.e.Board(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	for _, row := range b["agents"].([]map[string]any) {
		if row["id"] == f.into["agent_id"] {
			if row["status"] != want {
				f.t.Fatalf("survivor status=%v, want %v", row["status"], want)
			}
			return
		}
	}
	f.t.Fatal("setup: survivor missing from board")
}
