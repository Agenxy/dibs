package ledger

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/lanes/internal/core"
)

// The ledger's on-disk field names are FROZEN. This test is what makes that
// true rather than merely intended.
//
// Every other test in this package writes and reads with the same code, so a
// renamed struct tag is invisible to all of them: the new name is written, the
// new name is read, everything passes, and every ledger written before the
// rename becomes unreplayable. The daemon would refuse to start on a board that
// was working yesterday, and `lanes verify` would say the chain is intact
// because the bytes are unchanged — the hash chain protects the LINES, not the
// meaning of the keys inside them.
//
// This is not hypothetical. SPEC-CHANNELS.md §1 renames the participant from
// `Lane` to `Agent`, which is 196 Go identifiers, several of which carry
// `json:"lane"`. A careless rename does exactly the above. The compiler cannot
// help — both spellings compile.
//
// So: the wire names are asserted against a literal list. Changing one requires
// changing this file, which is the point. A Go identifier may be renamed freely;
// the tag it carries may not.
func TestLedgerFieldNamesAreFrozen(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	// Representative of every op family that carries payload, so the frozen set
	// below covers the tags that actually reach disk.
	apply(t, st, led, &core.Op{
		Kind: core.OpRegisterLane, Name: "alpha", NewToken: "ta",
		Nonce: "n-a", LaneKind: core.KindPersistent, PID: 42, SessionID: "s-1",
		Agent: &core.AgentInfo{Harness: "test", CWD: "/tmp", Host: "h"},
	}, t0)
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "ta"}, t0.Add(time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSetSlot, Token: "ta", Text: "work",
		Dirs: []string{"/d"}, Refs: []string{"pr:1"},
	}, t0.Add(2*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpClaim, Token: "ta", Path: "/repo",
		Mode: core.ClaimExclusive, Note: "mine",
	}, t0.Add(3*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpRegisterLane, Name: "beta", NewToken: "tb"}, t0.Add(4*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "tb"}, t0.Add(5*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSendMessage, Token: "ta", To: "beta",
		MsgType: core.MsgQuestion, Body: "?", OpID: "op-1", DeadlineSec: 60,
	}, t0.Add(6*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpLaneOpen, Token: "ta", Channel: "work",
		Text: "a topic", Exclusive: true,
		Predicted: []core.PredFile{{Path: "a.go", Weight: 1}},
	}, t0.Add(7*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpLaneJoin, Token: "tb", Channel: "work",
		Score: 0.8, Threshold: 0.3, ScorerID: "s", ScorerVersion: "1",
		Evidence: []string{"a.go"}, Auto: true,
	}, t0.Add(8*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSweep, StaleLanes: []string{"beta"},
		AlivePIDs: []int{42},
	}, t0.Add(9*time.Second))
	_ = led.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The envelope: what every ledger line carries.
	wantEnvelope := map[string]bool{
		"s": true, "t": true, "prev": true, "op": true, "n": true, "e": true,
	}
	// The op payload: the union of every tag that reached disk above.
	wantOp := map[string]bool{
		"kind": true, "lane": true, "name": true, "pid": true, "token": true,
		"nonce": true, "lane_kind": true, "session_id": true, "agent": true,
		"slot_id": true, "text": true, "dirs": true, "refs": true,
		"to": true, "msg_type": true, "body": true, "deadline_sec": true, "op_id": true,
		"path": true, "mode": true, "note": true,
		"channel": true, "exclusive": true, "predicted": true,
		"score": true, "threshold": true, "scorer_id": true, "scorer_version": true,
		"evidence": true, "auto": true,
		"stale_lanes": true, "alive_pids": true,
	}

	seenEnvelope := map[string]bool{}
	seenOp := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("ledger line is not JSON: %v", err)
		}
		for k := range rec {
			seenEnvelope[k] = true
			if !wantEnvelope[k] {
				t.Errorf("UNEXPECTED ledger envelope field %q — the on-disk format changed. "+
					"If this is deliberate, existing ledgers stop replaying; if it is a rename, "+
					"put the old tag back.", k)
			}
		}
		// The op is encrypted at rest, so decrypt through the box the way Replay
		// does rather than reading the ciphertext.
		var l Line
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("ledger line does not fit Line: %v", err)
		}
		if err := led.box.DecryptOp(l.Op); err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		blob, err := json.Marshal(l.Op)
		if err != nil {
			t.Fatal(err)
		}
		var op map[string]json.RawMessage
		if err := json.Unmarshal(blob, &op); err != nil {
			t.Fatal(err)
		}
		for k := range op {
			seenOp[k] = true
			if !wantOp[k] {
				t.Errorf("UNEXPECTED op field %q in a %s op — a struct tag changed. "+
					"Renaming the Go identifier is fine; renaming the tag breaks every "+
					"ledger written before the change.", k, opKind(op))
			}
		}
	}

	// The other direction: a tag that VANISHES is just as bad, and a test that
	// only checks for unexpected keys would not notice.
	for _, k := range []string{"kind", "lane", "name", "token", "channel", "score", "predicted"} {
		if !seenOp[k] {
			t.Errorf("op field %q is no longer written — an existing ledger's %q "+
				"would be silently ignored on replay", k, k)
		}
	}
	for _, k := range []string{"s", "t", "prev", "op"} {
		if !seenEnvelope[k] {
			t.Errorf("ledger envelope field %q is no longer written", k)
		}
	}

	if t.Failed() {
		var got []string
		for k := range seenOp {
			got = append(got, k)
		}
		sort.Strings(got)
		t.Logf("op fields actually written: %s", strings.Join(got, " "))
	}
}

func opKind(op map[string]json.RawMessage) string {
	var k string
	_ = json.Unmarshal(op["kind"], &k)
	return k
}

// The op KIND strings are the other half of the on-disk contract: they are
// matched by value in Apply's switch, so a renamed constant makes every
// historical op of that kind fall through to "unknown" and stop replaying.
func TestOpKindStringsAreFrozen(t *testing.T) {
	for name, got := range map[string]string{
		"OpRegisterLane": core.OpRegisterLane, "OpResumeLane": core.OpResumeLane,
		"OpWakeLane": core.OpWakeLane, "OpAckBoard": core.OpAckBoard,
		"OpUpdateLane": core.OpUpdateLane, "OpCloseLane": core.OpCloseLane,
		"OpHeartbeat": core.OpHeartbeat, "OpSetSlot": core.OpSetSlot,
		"OpClearSlot": core.OpClearSlot, "OpSendMessage": core.OpSendMessage,
		"OpClaim": core.OpClaim, "OpRelease": core.OpRelease,
		"OpSweep": core.OpSweep, "OpMarkDelivered": core.OpMarkDelivered,
		"OpPutBlob": core.OpPutBlob, "OpGrantRole": core.OpGrantRole,
		"OpPruneLane": core.OpPruneLane, "OpForceRelease": core.OpForceRelease,
		"OpLaneOpen": core.OpLaneOpen, "OpLaneJoin": core.OpLaneJoin,
		"OpLaneLeave": core.OpLaneLeave, "OpLaneSubscribe": core.OpLaneSubscribe,
		"OpLaneExclusive": core.OpLaneExclusive, "OpLanePost": core.OpLanePost,
		"OpLaneAnnounce": core.OpLaneAnnounce, "OpLaneAck": core.OpLaneAck,
	} {
		want := map[string]string{
			"OpRegisterLane": "register_lane", "OpResumeLane": "resume_lane",
			"OpWakeLane": "wake_lane", "OpAckBoard": "ack_board",
			"OpUpdateLane": "update_lane", "OpCloseLane": "close_lane",
			"OpHeartbeat": "heartbeat", "OpSetSlot": "set_slot",
			"OpClearSlot": "clear_slot", "OpSendMessage": "send_message",
			"OpClaim": "claim", "OpRelease": "release",
			"OpSweep": "sweep", "OpMarkDelivered": "mark_delivered",
			"OpPutBlob": "put_blob", "OpGrantRole": "grant_role",
			"OpPruneLane": "prune_lane", "OpForceRelease": "force_release",
			"OpLaneOpen": "lane_open", "OpLaneJoin": "lane_join",
			"OpLaneLeave": "lane_leave", "OpLaneSubscribe": "lane_subscribe",
			"OpLaneExclusive": "lane_exclusive", "OpLanePost": "lane_post",
			"OpLaneAnnounce": "lane_announce", "OpLaneAck": "lane_ack",
		}[name]
		if got != want {
			t.Errorf("%s = %q, must stay %q — every ledger ever written uses the old "+
				"value, and Apply matches it by string", name, got, want)
		}
	}
}
