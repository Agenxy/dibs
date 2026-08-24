package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
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
		// The OTHER name one harness session goes by, joined by the daemon at
		// ingress. Frozen from the day it shipped, like every tag here: it
		// records which agent a lifecycle hook resolves to, so renaming it later
		// would silently unbind every agent on replay and wake nobody, reporting
		// success throughout.
		"session_alias": true,
		"slot_id":       true, "text": true, "dirs": true, "refs": true,
		"to": true, "msg_type": true, "body": true, "deadline_sec": true, "op_id": true,
		"path": true, "mode": true, "note": true,
		"space": true, "exclusive": true, "predicted": true,
		"score": true, "threshold": true, "scorer_id": true, "scorer_version": true,
		"evidence": true, "auto": true,
		"stale_agents": true, "alive_pids": true,
		"no_process": true, "adopt_authorised": true, "choices": true, "grant": true, "adopt": true,
		// Declared by core.Op and reachable on disk, though the fixture below
		// does not exercise every one. They were entirely unfrozen until the
		// type check underneath this list was added: seventeen tags, more than
		// a third of the format, protected by nothing. A rename of any of them
		// would have replayed as success with the field silently zero, which is
		// the exact failure `lane_kind` -> `agent_kind` caused across every
		// release to v0.0.4.
		"description": true, "proc_start": true, "resume_id": true,
		"parent": true, "parent_nonce": true, "claim_verified": true,
		"activity": true, "holds": true, "disposition": true,
		"msg_serial": true, "msg_serials": true, "attachments": true,
		"blob": true, "mime": true, "size": true,
		"dead_agents": true, "give_up_announce": true,
		// purge_mail: whether THIS sweep may take a purged agent's mailbox with
		// it. Absent on every sweep written before v0.0.7, and that absence is
		// load-bearing: it is what makes those ops replay with the semantics
		// they were written under instead of today's.
		"purge_mail": true,
	}

	// Every tag the Op DECLARES, not merely the ones this fixture happens to
	// write. The check below compares what reached disk, which protects a field
	// once it is in use and is blind to one that is not yet: `no_process` and
	// `adopt_authorised` were both added and neither tripped anything, because
	// no op in the fixture set them. They are on disk the moment a human
	// registers or a mailbox is adopted, so "not exercised here" is not the
	// same as "not part of the format".
	//
	// A new wire field must be declared. That is the whole cost of the rule,
	// and it is what stops the next one being invisible.
	for _, tag := range declaredOpTags() {
		if !wantOp[tag] {
			t.Errorf("core.Op declares json tag %q, which is not in this test's frozen "+
				"list. Every field that can reach disk is part of the on-disk format, "+
				"whether or not this fixture writes it: add it here deliberately, and "+
				"remember that RENAMING one later is silent data loss rather than a "+
				"rename", tag)
		}
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
	// Updated deliberately when `session_alias` was added, and again for
	// `purge_mail`: one new tag each time, no rename. If you are here because a
	// sweep moved this value, the sweep is the bug, and the tag it renamed is
	// the data loss.
	frozenOpFingerprint       = "sha256:f66bb393b72daf67"
	frozenEnvelopeFingerprint = "sha256:fa4924db73ff6cd9"
	// The Message list had no fingerprint, and the list it guards sits in the
	// same file as the tags it is guarding. A sweep that renames `json:"grant"`
	// renames the adjacent `"grant": true` with it and this test goes on
	// passing: the exact co-edited-guard failure AGENTS.md describes, in the
	// guard written to stop it. Found by a pre-release review.
	frozenMessageFingerprint = "sha256:fda0b5aee19d524a"
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
	// ONE table, because two of them drifted.
	//
	// This was a map of constants iterated against a SEPARATE map of expected
	// strings, and the outer one omitted OpAdoptAgent and OpSpaceRetitle. Their
	// expected values sat in the inner map, unreachable, because the loop never
	// supplied those names: the comment said the new kinds were pinned and
	// renaming either left the test green. A pre-release review found it.
	//
	// That is the co-edited-guard shape again, in the arrangement that makes it
	// hardest to see: nothing was wrong with either map, only with the fact that
	// membership of one decided whether the other was consulted at all. Paired
	// here so a kind added to the table brings its expected string with it or
	// does not compile.
	for name, pair := range map[string]struct{ got, want string }{
		"OpRegister":       {core.OpRegister, "register"},
		"OpResume":         {core.OpResume, "resume"},
		"OpWake":           {core.OpWake, "wake"},
		"OpAckBoard":       {core.OpAckBoard, "check_in"},
		"OpUpdate":         {core.OpUpdate, "update"},
		"OpSignOff":        {core.OpSignOff, "sign_off"},
		"OpHeartbeat":      {core.OpHeartbeat, "heartbeat"},
		"OpSetSlot":        {core.OpSetSlot, "declare"},
		"OpClearSlot":      {core.OpClearSlot, "undeclare"},
		"OpSendMessage":    {core.OpSendMessage, "send"},
		"OpClaim":          {core.OpClaim, "claim"},
		"OpRelease":        {core.OpRelease, "release"},
		"OpSweep":          {core.OpSweep, "sweep"},
		"OpMarkDelivered":  {core.OpMarkDelivered, "mark_delivered"},
		"OpPutBlob":        {core.OpPutBlob, "put_blob"},
		"OpGrantRole":      {core.OpGrantRole, "grant_role"},
		"OpPrune":          {core.OpPrune, "prune"},
		"OpForceRelease":   {core.OpForceRelease, "force_release"},
		"OpSpaceOpen":      {core.OpSpaceOpen, "open_space"},
		"OpSpaceJoin":      {core.OpSpaceJoin, "join_space"},
		"OpSpaceLeave":     {core.OpSpaceLeave, "leave_space"},
		"OpSpaceSubscribe": {core.OpSpaceSubscribe, "watch_space"},
		"OpSpaceExclusive": {core.OpSpaceExclusive, "lock_space"},
		"OpSpacePost":      {core.OpSpacePost, "post"},
		"OpSpaceAnnounce":  {core.OpSpaceAnnounce, "announce"},
		"OpSpaceAck":       {core.OpSpaceAck, "ack_announcement"},
		"OpAdoptAgent":     {core.OpAdoptAgent, "adopt_agent"},
		"OpSpaceRetitle":   {core.OpSpaceRetitle, "retitle_space"},
	} {
		// FROZEN AGAIN, at new values, and the break was deliberate. 0.0.3 renamed
		// the product to Dibs and its vocabulary with it, and these strings went
		// along, because leaving `register` inside a tool called `register` is the
		// kind of seam that outlives everyone who remembers why. A 0.0.2 ledger
		// therefore cannot be replayed by 0.0.3: the daemon refuses to start and
		// says so, with `verify` and `admin repair-ledger` offered.
		got, want := pair.got, pair.want
		if got != want {
			t.Errorf("%s = %q, must stay %q: every ledger written since 0.0.3 uses "+
				"this value, and Apply matches it by string", name, got, want)
		}
	}
}

// The MESSAGE is on disk too, and was guarded by nothing.
//
// This file froze core.Op's tags by reflection and stopped there, but a Message
// is state, state is a fold over the ledger, and its fields are serialised with
// it. Renaming one is the same silent data loss the whole file exists to stop:
// the op applies, replay reports success, and the field is quietly zero. 0.0.6
// added three of them (`choices`, `grant`, `adopt`), each of which decides what
// approving a request DOES, so a rename would turn an approval into a no-op
// that reports success.
//
// Found by an independent review before release, in the same pass that found
// the two op kinds above.
func TestLedgerMessageFieldNamesAreFrozen(t *testing.T) {
	frozen := map[string]bool{
		"serial": true, "from": true, "to": true, "type": true, "body": true,
		"state": true, "consumed": true, "deadline": true, "response": true,
		"delivered_serial": true, "sent_at": true, "delivered_at": true,
		"responded_serial": true, "acked_serial": true, "terminal_at": true,
		"expire_detail": true, "attachments": true,
		// 0.0.6.
		"choices": true, "grant": true, "adopt": true,
	}
	var declared []string
	mt := reflect.TypeOf(core.Message{})
	for i := range mt.NumField() {
		tag := mt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if c := strings.Index(tag, ","); c >= 0 {
			tag = tag[:c]
		}
		if tag == "" {
			continue
		}
		declared = append(declared, tag)
		if !frozen[tag] {
			t.Errorf("core.Message declares json tag %q, which is not frozen here. A "+
				"message is state, and state is a fold over the ledger: renaming this "+
				"later applies cleanly, replays cleanly, and silently zeroes the "+
				"field. Add it deliberately", tag)
		}
	}
	// The fingerprint, for the reason the constant gives: a text sweep can keep
	// the tag and the list in step with each other, and cannot keep either in
	// step with this.
	if got := fingerprint(frozen); got != frozenMessageFingerprint {
		t.Errorf("the frozen message-field list changed.\n"+
			"  got  %s\n  want %s\n\n"+
			"  If you renamed a tag: every ledger written before the change replays\n"+
			"  with that field silently zero, and for `grant` or `adopt` that turns\n"+
			"  an approval into a no-op that reports success. Put the old tag back.\n"+
			"  If you genuinely added a field, this is deliberate: set\n"+
			"    frozenMessageFingerprint = %q", got, frozenMessageFingerprint, got)
	}

	// The other half of the same loss: a field REMOVED is a field that stops
	// being written, and every reader of an older ledger keeps expecting it.
	if len(declared) != len(frozen) {
		t.Errorf("core.Message declares %d tags and %d are frozen: a field was "+
			"removed or renamed", len(declared), len(frozen))
	}
}

// declaredOpTags lists every json tag on core.Op, so the frozen list above is
// checked against the type rather than against whatever the fixture exercised.
func declaredOpTags() []string {
	var out []string
	t := reflect.TypeOf(core.Op{})
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if i := strings.Index(tag, ","); i >= 0 {
			tag = tag[:i]
		}
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}
