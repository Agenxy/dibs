package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

func reg(t *testing.T, s *State, name, token string, now time.Time) *Lane {
	t.Helper()
	res, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: name, NewToken: token}, now)
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return s.Lanes[res["lane_id"].(string)]
}

//nolint:unparam // returning the Lane keeps the helper usable from new tests
func regPersistent(t *testing.T, s *State, name, token, nonce string, now time.Time) *Lane {
	t.Helper()
	res, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: name, NewToken: token,
		Nonce: nonce, LaneKind: KindPersistent,
	}, now)
	if err != nil {
		t.Fatalf("register persistent %s: %v", name, err)
	}
	return s.Lanes[res["lane_id"].(string)]
}

func mustApply(t *testing.T, s *State, op *Op, now time.Time) Result {
	t.Helper()
	res, _, err := s.Apply(op, now)
	if err != nil {
		t.Fatalf("%s: %v", op.Kind, err)
	}
	return res
}

func TestAwarenessGatePerActivation(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "alpha", "tokA", t0)

	if _, _, err := s.Apply(&Op{Kind: OpSetSlot, Token: "tokA", Text: "w"}, t0); !errors.Is(err, ErrMustAck) {
		t.Fatalf("slot before ack: got %v", err)
	}
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tokA"}, t0)
	mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tokA", Text: "w"}, t0)

	// Gate re-arms on the stale transition (SPEC §6).
	mustApply(t, s, &Op{Kind: OpSweep, StaleLanes: []string{"alpha"}}, t0.Add(10*time.Minute))
	mustApply(t, s, &Op{Kind: OpWakeLane, Token: "tokA"}, t0.Add(11*time.Minute))
	if _, _, err := s.Apply(&Op{Kind: OpSetSlot, Token: "tokA", Text: "w2"}, t0.Add(11*time.Minute)); !errors.Is(err, ErrMustAck) {
		t.Fatalf("gate must re-arm per activation: got %v", err)
	}
}

func TestWakeIsLedgeredTransition(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	mustApply(t, s, &Op{Kind: OpSweep, StaleLanes: []string{"a"}}, t0.Add(6*time.Minute))
	if s.Lanes["a"].Status != StatusStale {
		t.Fatal("setup: lane should be stale")
	}
	before := s.Serial
	_, evs, err := s.Apply(&Op{Kind: OpWakeLane, Token: "ta"}, t0.Add(7*time.Minute))
	if err != nil || len(evs) != 1 || evs[0].Type != "lane.recovered" {
		t.Fatalf("wake: evs=%v err=%v", evs, err)
	}
	if s.Serial != before+1 {
		t.Fatal("wake must consume exactly one serial")
	}
	// Idempotent wake on an active lane: unchanged, no serial.
	before = s.Serial
	_, evs, _ = s.Apply(&Op{Kind: OpWakeLane, Token: "ta"}, t0.Add(8*time.Minute))
	if evs != nil || s.Serial != before {
		t.Fatal("wake on active lane must be a serial-free no-op")
	}
}

func TestPersistentLifecycleAndResume(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	regPersistent(t, s, "reviewer", "tok1", "nonce-secret-1", t0)

	// Persistent registration without nonce is refused.
	if _, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "x", NewToken: "tx", LaneKind: KindPersistent}, t0); err == nil {
		t.Fatal("persistent without nonce must fail")
	}

	// Lease lapse → dormant (not stale), mailbox retained, claims released.
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok1"}, t0)
	mustApply(t, s, &Op{Kind: OpClaim, Token: "tok1", Path: "/w", Mode: ClaimExclusive}, t0)
	mustApply(t, s, &Op{Kind: OpSweep, StaleLanes: []string{"reviewer"}}, t0.Add(10*time.Minute))
	l := s.Lanes["reviewer"]
	if l.Status != StatusDormant || len(s.Claims) != 0 || l.AckedSerial != 0 {
		t.Fatalf("dormant transition wrong: status=%s claims=%d acked=%d", l.Status, len(s.Claims), l.AckedSerial)
	}

	// resume_lane: rotation + generation + rebind, atomic.
	res := mustApply(t, s, &Op{
		Kind: OpResumeLane, Nonce: "nonce-secret-1", ResumeID: "r1",
		NewToken: "tok2", PID: 4242,
	}, t0.Add(24*time.Hour))
	if res["token"] != "tok2" || res["activation"].(uint64) != 1 {
		t.Fatalf("resume result: %v", res)
	}
	if s.LaneByToken("tok1") != nil {
		t.Fatal("old token must be invalid after rotation")
	}
	if l.Status != StatusActive || l.PID != 4242 {
		t.Fatal("resume must wake and rebind")
	}

	// Idempotent retry with same resume_id → same token (generation unchanged).
	res = mustApply(t, s, &Op{
		Kind: OpResumeLane, Nonce: "nonce-secret-1", ResumeID: "r1",
		NewToken: "tok3-ignored",
	}, t0.Add(24*time.Hour+time.Minute))
	if res["token"] != "tok2" || res["resumed"] != true {
		t.Fatalf("resume retry must return original token: %v", res)
	}

	// A later resume supersedes: old resume_id retry yields no token.
	mustApply(t, s, &Op{Kind: OpResumeLane, Nonce: "nonce-secret-1", ResumeID: "r2", NewToken: "tok4"}, t0.Add(25*time.Hour))
	res = mustApply(t, s, &Op{Kind: OpResumeLane, Nonce: "nonce-secret-1", ResumeID: "r1", NewToken: "ignored"}, t0.Add(25*time.Hour+time.Minute))
	if res["superseded"] != true {
		t.Fatalf("stale resume retry must be superseded: %v", res)
	}
	if _, has := res["token"]; has {
		t.Fatal("superseded retry must never return a token")
	}

	// Dormancy max runs from the ledgered transition → archived.
	mustApply(t, s, &Op{Kind: OpSweep, StaleLanes: []string{"reviewer"}}, t0.Add(26*time.Hour))
	mustApply(t, s, &Op{Kind: OpSweep}, t0.Add(26*time.Hour).Add(DefaultLimits().DormancyMax+time.Hour))
	if s.Lanes["reviewer"].Status != StatusArchived {
		t.Fatalf("dormancy max must archive, got %s", s.Lanes["reviewer"].Status)
	}
}

func TestTerminalPredicateAndConsumption(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)

	// Notify: ack is terminal + consumed (SPEC §8).
	res := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "fyi"}, t0)
	nser := res["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpAckMessage, Token: "tb", MsgSerial: nser}, t0)
	m := s.Messages[nser]
	if !m.Terminal() || !m.Consumed {
		t.Fatalf("acked notify must be terminal+consumed: %+v", m)
	}

	// Question: acked is non-terminal; respond is terminal + consumed.
	res = mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgQuestion, Body: "q"}, t0)
	qser := res["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpAckMessage, Token: "tb", MsgSerial: qser}, t0)
	if s.Messages[qser].Terminal() {
		t.Fatal("acked question must be non-terminal")
	}
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tb", MsgSerial: qser, Disposition: "answer", Body: "ans"}, t0)
	if !s.Messages[qser].Terminal() || !s.Messages[qser].Consumed {
		t.Fatal("answered question must be terminal+consumed")
	}

	// ack_message on terminal mail = consumption transition.
	res = mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgQuestion, Body: "q2",
		DeadlineSec: 60,
	}, t0)
	q2 := res["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpHeartbeat, Token: "tb"}, t0.Add(90*time.Second))
	mustApply(t, s, &Op{Kind: OpSweep}, t0.Add(2*time.Minute)) // expires q2 (recipient active)
	if got := s.Messages[q2].State; got != MsgStateExpiredSilent {
		t.Fatalf("q2 state = %s", got)
	}
	if s.Messages[q2].Consumed {
		t.Fatal("expired message not yet consumed")
	}
	_, evs, err := s.Apply(&Op{Kind: OpAckMessage, Token: "tb", MsgSerial: q2}, t0.Add(3*time.Minute))
	if err != nil || len(evs) != 1 || evs[0].Type != "message.consumed" {
		t.Fatalf("terminal ack must be consumption: evs=%v err=%v", evs, err)
	}
	if !s.Messages[q2].Consumed {
		t.Fatal("consumption flag not set")
	}
	// GC keeps consumed terminal mail through the sender's read window
	// (real-agent finding: respond+GC raced the sender's get_message)…
	mustApply(t, s, &Op{Kind: OpSweep}, t0.Add(4*time.Minute))
	if _, exists := s.Messages[q2]; !exists {
		t.Fatal("consumed terminal message must survive the consumed-retention window")
	}
	// …and removes it after the window.
	mustApply(t, s, &Op{Kind: OpSweep}, t0.Add(4*time.Minute).Add(DefaultLimits().ConsumedRetention+time.Minute))
	if _, exists := s.Messages[q2]; exists {
		t.Fatal("consumed terminal message must be GC'd after the retention window")
	}
}

func TestClaimMatrixV1(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "ta"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tb"}, t0)

	// SPEC §9 matrix: exclusive refused on ANY overlap.
	res := mustApply(t, s, &Op{Kind: OpClaim, Token: "ta", Path: "/repo/src", Mode: ClaimShared}, t0)
	if res["granted"] != true {
		t.Fatal("first shared must be granted")
	}
	res = mustApply(t, s, &Op{Kind: OpClaim, Token: "tb", Path: "/repo/src", Mode: ClaimExclusive}, t0)
	if res["granted"] != false {
		t.Fatal("exclusive over existing shared must be refused")
	}
	res = mustApply(t, s, &Op{Kind: OpClaim, Token: "tb", Path: "/repo/src/pkg", Mode: ClaimShared}, t0)
	if res["granted"] != true {
		t.Fatal("shared/shared overlap must be granted")
	}
	// Component-wise: /repo/src2 does not overlap /repo/src.
	res = mustApply(t, s, &Op{Kind: OpClaim, Token: "tb", Path: "/repo/src2", Mode: ClaimExclusive}, t0)
	if res["granted"] != true {
		t.Fatal("/repo/src2 must not overlap /repo/src")
	}
	// Shared under exclusive refused.
	res = mustApply(t, s, &Op{Kind: OpClaim, Token: "ta", Path: "/repo/src2/sub", Mode: ClaimShared}, t0)
	if res["granted"] != false {
		t.Fatal("shared under exclusive must be refused")
	}
}

func TestSendDedupAndConflict(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)

	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify,
		Body: "hello", OpID: "op-1",
	}, t0)
	first := res["msg_serial"].(uint64)
	before := s.Serial

	// Same op_id + same payload → original serial, no new state.
	res = mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify,
		Body: "hello", OpID: "op-1",
	}, t0.Add(time.Second))
	if res["msg_serial"].(uint64) != first || res["deduplicated"] != true || s.Serial != before {
		t.Fatalf("dedup retry failed: %v serial=%d", res, s.Serial)
	}

	// Same op_id + different payload → E_OP_ID_CONFLICT.
	_, _, err := s.Apply(&Op{
		Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify,
		Body: "DIFFERENT", OpID: "op-1",
	}, t0.Add(2*time.Second))
	var ce *Error
	if !asErr(err, &ce) || ce.Code != "E_OP_ID_CONFLICT" {
		t.Fatalf("want E_OP_ID_CONFLICT, got %v", err)
	}
}

func TestMailboxDisplacementAndWatermark(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxMailboxDepth = 2
	lim.TerminalRetention = 1
	s := NewState("n1", lim)
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)

	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "n1"}, t0)
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "n2"}, t0)
	// At capacity: a request is rejected; a notify displaces the oldest notify.
	if _, _, err := s.Apply(&Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgRequest, Body: "r"}, t0); !errors.Is(err, ErrMailboxFull) {
		t.Fatalf("request at capacity: %v", err)
	}
	_, evs, err := s.Apply(&Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "n3"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	foundDisplaced := false
	for _, ev := range evs {
		if ev.Type == "message.displaced" {
			foundDisplaced = true
		}
	}
	if !foundDisplaced {
		t.Fatal("displacement must emit message.displaced in the same op")
	}

	// Displaced (terminal, unconsumed) + retention 1 → second displacement
	// evicts under the watermark.
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "n4"}, t0)
	mustApply(t, s, &Op{Kind: OpSweep}, t0.Add(time.Second))
	if s.Lanes["b"].TruncatedBefore == 0 {
		t.Fatal("watermark must advance when unconsumed terminal mail is evicted")
	}
}

func TestExpiryDiagnosisTriad(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)
	regPersistent(t, s, "p", "tp", "np", t0)

	send := func(to string) uint64 {
		res := mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "ta", To: to, MsgType: MsgQuestion,
			Body: "q", DeadlineSec: 60,
		}, t0)
		return res["msg_serial"].(uint64)
	}
	qActive, qDead, qDormant := send("b"), send("b"), send("p")

	// b answers one, stays active; then we let the other expire while b is
	// active → expired_unanswered.
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tb", MsgSerial: qActive, Disposition: "decline"}, t0.Add(30*time.Second))
	later := t0.Add(2 * time.Minute)
	mustApply(t, s, &Op{Kind: OpHeartbeat, Token: "tb"}, later.Add(-time.Second))
	mustApply(t, s, &Op{Kind: OpSweep, StaleLanes: []string{"p"}}, later)
	if got := s.Messages[qDead].State; got != MsgStateExpiredSilent {
		t.Fatalf("active recipient expiry = %s", got)
	}
	if got := s.Messages[qDormant].State; got != MsgStateExpiredDormant {
		t.Fatalf("dormant recipient expiry = %s", got)
	}
	if !strings.Contains(s.Messages[qDormant].ExpireDetail, "wake") {
		t.Fatal("dormant expiry detail must mention wake")
	}
}

func TestNonceRetryWindowAndInUse(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	res, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "a", NewToken: "t1", Nonce: "nx"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	// Response-loss retry within one TTL: original result.
	res2, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "a", NewToken: "t2-ignored", Nonce: "nx"}, t0.Add(time.Minute))
	if err != nil || res2["token"] != "t1" || res2["resumed"] != true {
		t.Fatalf("retry within window: %v %v", res2, err)
	}
	if res2["lane_id"] != res["lane_id"] {
		t.Fatal("retry must return the same lane")
	}
	// Outside the window, same name: the nonce is the recovery credential, so
	// this is the agent coming back: reattach with a rotated token rather than
	// refusing. Refusing here is what stranded four lanes' mail on a live fleet:
	// the agent had kept its nonce exactly as advised and was told, by its own
	// credential, that it was somebody else.
	res3, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "a", NewToken: "t3", Nonce: "nx"}, t0.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("outside the window the nonce must recover the lane: %v", err)
	}
	if res3["lane_id"] != res["lane_id"] || res3["reattached"] != true || res3["token"] != "t3" {
		t.Fatalf("want reattach to the same lane with a rotated token, got %v", res3)
	}
	// A DIFFERENT name is still refused: a nonce recovers one identity.
	_, _, err = s.Apply(&Op{Kind: OpRegisterLane, Name: "b", NewToken: "t4", Nonce: "nx"}, t0.Add(20*time.Minute))
	var ce *Error
	if !asErr(err, &ce) || ce.Code != "E_NONCE_IN_USE" {
		t.Fatalf("want E_NONCE_IN_USE for a second identity, got %v", err)
	}
}

func TestAckBoardAtomicCheckpoint(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "a", "ta", t0)
	reg(t, s, "b", "tb", t0)
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "m"}, t0)

	res, evs, err := s.Apply(&Op{Kind: OpAckBoard, Token: "tb"}, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// Post-state: returned inbox shows delivered; delivery event shares the
	// ack_board serial; returned serial is the op's own.
	inbox := res["inbox"].([]*Message)
	if len(inbox) != 1 || inbox[0].State != MsgStateDelivered {
		t.Fatalf("checkpoint inbox must be post-state: %+v", inbox)
	}
	serial := res["serial"].(uint64)
	if serial != s.Serial {
		t.Fatal("checkpoint serial must be the op's own")
	}
	hasDelivery := false
	for _, ev := range evs {
		if ev.Type == "message.delivered" && ev.Serial == serial {
			hasDelivery = true
		}
	}
	if !hasDelivery {
		t.Fatal("delivery must be an effect of the ack_board op at its serial")
	}
	if res["truncated_before_serial"].(uint64) != 0 {
		t.Fatal("fresh lane watermark must be 0")
	}
}

func TestTokenPrivacyEverywhere(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	regPersistent(t, s, "a", "secret-token-val", "secret-nonce-val", t0)
	b, _ := json.Marshal(s.Board())
	for _, secret := range []string{"secret-token-val", "secret-nonce-val"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("%q leaked into public board", secret)
		}
	}
}

func asErr(err error, target **Error) bool {
	var e *Error
	if errors.As(err, &e) {
		*target = e
		return true
	}
	return false
}

// TestRedundantObjectiveIsCaught replays the real fleet failure. Two agents
// independently pursued ONE objective ("green main / typos gate") and produced
// three overlapping PRs. ~3,900 diff lines, ~1,200 wasted. The waste was
// redundant EFFORT, not a file conflict; the files only incidentally overlapped.
// Detection keys on the shared objective ref.
func TestRedundantObjectiveIsCaught(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "lane-a", "t3d", t0)
	reg(t, s, "lane-b", "tdoc", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "t3d"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tdoc"}, t0)

	mustApply(t, s, &Op{
		Kind: OpSetSlot, Token: "t3d",
		Text: "typos root-cause fix (vendored excludes)",
		Refs: []string{"gate:typos", "PR #101"},
		Dirs: []string{"/repo/_typos.toml"},
	}, t0)

	// Different phrasing, different files: same objective. Must still fire.
	res := mustApply(t, s, &Op{
		Kind: OpSetSlot, Token: "tdoc",
		Text: "restore green main",
		Refs: []string{"gate:typos"},
		Dirs: []string{"/repo/docs"},
	}, t0.Add(time.Minute))

	ov, ok := res["overlaps"].([]SlotOverlap)
	if !ok || len(ov) == 0 || !ov[0].Strong() {
		t.Fatalf("redundant objective NOT caught: %#v", res)
	}
	if ov[0].Lane != "lane-a" || ov[0].Signal != SignalSameObjective {
		t.Fatalf("overlap should name the incumbent + the objective: %+v", ov[0])
	}
	if _, warned := res["warning"]; !warned {
		t.Fatal("a same-objective overlap must warn explicitly")
	}
	// Advisory, never coercive: #103's lane was legitimately complementary.
	if s.Lanes["lane-b"].Slots["s1"].Text == "" {
		t.Fatal("declaring must still succeed. Lanes informs, never blocks")
	}
}

// TestConcurrentFileWorkIsNotAnAlarm is the counterweight, and it matters as
// much as the test above: two agents editing the same file is NORMAL. Version
// control solved that. Lanes must report it as awareness, never as duplication
// a false alarm here would push agents to serialize and destroy parallelism.
func TestConcurrentFileWorkIsNotAnAlarm(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "alpha", "ta", t0)
	reg(t, s, "beta", "tb", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "ta"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tb"}, t0)

	mustApply(t, s, &Op{
		Kind: OpSetSlot, Token: "ta", Text: "add auth endpoints",
		Refs: []string{"issue:900"}, Dirs: []string{"/repo/api"},
	}, t0)
	res := mustApply(t, s, &Op{
		Kind: OpSetSlot, Token: "tb", Text: "add rate limiting",
		Refs: []string{"issue:901"}, Dirs: []string{"/repo/api"},
	}, t0)

	if _, warned := res["warning"]; warned {
		t.Fatal("same files + different objectives must NOT warn: that is healthy parallelism")
	}
	ov, _ := res["overlaps"].([]SlotOverlap)
	if len(ov) == 0 || ov[0].Signal != SignalSamePaths {
		t.Fatalf("should still surface awareness of concurrent work: %#v", res)
	}
	if _, noted := res["note"]; !noted {
		t.Fatal("path overlap should be reported as awareness")
	}
}
