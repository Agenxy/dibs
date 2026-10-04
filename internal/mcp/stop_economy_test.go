package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

func readStopLedger(t *testing.T, dir string) *core.State {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.OpenReadOnly(filepath.Join(dir, "ledger.jsonl"), "fixture", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	s := core.NewState("fixture", core.DefaultLimits())
	if _, err = l.Replay(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Actual production hook_poll over HTTP, real encrypted persistence, no
// hand-set notices or decision flags. Informational progress must survive a
// Stop which sends no model context, then arrive once through check_in.
func TestStopEconomyThroughMCP(t *testing.T) {
	for _, item := range []string{
		"progress", "ordinary-approval", "accepted-review", "notify", "queue", "done-without-wait",
		"question", "request", "handoff", "human-notify", "answer", "deny", "decline", "flagged-review", "grant", "adopt", "done-with-wait", "announcement",
	} {
		for _, event := range []string{"Stop", "SubagentStop"} {
			t.Run(item+"/"+event, func(t *testing.T) {
				dir := t.TempDir()
				srv, eng, _ := restartableQueueServer(t, dir)
				call := func(name string, args map[string]any) map[string]any {
					t.Helper()
					r := toolCall(t, srv, name, args)
					if r["__is_error"] == true {
						t.Fatalf("setup %s: %v", name, r)
					}
					return r
				}
				lead := call("register", map[string]any{"name": "lead", "nonce": "stop-lead", "session_id": "stop-lead-session"})["token"].(string)
				worker := call("register", map[string]any{"name": "worker", "nonce": "stop-worker", "session_id": "stop-worker-session"})["token"].(string)
				call("check_in", map[string]any{"token": lead})
				call("check_in", map[string]any{"token": worker})
				token, session := lead, "stop-lead-session"
				marker := "typed-stop-" + item
				actionable := true
				switch item {
				case "progress", "ordinary-approval", "accepted-review", "notify", "queue", "done-without-wait":
					actionable = false
				}
				switch item {
				case "notify", "question", "request", "handoff":
					call("send", map[string]any{"token": worker, "to": "lead", "type": item, "body": marker})
				case "human-notify":
					_, humanToken, err := eng.HumanAgent(context.Background())
					if err != nil {
						t.Fatal("setup: human:", err)
					}
					call("send", map[string]any{"token": humanToken, "to": "lead", "type": "notify", "body": marker})
				case "announcement":
					call("open_space", map[string]any{"token": worker, "space": "stop-proof", "topic": "proof"})
					call("join_space", map[string]any{"token": lead, "space": "stop-proof"})
					call("check_in", map[string]any{"token": worker})
					call("announce", map[string]any{"token": worker, "space": "stop-proof", "body": "announcement"})
					marker = "ack_announcement"
				case "grant", "adopt":
					human, humanToken, err := eng.HumanAgent(context.Background())
					if err != nil {
						t.Fatal("setup: human:", err)
					}
					args := map[string]any{"token": lead, "to": human, "type": "request", "body": "permission"}
					if item == "grant" {
						args["grant"] = "coordinator"
					} else {
						lost := call("register", map[string]any{"name": "lost", "session_id": "lost-session"})["agent_id"].(string)
						call("send", map[string]any{"token": worker, "to": lost, "type": "notify", "body": "stranded mail"})
						if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{lost}}); err != nil {
							t.Fatal("setup: abandoned mailbox:", err)
						}
						args["adopt"] = lost
					}
					n := call("send", args)["msg_serial"]
					call("respond", map[string]any{"token": humanToken, "msg_serial": n, "disposition": "approve", "body": marker})
				default:
					kind := "request"
					if item == "answer" {
						kind = "question"
					}
					args := map[string]any{"token": lead, "to": "worker", "type": kind, "body": "work"}
					if kind == "request" {
						args["milestones"] = []string{"one"}
					}
					n := call("send", args)["msg_serial"]
					if item == "answer" || item == "deny" || item == "decline" || item == "queue" {
						call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": item, "body": marker})
					} else {
						call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve", "body": marker})
						if item != "ordinary-approval" {
							call("check_in", map[string]any{"token": lead})
						}
						if item == "progress" {
							call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "milestone": 1, "body": marker})
						}
						if item == "accepted-review" || item == "flagged-review" {
							call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "milestone": 1, "body": "reported"})
							disposition := "accept"
							if item == "flagged-review" {
								disposition = "flag"
							}
							call("respond", map[string]any{"token": lead, "msg_serial": n, "disposition": disposition, "milestone": 1, "body": marker})
							token, session = worker, "stop-worker-session"
						}
						if strings.HasPrefix(item, "done-") {
							call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "milestone": 1, "body": "reported"})
							call("check_in", map[string]any{"token": lead})
							if item == "done-with-wait" {
								call("declare", map[string]any{"token": lead, "text": "waiting for worker", "waiting": "worker"})
							}
							call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "done", "body": marker})
						}
					}
				}
				before := readStopLedger(t, dir)
				got := call("hook_poll", map[string]any{"session_id": session, "event": event, "strict_output": true})
				if actionable {
					if got["decision"] != "block" || !mentions(got, marker) {
						t.Fatalf("actionable %s not delivered: %v", item, got)
					}
					return
				}
				if got["decision"] == "block" || got["reason"] != nil || got["hookSpecificOutput"] != nil {
					t.Fatalf("informational %s forced context/turn: %v", item, got)
				}
				after := readStopLedger(t, dir)
				if before.Serial != after.Serial {
					t.Fatalf("non-blocking Stop mutated ledger %d->%d", before.Serial, after.Serial)
				}
				for n, m := range before.Messages {
					other := after.Messages[n]
					if other == nil || m.OutcomeReadAt != other.OutcomeReadAt || m.ReviewReadAt != other.ReviewReadAt || m.DeliveredAt != other.DeliveredAt || m.Consumed != other.Consumed {
						t.Fatalf("non-delivering Stop consumed %d", n)
					}
				}
				first := call("check_in", map[string]any{"token": token})
				if !mentions(first, marker) {
					t.Fatalf("held item disappeared: %v", first)
				}
				second := call("check_in", map[string]any{"token": token})
				if item != "notify" && strings.Contains(fmt.Sprint(second["agent_updates"]), marker) {
					t.Fatalf("outcome repeated after real read: %v", second)
				}
			})
		}
	}
}

func TestStopEconomyHeldProgressRidesActionableDelivery(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := restartableQueueServer(t, dir)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r := toolCall(t, srv, name, args)
		if r["__is_error"] == true {
			t.Fatalf("setup %s: %v", name, r)
		}
		return r
	}
	lead := call("register", map[string]any{"name": "lead", "session_id": "riding-lead"})["token"].(string)
	worker := call("register", map[string]any{"name": "worker"})["token"].(string)
	call("check_in", map[string]any{"token": lead})
	call("check_in", map[string]any{"token": worker})
	n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work", "milestones": []string{"one"}})["msg_serial"]
	call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "approve"})
	call("check_in", map[string]any{"token": lead})
	call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "progress", "milestone": 1, "body": "riding-progress"})
	first := call("hook_poll", map[string]any{"session_id": "riding-lead", "event": "Stop", "strict_output": true})
	if first["decision"] == "block" {
		t.Fatalf("progress alone bought a turn: %v", first)
	}
	call("send", map[string]any{"token": worker, "to": "lead", "type": "question", "body": "riding-question"})
	second := call("hook_poll", map[string]any{"session_id": "riding-lead", "event": "Stop", "strict_output": true})
	if second["decision"] != "block" || !mentions(second, "riding-progress") || !mentions(second, "riding-question") {
		t.Fatalf("actionable digest lost held information: %v", second)
	}
	pull := call("check_in", map[string]any{"token": lead})
	if strings.Contains(fmt.Sprint(pull["agent_updates"]), "riding-progress") {
		t.Fatalf("fully quoted progress repeated after read receipt: %v", pull)
	}
}

func TestStopEconomyQueueDoesNotOfferASocketWake(t *testing.T) {
	dir := t.TempDir()
	srv, eng, _ := restartableQueueServer(t, dir)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r := toolCall(t, srv, name, args)
		if r["__is_error"] == true {
			t.Fatalf("setup %s: %v", name, r)
		}
		return r
	}
	lead := call("register", map[string]any{"name": "lead", "session_id": "queue-lead"})["token"].(string)
	worker := call("register", map[string]any{"name": "worker"})["token"].(string)
	call("check_in", map[string]any{"token": lead})
	call("check_in", map[string]any{"token": worker})
	n := call("send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "work"})["msg_serial"]
	call("respond", map[string]any{"token": worker, "msg_serial": n, "disposition": "queue", "body": "queued-information"})
	call("hook_poll", map[string]any{"session_id": "queue-lead", "event": "Stop", "stop_hook_active": true, "strict_output": true})
	before := readStopLedger(t, dir)
	offer, err := eng.SocketOfferFor(context.Background(), lead, "queue-lead", "", false)
	if err != nil || offer["digest"] != "" {
		t.Fatalf("queue alone offered a native wake: %v %v", offer, err)
	}
	if after := readStopLedger(t, dir); after.Serial != before.Serial {
		t.Fatalf("observing held queue mutated ledger %d->%d", before.Serial, after.Serial)
	}
	if pull := call("check_in", map[string]any{"token": lead}); !mentions(pull, "queued-information") {
		t.Fatalf("suppressed queue wake lost information: %v", pull)
	}
}
