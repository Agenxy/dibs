package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The ledger's on-disk field names are FROZEN. This test is what makes that
// true rather than merely intended.
//
// Every other test in this package writes and reads with the same code, so a
// renamed struct tag is invisible to all of them: the new name is written, the
// new name is read, everything passes, and every ledger written before the
// rename becomes unreplayable. The daemon would refuse to start on a board that
// was working yesterday, and `dibs verify` would say the chain is intact
// because the bytes are unchanged: the hash chain protects the LINES, not the
// meaning of the keys inside them.
//
// This is not hypothetical, and it is no longer even a warning: it happened
// here. The rename of the participant from `Lane` to `Agent` touched 196 Go
// identifiers, several carrying `json:"lane_kind"`. The compiler cannot help,
// because both spellings compile, and neither could this test, because the
// same text sweep that renamed the tags rewrote the frozen list below to match,
// along with the sentence you are reading, which said "from `Agent` to `Agent`"
// until someone read it. Every release up to v0.0.4 wrote `lane_kind`; the
// build after it read `agent_kind`; this test passed throughout, and every
// persistent agent on an upgraded board came back ephemeral.
//
// So the list is fingerprinted. A literal list defends against a careless
// rename; a fingerprint defends against a thorough one, because a sweep that
// rewrites the words will not rewrite the hash. Updating the wire format now
// takes two deliberate edits, and that is the entire point of it.
//
// A Go identifier may be renamed freely; the tag it carries may not.
func TestLedgerFieldNamesAreFrozen(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	// Representative of every op family that carries payload, so the frozen set
	// below covers the tags that actually reach disk.
	apply(t, st, led, &core.Op{
		Kind: core.OpRegister, Name: "alpha", NewToken: "ta",
		Nonce: "n-a", AgentKind: core.KindPersistent, PID: 42, SessionID: "s-1",
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
	apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: "beta", NewToken: "tb"}, t0.Add(4*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "tb"}, t0.Add(5*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSendMessage, Token: "ta", To: "beta",
		MsgType: core.MsgQuestion, Body: "?", OpID: "op-1", DeadlineSec: 60,
	}, t0.Add(6*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "ta", Space: "work",
		Text: "a topic", Exclusive: true,
		Predicted: []core.PredFile{{Path: "a.go", Weight: 1}},
	}, t0.Add(7*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceJoin, Token: "tb", Space: "work",
		Score: 0.8, Threshold: 0.3, ScorerID: "s", ScorerVersion: "1",
		Evidence: []string{"a.go"}, Auto: true,
	}, t0.Add(8*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSweep, StaleAgents: []string{"beta"},
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
		"kind": true, "agent": true, "name": true, "pid": true, "token": true,
		"nonce": true, "agent_kind": true, "session_id": true, "agent_id": true,
		"slot_id": true, "text": true, "dirs": true, "refs": true,
		"to": true, "msg_type": true, "body": true, "deadline_sec": true, "op_id": true,
		"path": true, "mode": true, "note": true,
		"space": true, "exclusive": true, "predicted": true,
		"score": true, "threshold": true, "scorer_id": true, "scorer_version": true,
		"evidence": true, "auto": true,
		"stale_agents": true, "alive_pids": true,
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
				t.Errorf("UNEXPECTED ledger envelope field %q: the on-disk format changed. "+
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
				t.Errorf("UNEXPECTED op field %q in a %s op: a struct tag changed. "+
					"Renaming the Go identifier is fine; renaming the tag breaks every "+
					"ledger written before the change.", k, opKind(op))
			}
		}
	}

	// The other direction: a tag that VANISHES is just as bad, and a test that
	// only checks for unexpected keys would not notice.
	for _, k := range []string{"kind", "agent", "name", "token", "space", "score", "predicted"} {
		if !seenOp[k] {
			t.Errorf("op field %q is no longer written: an existing ledger's %q "+
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

	// The fingerprint. See the doc comment: this is what a text sweep cannot
	// keep in step with, and it is the only reason a rename has to be noticed.
	if got := fingerprint(wantOp); got != frozenOpFingerprint {
		t.Errorf("the frozen op-field list changed.\n"+
			"  got  %s\n  want %s\n\n"+
			"  If you renamed a tag: every ledger written before the change replays\n"+
			"  with that field silently zero. Put the old tag back.\n"+
			"  If you genuinely added a field, this is deliberate: set\n"+
			"    frozenOpFingerprint = %q", got, frozenOpFingerprint, got)
	}
	if got := fingerprint(wantEnvelope); got != frozenEnvelopeFingerprint {
		t.Errorf("the frozen envelope-field list changed: got %s, want %s",
			got, frozenEnvelopeFingerprint)
	}
}

// Fingerprints of the frozen sets above. Deliberately not derived from anything
// at build time: a value a sweep can recompute defends nothing.
const (
	frozenOpFingerprint       = "sha256:a6b92b35ecc6140e"
	frozenEnvelopeFingerprint = "sha256:fa4924db73ff6cd9"
)

func fingerprint(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, ",")))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func opKind(op map[string]json.RawMessage) string {
	var k string
	_ = json.Unmarshal(op["kind"], &k)
	return k
}

// The op KIND strings are the other half of the on-disk contract: they are
// matched by value in Apply's switch, so a renamed constant makes every
// historical op of that kind fall through to "unknown" and stop replaying.
// Re-frozen at "wake" and "prune" on 2026-08-12, deliberately.
//
// A board written before this cannot be replayed by a version after it, and
// that is the second such break in this file's history. It was taken because
// "lane" is not a concept in Dibs any more and carrying the word in the
// persisted format to avoid one migration would mean carrying it forever.
//
// The blast radius was measured rather than assumed: these are the only two op
// kinds whose value still contained the old noun, and a real board of 96 ops
// contained neither. A ledger is affected only if it ever recorded a
// stalled-subagent wake or an admin prune.
func TestOpKindStringsAreFrozen(t *testing.T) {
	for name, got := range map[string]string{
		"OpRegister": core.OpRegister, "OpResume": core.OpResume,
		"OpWake": core.OpWake, "OpAckBoard": core.OpAckBoard,
		"OpUpdate": core.OpUpdate, "OpSignOff": core.OpSignOff,
		"OpHeartbeat": core.OpHeartbeat, "OpSetSlot": core.OpSetSlot,
		"OpClearSlot": core.OpClearSlot, "OpSendMessage": core.OpSendMessage,
		"OpClaim": core.OpClaim, "OpRelease": core.OpRelease,
		"OpSweep": core.OpSweep, "OpMarkDelivered": core.OpMarkDelivered,
		"OpPutBlob": core.OpPutBlob, "OpGrantRole": core.OpGrantRole,
		"OpPrune": core.OpPrune, "OpForceRelease": core.OpForceRelease,
		"OpSpaceOpen": core.OpSpaceOpen, "OpSpaceJoin": core.OpSpaceJoin,
		"OpSpaceLeave": core.OpSpaceLeave, "OpSpaceSubscribe": core.OpSpaceSubscribe,
		"OpSpaceExclusive": core.OpSpaceExclusive, "OpSpacePost": core.OpSpacePost,
		"OpSpaceAnnounce": core.OpSpaceAnnounce, "OpSpaceAck": core.OpSpaceAck,
	} {
		// FROZEN AGAIN, at new values, and the break was deliberate.
		//
		// 0.0.3 renamed the product to Dibs and its vocabulary with it, and these
		// strings went along because leaving `register` inside a tool called
		// `register` is the kind of seam that outlives everyone who remembers why.
		// A 0.0.2 ledger therefore cannot be replayed by 0.0.3: the daemon refuses
		// to start and says so, with `verify` and `admin repair-ledger` offered,
		// which is the honest failure rather than a silent one.
		//
		// That was affordable exactly once, at 0.0.x with a handful of users. It
		// is not affordable again. From here these are append-only: a new op gets
		// a new constant, and an existing one keeps its string forever, because
		// every ledger already written is read by every version that follows.
		want := map[string]string{
			"OpRegister": "register", "OpResume": "resume",
			"OpWake": "wake", "OpAckBoard": "check_in",
			"OpUpdate": "update", "OpSignOff": "sign_off",
			"OpHeartbeat": "heartbeat", "OpSetSlot": "declare",
			"OpClearSlot": "undeclare", "OpSendMessage": "send",
			"OpClaim": "claim", "OpRelease": "release",
			"OpSweep": "sweep", "OpMarkDelivered": "mark_delivered",
			"OpPutBlob": "put_blob", "OpGrantRole": "grant_role",
			"OpPrune": "prune", "OpForceRelease": "force_release",
			"OpSpaceOpen": "open_space", "OpSpaceJoin": "join_space",
			"OpSpaceLeave": "leave_space", "OpSpaceSubscribe": "watch_space",
			"OpSpaceExclusive": "lock_space", "OpSpacePost": "post",
			"OpSpaceAnnounce": "announce", "OpSpaceAck": "ack_announcement",
		}[name]
		if got != want {
			t.Errorf("%s = %q, must stay %q: every ledger written since 0.0.3 uses "+
				"this value, and Apply matches it by string", name, got, want)
		}
	}
}
