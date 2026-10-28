// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
)

func TestSupportedMergeHumanApprovalPerformsExactlyOneTransition(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := newSupportedMerge(t, version)
			human, token, err := f.e.HumanAgent(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			f.call("check_in", map[string]any{"token": token})
			p := f.call("merge_agents", f.args())
			r := f.call("send", map[string]any{"token": f.lead["token"], "to": "human", "type": "request", "body": "sender prose", "merge": map[string]any{"from": f.source["agent_id"], "into": f.into["agent_id"], "fence": p["fence"]}})
			n, found, err := f.e.HumanNoticeFor(context.Background(), uint64(r["msg_serial"].(float64)))
			if err != nil || !found || !n.Privileged() || !mentions(map[string]any{"body": n.Body}, "Approve merges worker into worker-2") {
				t.Fatalf("approval did not state its actual typed effect: found=%v err=%v", found, err)
			}
			if !mentions(map[string]any{"body": n.Body}, "the fork's credential will open worker-2") {
				t.Fatal("human approval did not disclose credential transfer")
			}
			// Reading the request is ordinary delivery, not a reason to lose its fence.
			f.call("read_mail", map[string]any{"token": token, "msg_serial": r["msg_serial"]})
			before, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			// The one-call done convenience applies to work, never a typed effect.
			// Neither done nor progress may consume this pending human approval.
			for _, disposition := range []string{"done", "progress"} {
				refused := f.raw("respond", map[string]any{"token": token, "msg_serial": r["msg_serial"], "disposition": disposition, "body": "not an approval"})
				if refused["code"] != "E_BAD_DISPOSITION" {
					t.Fatalf("typed merge accepted %s without approval: %v", disposition, refused)
				}
			}
			unchanged, err := f.e.Board(context.Background())
			if err != nil || unchanged["serial"] != before["serial"] {
				t.Fatal("refused work verdict changed the merge approval")
			}
			f.call("respond", map[string]any{"token": token, "msg_serial": r["msg_serial"], "disposition": "approve"})
			after, err := f.e.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if after["serial"].(uint64) != before["serial"].(uint64)+1 {
				t.Fatal("approval and effect did not share one serial")
			}
			read := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
			if !mentions(read, "private original mail") {
				t.Fatal("human yes did not perform the merge")
			}
			assertMergedHistory(f)
			rows := after["merged_agents"].([]core.Result)
			actor := rows[0]["by"].(*core.MergeActor)
			if actor == nil || actor.ID != human || actor.Authority != "human" {
				t.Fatal("actual approver absent from audit")
			}
			if recovered := f.call("register", map[string]any{"name": "worker", "nonce": "source-secret"}); recovered["agent_id"] != f.into["agent_id"] {
				t.Fatal("explicit human approval did not transfer the fork credential")
			}
		})
	}
}

func TestSupportedMergeStaleApprovalLeavesRequestOpen(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	_, token, err := f.e.HumanAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": token})
	p := f.call("merge_agents", f.args())
	r := f.call("send", map[string]any{"token": f.lead["token"], "to": "human", "type": "request", "body": "merge", "merge": map[string]any{"from": f.source["agent_id"], "into": f.into["agent_id"], "fence": p["fence"]}})
	f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "notify", "body": "new private mail"})
	if got := f.raw("respond", map[string]any{"token": token, "msg_serial": r["msg_serial"], "disposition": "approve"}); got["code"] != "E_MERGE_STALE" {
		t.Fatalf("stale human decision accepted: %v", got)
	}
	m := f.call("read_mail", map[string]any{"token": token, "msg_serial": r["msg_serial"]})["message"].(map[string]any)
	if m["state"] == "approved" {
		t.Fatal("failed merge nevertheless recorded approval")
	}
}

func TestSupportedMergeFenceAndActorSurviveRealLedgerReplay(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	// Unread, unreachable mail legitimately gains a contact record on startup.
	// Deliver it before preview so this tests restart with UNCHANGED inputs.
	f.call("read_mail", map[string]any{"token": f.source["token"], "msg_serial": f.mail})
	f.call("check_in", map[string]any{"token": f.into["token"]})
	if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
		t.Fatal(err)
	}

	a := f.args()
	p := f.call("merge_agents", a)
	f.stop()
	f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
	f.e.SetHumanNotifier(desktopFixture{available: false})
	a["dry_run"], a["fence"] = false, p["fence"]
	f.call("merge_agents", a)
	f.stop()
	f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
	b, err := f.e.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := b["merged_agents"].([]core.Result)
	if !ok || len(rows) != 1 {
		t.Fatal("merge audit disappeared on replay")
	}
	actor := rows[0]["by"].(*core.MergeActor)
	if actor == nil || actor.ID != "lead" || actor.Authority != "coordinator" || !actor.Evidence.Known || actor.Evidence.Source != "worker" {
		t.Fatal("actor changed during replay")
	}
	if read := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); !mentions(read, "private original mail") {
		t.Fatal("inherited readability lost on replay")
	}
	if read := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.outgoing}); !mentions(read, "source outgoing question") {
		t.Fatal("sender ownership lost on replay")
	}
	assertMergedHistory(f)
	if r := f.raw("register", map[string]any{"name": "worker", "nonce": "source-secret"}); r["code"] != "E_SEAT_MERGED" || r["token"] != nil {
		t.Fatal("source credential tombstone lost on real encrypted ledger replay")
	}
}

func TestSupportedMergeSurvivorRequestRetryAndRecoveryCustody(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	_, token, err := f.e.HumanAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": token})
	a := f.args()
	a["token"] = f.into["token"]
	p := f.call("merge_agents", a)
	req := map[string]any{
		"token": f.into["token"], "to": "human", "type": "request", "body": "repair my duplicate",
		"op_id": "merge-control-1", "merge": map[string]any{
			"from": f.source["agent_id"], "into": f.into["agent_id"], "fence": p["fence"],
		},
	}
	first := f.call("send", req)
	retry := f.call("send", req)
	if retry["deduplicated"] != true || retry["msg_serial"] != first["msg_serial"] {
		t.Fatal("typed request retry created a second decision")
	}
	public := map[string]any{"name": "worker", "session_id": "worker-session"}
	if r := f.raw("register", public); r["code"] != "E_NEEDS_NONCE" {
		t.Fatalf("pending effect allowed public-session takeover: %v", r)
	}
	f.call("respond", map[string]any{"token": token, "msg_serial": first["msg_serial"], "disposition": "approve"})
	checkpoint := f.call("check_in", map[string]any{"token": f.into["token"]})
	if !mentions(checkpoint, "was merged into") {
		t.Fatal("survivor did not receive its completed merge outcome")
	}
	if r := f.raw("register", public); r["code"] != "E_NEEDS_NONCE" {
		t.Fatalf("merged mailbox allowed public-session takeover: %v", r)
	}
	if r := f.call("send", req); r["deduplicated"] != true || r["msg_serial"] != first["msg_serial"] {
		t.Fatal("same control request retried after effect did not deduplicate")
	}
}

func TestSupportedMergeHumanOverridesBindingsAndDeclarationsButNotLiveSource(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	_, human, err := f.e.HumanAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": human})
	f.call("check_in", map[string]any{"token": f.source["token"]})
	f.call("bind_session", map[string]any{"token": f.source["token"], "session_id": "source-owned-session"})
	f.call("declare", map[string]any{"token": f.source["token"], "text": "remaining source declaration", "waiting": "review"})
	a := f.args()
	a["token"] = human
	if r := f.raw("merge_agents", a); r["code"] != "E_AGENT_LIVE" {
		t.Fatal("human merged active source")
	}
	if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	p := f.call("merge_agents", a)
	source := p["source"].(map[string]any)
	if source["bound_session"] != true || source["declarations"] != float64(1) {
		t.Fatal("preview hid overridden conditions")
	}
	a["dry_run"], a["fence"] = false, p["fence"]
	f.call("merge_agents", a)
}

func TestSupportedMergeTypedEffectCannotBeAddressedToAnAgent(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	p := f.call("merge_agents", f.args())
	r := f.raw("send", map[string]any{
		"token": f.into["token"], "to": f.lead["agent_id"], "type": "request",
		"body": "approve this merge", "merge": map[string]any{"from": f.source["agent_id"], "into": f.into["agent_id"], "fence": p["fence"]},
	})
	if r["code"] != "E_MERGE_NEEDS_HUMAN" {
		t.Fatalf("nonhuman decision route accepted: %v", r)
	}
}

func TestSupportedMergeSessionDetailTracksTheCurrentAppThread(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	// App routing requires bridge evidence; a model-only surface claim is
	// intentionally ignored. The move under test has that observed provenance.
	updated := recoveryCall(t, f.srv, f.version, "update", map[string]any{"token": f.into["token"]},
		map[string]any{HostMetaKey: "fixture-host", SurfaceMetaKey: "chatgpt-app"})
	if updated["ok"] != true {
		t.Fatalf("observed app surface setup: %v", updated)
	}
	const old = "11111111-1111-4111-8111-111111111111"
	const current = "22222222-2222-4222-8222-222222222222"
	for _, sid := range []string{old, current} {
		f.call("bind_session", map[string]any{"token": f.into["token"], "session_id": sid})
	}
	b, err := f.e.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range b["agents"].([]map[string]any) {
		if row["id"] != f.into["agent_id"] {
			continue
		}
		found = true
		sessions := row["sessions"].(map[string]any)
		if sessions["current"] != current || sessions["app_thread"] != current || sessions["host_id"] != "fixture-host" {
			t.Fatalf("moved app identity is not host-paired/current: %v", sessions)
		}
	}
	if !found {
		t.Fatal("setup: survivor absent from board")
	}
	compact := f.call("check_in", map[string]any{"token": f.into["token"]})["board"]
	if mentions(map[string]any{"board": compact}, current) {
		t.Fatal("compact roster expanded session details")
	}
}

func TestSupportedMergeNativePanelCarriesSessionsAndTypedDecision(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	_, human, err := f.e.HumanAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": human})
	p := f.call("merge_agents", f.args())
	sent := f.call("send", map[string]any{
		"token": f.lead["token"], "to": "human", "type": "request", "body": "sender prose",
		"merge": map[string]any{"from": f.source["agent_id"], "into": f.into["agent_id"], "fence": p["fence"]},
	})
	result := rawToolResult(t, f.srv, "board", map[string]any{"token": human})
	meta := result["_meta"].(map[string]any)
	payload := asMap(meta[panelDataMetaKey])
	foundSession, foundEffect := false, false
	for _, row := range asMaps(asMap(payload["board"])["agents"]) {
		if row["id"] == f.into["agent_id"] {
			sessions := asMap(row["sessions"])
			foundSession = sessions["current"] == "worker-session" && sessions["host_id"] == "fixture-host"
		}
	}
	for _, m := range asMaps(payload["inbox"]) {
		if m["serial"] == sent["msg_serial"] {
			effect := asMap(m["merge"])
			foundEffect = effect["from"] == f.source["agent_id"] && effect["into"] == f.into["agent_id"]
		}
	}
	if !foundSession || !foundEffect {
		t.Fatalf("actual panel filter lost sessions=%v or typed effect=%v", !foundSession, !foundEffect)
	}
}

func TestSupportedMergeDesktopPresentationCarriesAnIndependentTypedEffect(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	_, human, err := f.e.HumanAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": human})
	asked := make(chan humanask.Message, 1)
	f.e.SetHumanNotifier(desktopFixture{available: true, ask: func(m humanask.Message) (humanask.Answer, error) {
		asked <- m
		return humanask.Answer{}, nil
	}})
	p := f.call("merge_agents", f.args())
	sent := f.call("send", map[string]any{
		"token": f.lead["token"], "to": "human", "type": "request", "body": "sender prose",
		"merge": map[string]any{"from": f.source["agent_id"], "into": f.into["agent_id"], "fence": p["fence"]},
	})
	select {
	case m := <-asked:
		if m.Merge == nil || m.Merge.From != f.source["agent_id"] || m.Merge.Into != f.into["agent_id"] {
			t.Fatal("desktop presenter lost typed direction")
		}
		m.Merge.Fence = "presenter mutation"
	case <-time.After(5 * time.Second):
		t.Fatal("real send did not reach desktop presenter")
	}
	saved := f.call("read_mail", map[string]any{"token": human, "msg_serial": sent["msg_serial"]})["message"].(map[string]any)
	if saved["merge"].(map[string]any)["fence"] == "presenter mutation" {
		t.Fatal("presenter mutated replayable state")
	}
}
