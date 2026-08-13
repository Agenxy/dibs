package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

var t0 = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

func newLedger(t *testing.T) (*Ledger, string) {
	t.Helper()
	dir := t.TempDir()
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	led, err := Open(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	return led, path
}

// apply mirrors the engine's rule: ledger iff the serial advanced.
func apply(t *testing.T, st *core.State, led *Ledger, op *core.Op, now time.Time) core.Result {
	t.Helper()
	res, err := applyE(st, led, op, now)
	if err != nil {
		t.Fatalf("%s: %v", op.Kind, err)
	}
	return res
}

func applyE(st *core.State, led *Ledger, op *core.Op, now time.Time) (core.Result, error) {
	before := st.Serial
	res, _, err := st.Apply(op, now)
	if err != nil {
		return nil, err
	}
	if st.Serial != before {
		if lerr := led.Append(st.Serial, now, op); lerr != nil {
			return nil, lerr
		}
	}
	return res, nil
}

func reopen(t *testing.T, path string) *core.State {
	t.Helper()
	box, err := LoadOrCreateKey(filepath.Join(filepath.Dir(path), "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := Open(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	st := core.NewState("test", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatalf("replay: %v", err)
	}
	return st
}

// TestReplayDeterminism covers the full v1.0 op surface: register (both
// kinds), resume, wake, checkpoint, check_in, send with op_id, respond,
// ack/consume, claims, sweeps with recorded decisions, GC.
func TestReplayDeterminism(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: "alpha", NewToken: "ta"}, t0)
	apply(t, st, led, &core.Op{
		Kind: core.OpRegister, Name: "rev", NewToken: "tr",
		Nonce: "nonce-r", AgentKind: core.KindPersistent,
	}, t0.Add(time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "ta"}, t0.Add(2*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpSetSlot, Token: "ta", Text: "building"}, t0.Add(3*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSendMessage, Token: "ta", To: "rev",
		MsgType: core.MsgQuestion, Body: "secret plans?", OpID: "op-1",
	}, t0.Add(4*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpClaim, Token: "ta", Path: "/repo", Mode: core.ClaimExclusive}, t0.Add(5*time.Second))
	// Dormancy, resume with rotation, wake, checkpoint, consumption.
	apply(t, st, led, &core.Op{Kind: core.OpSweep, StaleAgents: []string{"rev"}}, t0.Add(10*time.Minute))
	apply(t, st, led, &core.Op{Kind: core.OpResume, Nonce: "nonce-r", ResumeID: "r1", NewToken: "tr2", PID: 99}, t0.Add(20*time.Minute))
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "tr2"}, t0.Add(21*time.Minute))
	apply(t, st, led, &core.Op{Kind: core.OpRespond, Token: "tr2", MsgSerial: 5, Disposition: "answer", Body: "yes"}, t0.Add(22*time.Minute))
	apply(t, st, led, &core.Op{Kind: core.OpActivityCheckpoint, Token: "ta"}, t0.Add(23*time.Minute))
	_ = led.Close()

	st2 := reopen(t, path)
	if st2.Serial != st.Serial {
		t.Fatalf("serial %d != %d", st2.Serial, st.Serial)
	}
	if !reflect.DeepEqual(st.Board(), st2.Board()) {
		t.Fatal("board mismatch after replay")
	}
	if st2.AgentByToken("tr2") == nil || st2.AgentByToken("tr") != nil {
		t.Fatal("token rotation lost in replay")
	}
	if st2.Agents["rev"].Activation != 1 {
		t.Fatalf("activation lost: %d", st2.Agents["rev"].Activation)
	}
	if got := st2.Messages[5].Response; got != "yes" {
		t.Fatalf("response after replay = %q", got)
	}
	if _, ok := st2.Dedup["alpha\x00op-1"]; !ok {
		t.Fatal("dedup record lost in replay")
	}
}

func TestEncryptionAtRestIncludesNonce(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())
	apply(t, st, led, &core.Op{
		Kind: core.OpRegister, Name: "a", NewToken: "super-secret-token",
		Nonce: "super-secret-nonce", AgentKind: core.KindPersistent,
	}, t0)
	apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: "b", NewToken: "tb"}, t0)
	apply(t, st, led, &core.Op{
		Kind: core.OpSendMessage, Token: "super-secret-token", To: "b",
		MsgType: core.MsgNotify, Body: "the private body",
	}, t0)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret-token", "super-secret-nonce", "the private body"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%q appears in plaintext in the ledger", secret)
		}
	}
	if !strings.Contains(string(raw), "register") {
		t.Fatal("public op kinds should be plaintext")
	}
}

func TestTornTailTruncation(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())
	apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: "a", NewToken: "ta"}, t0)
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "ta"}, t0)
	_ = led.Close()

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"s":3,"t":"2026-07-22T12:00:0`)
	_ = f.Close()

	st2 := reopen(t, path)
	if st2.Serial != 2 {
		t.Fatalf("serial after torn tail = %d, want 2", st2.Serial)
	}
	if n, _, err := Verify(path); err != nil || n != 2 {
		t.Fatalf("verify after truncation: n=%d err=%v", n, err)
	}
}

func TestHashChainDetectsTampering(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())
	for _, n := range []string{"a", "b", "c"} {
		apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: n, NewToken: "t" + n}, t0)
	}
	_ = led.Close()
	raw, _ := os.ReadFile(path)
	tampered := strings.Replace(string(raw), `"name":"b"`, `"name":"x"`, 1)
	if tampered == string(raw) {
		t.Fatal("test setup: no substitution")
	}
	_ = os.WriteFile(path, []byte(tampered), 0o600)
	if _, _, err := Verify(path); err == nil {
		t.Fatal("tampering not detected")
	}
}

// TestRandomizedReplayEquivalence: seeded random op/time sequences through
// live state + ledger, then replay and compare: the load-bearing gate
// (SPEC §17), now covering the full v1.0 op surface.
func TestRandomizedReplayEquivalence(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())
	rng := rand.New(rand.NewSource(42))
	var tokens []string
	var announceSerials []uint64  // real serials, so ack_announcement can hit one
	coordinator := ""             // a real coordinator, so the director ops land
	accepted := map[string]int{}  // which op kinds actually landed
	nonces := map[string]string{} // token → nonce for persistent agents
	now := t0

	// A deterministic prologue, so what the gate COVERS does not depend on the
	// draw: only how hard it is stressed does.
	//
	// Without this the run is seed-fragile: seed 12345 produced zero
	// announcements and tripped the vacuity guard, while seeds 1 and 7 passed.
	// A gate whose coverage varies by seed teaches you to re-run it rather than
	// to trust it.
	for i, n := range []string{"seedа", "seedb", "seedc"} {
		tok := "seedtok" + itoa(i)
		apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: n, NewToken: tok}, now)
		apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: tok}, now)
		tokens = append(tokens, tok)
	}
	// Counted into `accepted` like any other op: the prologue exists precisely
	// so these four are covered on EVERY seed, and a guard that ignored them
	// would still fail on the draws where the walk happens to miss one.
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "seedtokaa",
		Space: "seedspace", Text: "seeded work",
	}, now)
	accepted[core.OpSpaceOpen]++
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceJoin, Token: "seedtokba",
		Space: "seedspace", Score: 0.66, ScorerID: "seed",
	}, now)
	accepted[core.OpSpaceJoin]++
	seeded := apply(t, st, led, &core.Op{
		Kind: core.OpSpaceAnnounce, Token: "seedtokaa",
		Space: "seedspace", Body: "seeded announcement",
	}, now)
	accepted[core.OpSpaceAnnounce]++
	if ser, ok := seeded["serial"].(uint64); ok {
		announceSerials = append(announceSerials, ser)
	}
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceAck, Token: "seedtokba",
		MsgSerial: announceSerials[0],
	}, now)
	accepted[core.OpSpaceAck]++
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceLeave, Token: "seedtokba",
		Space: "seedspace",
	}, now)
	accepted[core.OpSpaceLeave]++

	for i := 0; i < 1500; i++ {
		now = now.Add(time.Duration(rng.Intn(30000)) * time.Millisecond)
		var op *core.Op
		switch k := rng.Intn(40); {
		case k == 0 && len(tokens) < 15:
			tok := "tok" + itoa(len(tokens))
			op = &core.Op{Kind: core.OpRegister, Name: "agent" + tok, NewToken: tok}
			if rng.Intn(3) == 0 {
				op.AgentKind = core.KindPersistent
				op.Nonce = "nonce-" + tok
				nonces[tok] = op.Nonce
			}
			tokens = append(tokens, tok)
		case len(tokens) == 0:
			continue
		case k <= 2:
			op = &core.Op{Kind: core.OpAckBoard, Token: pick(rng, tokens)}
		case k <= 4:
			op = &core.Op{Kind: core.OpSetSlot, Token: pick(rng, tokens), Text: "work"}
		case k <= 6:
			op = &core.Op{
				Kind: core.OpSendMessage, Token: pick(rng, tokens),
				To: "agent" + pick(rng, tokens), MsgType: pickType(rng), Body: "b",
				OpID: maybeOpID(rng, i),
			}
		case k == 7:
			op = &core.Op{
				Kind: core.OpClaim, Token: pick(rng, tokens),
				Path: "/p" + pick(rng, tokens), Mode: pickMode(rng),
			}
		case k == 8:
			op = &core.Op{Kind: core.OpSweep, StaleAgents: staleSubset(rng, st)}
		case k == 9:
			op = &core.Op{Kind: core.OpAckMessage, Token: pick(rng, tokens), MsgSerial: uint64(rng.Intn(int(st.Serial + 1)))}
		case k == 10:
			op = &core.Op{
				Kind: core.OpRespond, Token: pick(rng, tokens),
				MsgSerial: uint64(rng.Intn(int(st.Serial + 1))), Disposition: pickDisp(rng), Body: "r",
			}
		case k == 11:
			tok := pick(rng, tokens)
			if n, ok := nonces[tok]; ok {
				op = &core.Op{Kind: core.OpResume, Nonce: n, ResumeID: "r" + itoa(i), NewToken: tok} // rotate to same token: keeps map valid
			} else {
				op = &core.Op{Kind: core.OpHeartbeat, Token: tok}
			}
		case k == 12:
			op = &core.Op{Kind: core.OpWake, Token: pick(rng, tokens)}
		// Spaces (SPEC-CHANNELS.md). Included here because this is the
		// load-bearing determinism gate (SPEC §17), and space membership is
		// the one piece of state decided by an IMPURE input: a similarity
		// score. If Apply ever recomputes one instead of taking the recorded
		// value, this is the test that catches it.
		case k == 14:
			op = &core.Op{
				Kind: core.OpSpaceOpen, Token: pick(rng, tokens),
				Space: "agent" + itoa(rng.Intn(6)), Text: "topic",
				Exclusive: rng.Intn(3) == 0,
			}
		case k == 15:
			op = &core.Op{
				Kind: core.OpSpaceJoin, Token: pick(rng, tokens),
				Space: "agent" + itoa(rng.Intn(6)),
				Score: rng.Float64(), Threshold: 0.327,
				ScorerID: "lexical+cochange", ScorerVersion: "1",
				Evidence: []string{"a.go", "b.go"}, Auto: rng.Intn(2) == 0,
			}
		case k == 16:
			op = &core.Op{
				Kind: core.OpSpaceAnnounce, Token: pick(rng, tokens),
				Space: "agent" + itoa(rng.Intn(6)), Body: "announcement",
			}
		case k == 17:
			op = &core.Op{
				Kind: core.OpSpaceAck, Token: pick(rng, tokens),
				MsgSerial: uint64(rng.Intn(int(st.Serial + 1))),
			}
		case k == 18:
			op = &core.Op{
				Kind: core.OpSpaceExclusive, Token: pick(rng, tokens),
				Space: "agent" + itoa(rng.Intn(6)),
				Mode:  []string{"exclusive", "release"}[rng.Intn(2)],
			}
		case k == 19:
			op = &core.Op{
				Kind: core.OpSpaceLeave, Token: pick(rng, tokens),
				Space: "agent" + itoa(rng.Intn(6)),
			}
		// The four below were absent from this walk, and all four turned out to
		// be broken in the same way: a mutation the fold could not reproduce.
		// A determinism gate only covers the ops it generates, so an op missing
		// from this switch is an op with no determinism guarantee at all,
		// whatever the rest of the suite says about it.
		case k == 13:
			op = &core.Op{
				Kind: core.OpSpaceSubscribe, Token: pick(rng, tokens),
				Space: "agent" + itoa(rng.Intn(6)),
				Mode:  []string{"", "release"}[rng.Intn(2)],
			}
		case k == 20:
			op = &core.Op{
				Kind: core.OpSpacePost, Token: pick(rng, tokens),
				Space: "agent" + itoa(rng.Intn(6)), Body: "post",
			}
		case k == 21:
			// Reattach: the same nonce arriving on a NEW token, which is what a
			// restarted agent actually sends. Both branches emit agent.reattached.
			tok := pick(rng, tokens)
			if n, ok := nonces[tok]; ok {
				fresh := "re" + itoa(i)
				op = &core.Op{
					Kind: core.OpRegister, Name: "agent" + tok,
					Nonce: n, NewToken: fresh,
				}
				tokens = append(tokens, fresh)
				nonces[fresh] = n
			} else {
				op = &core.Op{
					Kind: core.OpBindSession, Token: tok,
					SessionID: "sess-" + itoa(rng.Intn(5)),
				}
			}
		case k == 22 && i > 900:
			// Late, and rarely: pruning early would empty the board and starve
			// every other branch of agents to act on.
			if rng.Intn(8) == 0 {
				op = &core.Op{Kind: core.OpPrune, To: "agent" + pick(rng, tokens)}
			} else {
				op = &core.Op{Kind: core.OpBindSession, Token: pick(rng, tokens), SessionID: "s" + itoa(i)}
			}
		default:
			op = &core.Op{Kind: core.OpActivityCheckpoint, Token: pick(rng, tokens)}
		}
		res, err := applyE(st, led, op, now)
		if err != nil {
			continue // rejected ops are never ledgered
		}
		accepted[op.Kind]++
		if op.Kind == core.OpSpaceAnnounce {
			if ser, ok := res["serial"].(uint64); ok {
				announceSerials = append(announceSerials, ser)
			}
		}
		// Promote the first agent that opens an agent, so the director branches
		// have somebody to run as.
		if coordinator == "" && op.Kind == core.OpSpaceOpen {
			agent, _ := res["agent_id"].(string)
			owner := st.AgentByToken(op.Token)
			if owner != nil && agent != "" {
				if _, gerr := applyE(st, led, &core.Op{
					Kind: core.OpGrantRole,
					To:   owner.ID, Mode: core.RoleCoordinator,
				}, now); gerr == nil {
					coordinator = op.Token
					accepted[core.OpGrantRole]++
				}
			}
		}
	}
	_ = led.Close()

	st2 := reopen(t, path)
	if st2.Serial != st.Serial {
		t.Fatalf("serial: replay %d != live %d", st2.Serial, st.Serial)
	}
	// The WHOLE state, not a list of fields somebody remembered to add.
	//
	// Every field-by-field assertion below predates this and stays, because
	// each names what diverged and this cannot. But the enumeration is the
	// weakness: Subs and Posts were both absent from replayed state for months
	// while this gate passed, because nothing compared them and nobody thought
	// to. State's own doc comment says it is "the entire replayable truth", so
	// the honest check is to compare the entire thing and let a new collection
	// be covered on the day it is added rather than the day somebody notices.
	if diff := stateDiff(st, st2); diff != "" {
		t.Fatalf("randomized: replayed state differs from live state: %s", diff)
	}
	if !reflect.DeepEqual(st.Board(), st2.Board()) {
		t.Fatal("randomized: board mismatch after replay")
	}
	if len(st2.Messages) != len(st.Messages) || len(st2.Dedup) != len(st.Dedup) {
		t.Fatalf("randomized: messages/dedup divergence (%d/%d vs %d/%d)",
			len(st2.Messages), len(st2.Dedup), len(st.Messages), len(st.Dedup))
	}
	if len(st2.Spaces) != len(st.Spaces) {
		t.Fatalf("randomized: space divergence (%d vs %d)", len(st2.Spaces), len(st.Spaces))
	}
	// Guard against a vacuous pass: measured on what was EXERCISED, not on what
	// survived.
	//
	// An earlier version counted members left on the board at the end, and was
	// seed-fragile for a reason that turned out to be correct behaviour:
	// evictions, merges and departures legitimately remove members, so a run
	// that exercised spaces HARDER finished with fewer. Counting accepted ops
	// measures the thing the gate is actually for.
	for _, kind := range []string{
		core.OpSpaceOpen, core.OpSpaceJoin, core.OpSpaceLeave, core.OpSpaceAnnounce,
		core.OpSpaceSubscribe, core.OpSpacePost, core.OpBindSession, core.OpPrune,
	} {
		if accepted[kind] == 0 {
			t.Errorf("%s was never accepted in 1500 ops: the gate is not covering it", kind)
		}
	}
	t.Logf("accepted space ops: %v", accepted)

	// Membership, exclusivity and QUEUE ORDER must all survive byte-identically:
	// queue order decides who gets admitted next, so a reordering on replay is a
	// different fleet, not a cosmetic difference.
	for id, live := range st.Spaces {
		replayed := st2.Spaces[id]
		if replayed == nil {
			t.Fatalf("randomized: agent %s lost in replay", id)
		}
		if replayed.Owner != live.Owner {
			t.Fatalf("randomized: agent %s owner %q != %q", id, replayed.Owner, live.Owner)
		}
		if !reflect.DeepEqual(replayed.Queue, live.Queue) {
			t.Fatalf("randomized: agent %s queue %v != %v", id, replayed.Queue, live.Queue)
		}
		if len(replayed.Members) != len(live.Members) {
			t.Fatalf("randomized: agent %s members %d != %d", id, len(replayed.Members), len(live.Members))
		}
		// Subscribers, because they are not private to the subscriber: the post
		// event's audience count and what a merge carries across both read this
		// set, so a fold that loses it is a different board.
		if !reflect.DeepEqual(replayed.Subs, live.Subs) {
			t.Fatalf("randomized: agent %s subs %v != %v", id, replayed.Subs, live.Subs)
		}
		// Posts, because read_space is the only way to reach one: a fold that
		// loses them loses the content itself, not merely a count.
		if !reflect.DeepEqual(replayed.Posts, live.Posts) {
			t.Fatalf("randomized: agent %s posts %d != %d", id, len(replayed.Posts), len(live.Posts))
		}
		// Pending explicitly, because stateDiff CANNOT see it: the field is
		// `json:"-"` so it never reaches the marshalled form the whole-state
		// comparison works from. It is still replayed state: it decides what
		// provenance a queued agent is promoted with, so it needs its own
		// assertion rather than the assumption that the general check covers
		// everything.
		if !reflect.DeepEqual(replayed.Pending, live.Pending) {
			t.Fatalf("randomized: agent %s pending %v != %v", id, replayed.Pending, live.Pending)
		}
		for a, m := range live.Members {
			r := replayed.Members[a]
			if r == nil || r.Score != m.Score || r.JoinedSerial != m.JoinedSerial || r.Auto != m.Auto {
				t.Fatalf("randomized: agent %s membership %s diverged: %+v vs %+v", id, a, r, m)
			}
		}
	}
	// Announcement STATE includes `unacked`, which the sweep sets from a
	// recorded decision: the same discipline as membership scores.
	for serial, live := range st.Announcements {
		r := st2.Announcements[serial]
		if r == nil || r.State != live.State || len(r.Acked) != len(live.Acked) ||
			len(r.Required) != len(live.Required) {
			t.Fatalf("randomized: announcement %d diverged: %+v vs %+v", serial, r, live)
		}
	}
}

func pick(rng *rand.Rand, ss []string) string { return ss[rng.Intn(len(ss))] }
func itoa(i int) string                       { return string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) }

func pickType(rng *rand.Rand) string {
	return []string{core.MsgNotify, core.MsgQuestion, core.MsgRequest, core.MsgHandoff}[rng.Intn(4)]
}

func pickMode(rng *rand.Rand) string {
	return []string{core.ClaimShared, core.ClaimExclusive}[rng.Intn(2)]
}

func pickDisp(rng *rand.Rand) string {
	return []string{"answer", "approve", "deny", "decline"}[rng.Intn(4)]
}

func maybeOpID(rng *rand.Rand, i int) string {
	if rng.Intn(2) == 0 {
		return ""
	}
	return "op" + itoa(i)
}

// staleSubset picks a random subset of live agents to declare stale.
//
// SORTED, and that is load-bearing. It used to range over st.Agents directly,
// a Go map, whose iteration order is randomised on every run. That made the
// "seeded" generator non-reproducible in two ways at once: a different subset
// went stale, and the per-entry rng.Intn call consumed draws in a different
// order, desynchronising every decision after it. The same seed passed on one
// run and failed on the next.
//
// A determinism gate that is not itself deterministic teaches you to re-run it
// instead of to trust it, which is worse than having no gate at all.
func staleSubset(rng *rand.Rand, st *core.State) []string {
	ids := make([]string, 0, len(st.Agents))
	for id := range st.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []string
	for _, id := range ids {
		if st.Agents[id].Status == core.StatusActive && rng.Intn(6) == 0 {
			out = append(out, id)
		}
	}
	return out
}

func blobID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestBlobReplayDeterminism proves the attachment registry (blob.registered,
// owner_added, evicted) and message attachments replay exactly from the ledger,
// and (the load-bearing A1 invariant) that NO blob bytes ever enter the
// ledger (only metadata handles do).
func TestBlobReplayDeterminism(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: "alpha", NewToken: "ta"}, t0)
	apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: "beta", NewToken: "tb"}, t0)
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "ta"}, t0)
	// Two agents put the SAME content: registered then owner_added.
	body := "the-generated-dataset-bytes"
	id := blobID(body)
	apply(t, st, led, &core.Op{Kind: core.OpPutBlob, Token: "ta", Blob: id, Size: int64(len(body)), Mime: "application/json"}, t0)
	apply(t, st, led, &core.Op{Kind: core.OpPutBlob, Token: "tb", Blob: id, Size: int64(len(body))}, t0)
	// Attach on a message + a fileref.
	apply(t, st, led, &core.Op{
		Kind: core.OpSendMessage, Token: "ta", To: "beta",
		MsgType: core.MsgHandoff, Body: "here you go", Attachments: []core.Attachment{
			{Blob: id}, {Path: "/big/local/file", Size: 1 << 30, Hash: "abcd"},
		},
	}, t0)
	_ = led.Close()

	st2 := reopen(t, path)
	if st2.Serial != st.Serial {
		t.Fatalf("serial %d != %d", st2.Serial, st.Serial)
	}
	b := st2.Blobs[id]
	if b == nil {
		t.Fatal("blob registry entry lost in replay")
	}
	if !b.Owners["alpha"] || !b.Owners["beta"] {
		t.Fatalf("owner set not replayed: %+v", b.Owners)
	}
	if b.Mime != "application/json" || b.Size != int64(len(body)) {
		t.Fatalf("blob metadata not replayed: %+v", b)
	}
	// Message attachments (blob handle + fileref) replayed verbatim.
	var msg *core.Message
	for _, m := range st2.Messages {
		if len(m.Attachments) > 0 {
			msg = m
		}
	}
	if msg == nil || len(msg.Attachments) != 2 {
		t.Fatalf("message attachments not replayed: %+v", msg)
	}
	if !reflect.DeepEqual(st.Blobs[id].Owners, st2.Blobs[id].Owners) {
		t.Fatal("owner sets diverge after replay")
	}
	// A1 invariant: the raw blob bytes must NEVER appear in the ledger.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), body) {
		t.Fatal("blob plaintext bytes leaked into the ledger (A1 violated)")
	}
	// The metadata handle (the sha256 id) SHOULD be present: it's a safe hash.
	if !strings.Contains(string(raw), id) {
		t.Fatal("blob id handle missing from ledger")
	}
}

// TestChannelReplayDeterminism is the proof of SPEC-CHANNELS.md §4.3.
//
// Space membership is decided by a similarity SCORE, which is impure: recompute
// it next week against a reindexed repository and it is a different number. If
// Apply ever recomputed one, replaying this ledger would reconstruct different
// membership and the hash chain would stop meaning anything.
//
// So the scores travel IN THE OP, exactly as the sweep's PID verdicts do, and
// this test replays a ledger full of space activity on a state that has no
// repository, no index and no scorer, and demands byte-identical membership,
// queue order, exclusivity and announcement state out the other side.
func TestChannelReplayDeterminism(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	for i, n := range []string{"alpha", "beta", "gamma"} {
		apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: n, NewToken: "t" + n},
			t0.Add(time.Duration(i)*time.Second))
		apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "t" + n},
			t0.Add(time.Duration(i)*time.Second+500*time.Millisecond))
	}

	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "talpha",
		Space: "auth-refactor", Text: "reworking the auth middleware",
	}, t0.Add(10*time.Second))
	// An auto-join carrying a recorded score and its evidence.
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceJoin, Token: "tbeta", Space: "auth-refactor",
		Score: 0.8137, Threshold: 0.327, ScorerID: "lexical+cochange", ScorerVersion: "1",
		Evidence: []string{"internal/mcp/identity.go", "internal/core/roles.go"}, Auto: true,
	},
		t0.Add(11*time.Second))
	ann := apply(t, st, led, &core.Op{
		Kind: core.OpSpaceAnnounce, Token: "talpha",
		Space: "auth-refactor", Body: "renaming AgentInfo.Token",
	}, t0.Add(12*time.Second))
	annSerial, ok := ann["serial"].(uint64)
	if !ok {
		t.Fatalf("announce must return its serial, got %v", ann)
	}
	apply(t, st, led, &core.Op{Kind: core.OpSpaceAck, Token: "tbeta", MsgSerial: annSerial}, t0.Add(13*time.Second))
	// A second, exclusive space with somebody queued behind it.
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "tgamma",
		Space: "hot", Text: "single-writer work", Exclusive: true,
	}, t0.Add(14*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceJoin, Token: "talpha", Space: "hot",
		Score: 0.91, Threshold: 0.327, ScorerID: "lexical+cochange",
	}, t0.Add(15*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpacePost, Token: "tbeta",
		Space: "auth-refactor", Body: "halfway through",
	}, t0.Add(16*time.Second))
	_ = led.Close()

	st2 := reopen(t, path)
	if st2.Serial != st.Serial {
		t.Fatalf("serial %d != %d", st2.Serial, st.Serial)
	}
	if len(st2.Spaces) != len(st.Spaces) {
		t.Fatalf("space count %d != %d", len(st2.Spaces), len(st.Spaces))
	}

	auth := st2.Spaces["auth-refactor"]
	if auth == nil {
		t.Fatal("auth-refactor lost in replay")
	}
	m := auth.Members["beta"]
	if m == nil {
		t.Fatal("beta's membership lost in replay")
	}
	// The exact recorded numbers, reconstructed with no scorer in the process.
	if m.Score != 0.8137 || m.Threshold != 0.327 {
		t.Fatalf("score/threshold not replayed verbatim: %v/%v", m.Score, m.Threshold)
	}
	if m.ScorerID != "lexical+cochange" || m.ScorerVersion != "1" || !m.Auto {
		t.Fatalf("scorer provenance lost in replay: %+v", m)
	}
	if len(m.Evidence) != 2 || m.Evidence[0] != "internal/mcp/identity.go" {
		t.Fatalf("evidence lost in replay: %v", m.Evidence)
	}
	if m.JoinedSerial != st.Spaces["auth-refactor"].Members["beta"].JoinedSerial {
		t.Fatal("joined serial diverged")
	}

	hot := st2.Spaces["hot"]
	if hot == nil || hot.Owner != "gamma" {
		t.Fatalf("exclusivity lost in replay: %+v", hot)
	}
	if len(hot.Queue) != 1 || hot.Queue[0] != "alpha" {
		t.Fatalf("queue lost or reordered in replay: %v", hot.Queue)
	}
	if _, joined := hot.Members["alpha"]; joined {
		t.Fatal("a queued agent must not replay as a member")
	}

	// Announcement state, including who still owes an acknowledgement.
	var found bool
	for _, a := range st2.Announcements {
		if a.Space != "auth-refactor" {
			continue
		}
		found = true
		if !a.Acked["beta"] {
			t.Fatal("beta's ack lost in replay")
		}
		if a.State != core.AnnounceAcked {
			t.Fatalf("announcement should replay as settled, got %q", a.State)
		}
	}
	if !found {
		t.Fatal("announcement lost in replay")
	}

	if !reflect.DeepEqual(st.Board(), st2.Board()) {
		t.Fatal("board mismatch after replay")
	}
}

// TestDirectorReplayDeterminism pins the coordinator's space powers
// (SPEC-CHANNELS.md §8.1) and subagent inheritance (§8.2) through a real ledger
// round-trip.
//
// Deterministic rather than fuzzed because both need a specific setup: a
// granted role, a locked agent, a declared parent: that a random walk reaches
// too rarely to depend on. TestRandomizedReplayEquivalence covers the ops that
// fuzz well; this covers the ones that do not.
func TestDirectorReplayDeterminism(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	for i, n := range []string{"owner", "waiter", "boss", "stray"} {
		apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: n, NewToken: "t" + n},
			t0.Add(time.Duration(i)*time.Second))
		apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "t" + n},
			t0.Add(time.Duration(i)*time.Second+500*time.Millisecond))
	}
	// A subagent of "owner", which joins nothing and inherits everything,
	// vouched for by its parent, because lineage that is merely asserted grants
	// nothing. The voucher is part of what replay must reproduce.
	apply(t, st, led, &core.Op{
		Kind: core.OpVouchChild, Token: "towner", Nonce: "helper-nonce-0123456789abcdef",
	}, t0.Add(4500*time.Millisecond))
	apply(t, st, led, &core.Op{
		Kind: core.OpRegister, Name: "helper",
		NewToken: "thelper", Parent: "owner", ParentNonce: "helper-nonce-0123456789abcdef",
	}, t0.Add(5*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "thelper"}, t0.Add(6*time.Second))

	apply(t, st, led, &core.Op{Kind: core.OpGrantRole, To: "boss", Mode: core.RoleCoordinator},
		t0.Add(7*time.Second))

	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "towner",
		Space: "locked", Text: "single-writer", Exclusive: true,
	}, t0.Add(8*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpSpaceJoin, Token: "twaiter", Space: "locked"},
		t0.Add(9*time.Second))
	// The subagent speaks in its parent's agent without ever joining.
	apply(t, st, led, &core.Op{
		Kind: core.OpSpacePost, Token: "thelper",
		Space: "locked", Body: "from the helper",
	}, t0.Add(10*time.Second))
	// The director unsticks it.
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceForceRelease, Token: "tboss",
		Space: "locked", Note: "owner gone",
	}, t0.Add(11*time.Second))

	// A second agent, an eviction, and a merge.
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "tstray",
		Space: "side", Text: "adjacent",
	}, t0.Add(12*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpSpaceJoin, Token: "twaiter", Space: "side"},
		t0.Add(13*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceEvict, Token: "tboss",
		Space: "side", To: "waiter", Note: "wrong agent",
	}, t0.Add(14*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceMerge, Token: "tboss",
		Space: "side", To: "locked", Note: "same job",
	}, t0.Add(15*time.Second))
	// An announcement the sweep later gives up on.
	ann := apply(t, st, led, &core.Op{
		Kind: core.OpSpaceAnnounce, Token: "towner",
		Space: "locked", Body: "nobody will answer this",
	}, t0.Add(16*time.Second))
	serial, ok := ann["serial"].(uint64)
	if !ok {
		t.Fatalf("announce returned no serial: %v", ann)
	}
	apply(t, st, led, &core.Op{Kind: core.OpSweep, GiveUpAnnounce: []uint64{serial}},
		t0.Add(17*time.Second))
	_ = led.Close()

	st2 := reopen(t, path)
	if st2.Serial != st.Serial {
		t.Fatalf("serial %d != %d", st2.Serial, st.Serial)
	}
	if _, gone := st2.Spaces["side"]; gone {
		t.Fatal("the merged-away agent came back on replay")
	}
	locked := st2.Spaces["locked"]
	if locked == nil {
		t.Fatal("the surviving agent is missing after replay")
	}
	if locked.Owner != "" {
		t.Fatalf("force-release did not replay: still owned by %q", locked.Owner)
	}
	if _, in := locked.Members["stray"]; !in {
		t.Fatalf("the merge did not replay: members=%v", locked.Members)
	}
	if _, in := locked.Members["helper"]; in {
		t.Fatal("a subagent must not replay as a member in its own right")
	}
	if st2.Agents["helper"].Parent != "owner" {
		t.Fatalf("the parent link did not replay: %q", st2.Agents["helper"].Parent)
	}
	if got := st2.Announcements[serial].State; got != core.AnnounceUnacked {
		t.Fatalf("the abandoned announcement replayed as %q, want %q", got, core.AnnounceUnacked)
	}
	if !reflect.DeepEqual(st.Board(), st2.Board()) {
		t.Fatal("board mismatch after replay")
	}
}

// Reclamation DELETES state, which makes it the sharpest test of `state ==
// fold(ledger)`: a deletion the fold does not reproduce leaves a phantom agent on
// every restart, and a deletion the fold performs but the live daemon did not is
// worse still.
//
// This exists because reclaiming empty agents was added late, to stop automatic
// agent creation from exhausting the 64-agent cap. It runs inside the sweep: the
// one op that is ledgered only when it changed something, so "the sweep removed
// an agent" and "the sweep did nothing" have to be distinguishable in the ledger,
// not merely in memory.
func TestReclaimedSpacesStayReclaimedAcrossReplay(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	for i, n := range []string{"alpha", "beta"} {
		apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: n, NewToken: "t" + n},
			t0.Add(time.Duration(i)*time.Second))
		apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "t" + n},
			t0.Add(time.Duration(i)*time.Second+500*time.Millisecond))
	}
	// One agent everybody leaves, one that keeps a member. Auto, because only a
	// agent Dibs opened from a declaration is reclaimable: one a human opened on
	// purpose outlives its members, which is what standing agents are for.
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "talpha", Space: "abandoned", Text: "work nobody kept",
		Auto: true,
	}, t0.Add(10*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "tbeta", Space: "kept", Text: "work with somebody in it",
	}, t0.Add(11*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceLeave, Token: "talpha", Space: "abandoned",
	}, t0.Add(12*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpSweep}, t0.Add(13*time.Second))

	if _, alive := st.Spaces["abandoned"]; alive {
		t.Fatal("setup: the abandoned agent should have been reclaimed live")
	}
	if _, alive := st.Spaces["kept"]; !alive {
		t.Fatal("setup: an agent with a member must survive")
	}
	_ = led.Close()

	st2 := reopen(t, path)
	if st2.Serial != st.Serial {
		t.Fatalf("serial %d != %d: the sweep that reclaimed an agent did not ledger "+
			"identically", st2.Serial, st.Serial)
	}
	if _, back := st2.Spaces["abandoned"]; back {
		t.Error("a reclaimed agent came back on replay: it will return on every restart")
	}
	if _, gone := st2.Spaces["kept"]; !gone {
		t.Error("replay reclaimed an agent the live daemon kept")
	}
	if len(st2.Spaces) != len(st.Spaces) {
		t.Errorf("space count diverged: %d replayed vs %d live", len(st2.Spaces), len(st.Spaces))
	}
}

// Agent traffic is CONTENT and must be sealed like mail.
//
// Mail bodies were encrypted at rest and agent bodies were not, though both carry
// the same promise: read_space is membership-gated, revoked on leave or eviction,
// and SECURITY.md states announcement bodies are unreachable on the token-less
// path. All of that holds for the running daemon and none of it survives a
// COPIED ledger, a backup, a support bundle, a pasted reproduction, where an
// announcement body sat in plaintext beside a sealed message body.
//
// Found by an agent reading the ledger of a candidate build, not by reading the
// code: from inside the daemon the two surfaces look identical.
func TestSpaceTrafficIsSealedAtRestLikeMail(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())

	for i, n := range []string{"alpha", "beta"} {
		apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: n, NewToken: "t" + n},
			t0.Add(time.Duration(i)*time.Second))
		apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "t" + n},
			t0.Add(time.Duration(i)*time.Second+500*time.Millisecond))
	}
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceOpen, Token: "talpha", Space: "w", Text: "work",
	}, t0.Add(10*time.Second))
	apply(t, st, led, &core.Op{Kind: core.OpSpaceJoin, Token: "tbeta", Space: "w"},
		t0.Add(11*time.Second))

	const (
		mail     = "MAIL_BODY_THAT_MUST_NOT_APPEAR"
		announce = "ANNOUNCE_BODY_THAT_MUST_NOT_APPEAR"
		post     = "POST_BODY_THAT_MUST_NOT_APPEAR"
	)
	apply(t, st, led, &core.Op{
		Kind: core.OpSendMessage, Token: "talpha", To: "beta", MsgType: "notify", Body: mail,
	}, t0.Add(12*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpaceAnnounce, Token: "talpha", Space: "w", Body: announce,
	}, t0.Add(13*time.Second))
	apply(t, st, led, &core.Op{
		Kind: core.OpSpacePost, Token: "talpha", Space: "w", Body: post,
	}, t0.Add(14*time.Second))
	_ = led.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	for _, secret := range []string{mail, announce, post} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("%q is plaintext on disk: anyone handed a copy of this ledger "+
				"reads agent-scoped content the live daemon would refuse them", secret)
		}
	}

	// ...and it still round-trips, or sealing has cost the thing it protects.
	st2 := reopen(t, path)
	found := 0
	for _, a := range st2.Announcements {
		if a.Body == announce {
			found++
		}
	}
	if found != 1 {
		t.Errorf("the announcement body did not survive replay (%d matches)", found)
	}
}

// stateDiff compares two states as data.
//
// It has one known blind spot: fields tagged `json:"-"` are invisible to it.
// Space.Pending is the only one today and the randomized test asserts it
// separately, but a new `json:"-"` field on replayed state gets no coverage
// here and will pass silently, so add its own assertion when you add one. It goes through JSON rather than
// reflect.DeepEqual because time.Time carries a monotonic reading in a live
// process and never after a replay, and comparing those directly reports a
// difference on every single run, which is a gate that cannot be used.
func stateDiff(live, replayed *core.State) string {
	a, err := json.Marshal(live)
	if err != nil {
		return "marshal live: " + err.Error()
	}
	b, err := json.Marshal(replayed)
	if err != nil {
		return "marshal replayed: " + err.Error()
	}
	if bytes.Equal(a, b) {
		return ""
	}
	// Report the first divergence rather than two multi-megabyte blobs.
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			from := max(0, i-120)
			return fmt.Sprintf("at byte %d:\n live: …%s…\n repl: …%s…", i,
				a[from:min(len(a), i+120)], b[from:min(len(b), i+120)])
		}
	}
	return fmt.Sprintf("replayed state is a %d-byte prefix of the live one (%d bytes)", len(b), len(a))
}

// A crash between write and fsync leaves a partial final record. Discarding it
// is correct: the daemon died before answering the caller, so nothing was
// promised, but Replay TRUNCATES the file to do it, and it did so without
// saying anything. "ledger replayed ops=7" is indistinguishable from a board
// that always had seven, so a run that quietly loses writes for some other
// reason leaves no trail at all.
func TestATornFinalRecordIsReportedNotJustSwallowed(t *testing.T) {
	led, path := newLedger(t)
	st := core.NewState("test", core.DefaultLimits())
	now := t0
	apply(t, st, led, &core.Op{Kind: core.OpRegister, Name: "a", NewToken: "tok"}, now)
	apply(t, st, led, &core.Op{Kind: core.OpAckBoard, Token: "tok"}, now)
	apply(t, st, led, &core.Op{Kind: core.OpSetSlot, Token: "tok", Text: "work"}, now)
	_ = led.Close()

	// Tear the last record the way a power cut does: keep a prefix, no newline.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lastNL := bytes.LastIndexByte(raw[:len(raw)-1], '\n')
	torn := append([]byte(nil), raw[:lastNL+1]...)
	fragment := raw[lastNL+1 : len(raw)-30]
	if len(fragment) == 0 {
		t.Fatal("precondition: the fragment should not be empty")
	}
	if err := os.WriteFile(path, append(torn, fragment...), 0o600); err != nil {
		t.Fatal(err)
	}

	box, err := LoadOrCreateKey(filepath.Join(filepath.Dir(path), "key"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	var gotBytes int
	var gotOffset int64
	reopened.OnTornTail = func(b int, at int64) { gotBytes, gotOffset = b, at }

	st2 := core.NewState("test", core.DefaultLimits())
	n, err := reopened.Replay(st2)
	if err != nil {
		t.Fatalf("a torn tail should be survivable, not fatal: %v", err)
	}
	if n != 2 {
		t.Fatalf("replayed %d ops, want 2 (the torn third is discarded)", n)
	}
	if gotBytes == 0 {
		t.Fatal("the torn tail was discarded and the file truncated with no report at all")
	}
	if gotBytes != len(fragment) {
		t.Errorf("reported %d discarded bytes, want %d", gotBytes, len(fragment))
	}
	if gotOffset != int64(lastNL+1) {
		t.Errorf("reported offset %d, want %d", gotOffset, lastNL+1)
	}
}

// The ledger is a file on disk: it can be truncated by a full disk, edited by
// somebody curious, or replaced by something else entirely. None of that may
// produce a panic, because a stack trace is the one output that tells an
// operator nothing and buries the diagnosis the daemon prints just above it.
//
// Found while testing an unrelated message: a hand-written line that was valid
// JSON but had no `op` object reached DecryptOp as nil and took the process
// down.
func TestALineWithNoOpIsCorruptionNotAPanic(t *testing.T) {
	dir := t.TempDir()
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	if err := os.WriteFile(path, []byte(`{"s":1,"prev":"","t":"2026-08-11T00:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, "t", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	st := core.NewState("t", core.DefaultLimits())
	_, err = l.Replay(st) // must not panic
	if err == nil {
		t.Fatal("a record with no op was accepted as replayable")
	}
	if !strings.Contains(err.Error(), "no op") {
		t.Errorf("the error does not say what is wrong with the line: %v", err)
	}
}
