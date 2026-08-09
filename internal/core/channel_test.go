package core

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A fixed clock: replay must reproduce timestamps, so nothing here may read
// the wall clock.
var testNow = time.Unix(1700000000, 0)

// ch builds a state with n registered, acked agents ready to use channels.
func chState(t *testing.T, names ...string) (*State, map[string]*Lane) {
	t.Helper()
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	agents := map[string]*Lane{}
	for _, n := range names {
		res, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: n, NewToken: "tok-" + n}, now)
		if err != nil {
			t.Fatalf("register %s: %v", n, err)
		}
		id, _ := res["lane_id"].(string)
		l := s.Lanes[id]
		if l == nil {
			t.Fatalf("no lane for %s (%v)", n, res)
		}
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: l.Token}, now); err != nil {
			t.Fatalf("ack %s: %v", n, err)
		}
		agents[n] = l
	}
	return s, agents
}

// spawnChild registers a subagent the way a real one must: the parent vouches
// with a one-time secret, and the child presents it. Parent alone is a claim
// anybody can make — it grants nothing without this.
func spawnChild(t *testing.T, s *State, parentTok, parentID, nonce string) Result {
	t.Helper()
	do(t, s, &Op{Kind: OpVouchChild, Token: parentTok, Nonce: nonce})
	return do(t, s, &Op{
		Kind: OpRegisterLane, Name: "helper", NewToken: "tok-helper",
		Parent: parentID, ParentNonce: nonce,
	})
}

func do(t *testing.T, s *State, op *Op) Result {
	t.Helper()
	res, _, err := s.Apply(op, testNow)
	if err != nil {
		t.Fatalf("%s: %v", op.Kind, err)
	}
	return res
}

func mustFail(t *testing.T, s *State, op *Op) error {
	t.Helper()
	_, _, err := s.Apply(op, testNow)
	if err == nil {
		t.Fatalf("%s: expected an error", op.Kind)
	}
	return err
}

// ── the replay contract (SPEC-CHANNELS.md §4.3) ──────────────────────────

// The single most important property in this subsystem: Apply must treat the
// recorded score as fact. If it ever recomputes one, replaying a ledger against
// a reindexed repository reconstructs different membership and the hash chain
// stops meaning anything.
func TestJoinRecordsTheScoreExactlyAsGiven(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth-refactor", Text: "reworking auth"})
	do(t, s, &Op{
		Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "auth-refactor",
		Score: 0.8137, Threshold: 0.327, ScorerID: "lexical+cochange", ScorerVersion: "1",
		Evidence: []string{"internal/mcp/identity.go"}, Auto: true,
	})

	m := s.Channels["auth-refactor"].Members["beta"]
	if m == nil {
		t.Fatal("beta should be a member")
	}
	if m.Score != 0.8137 || m.Threshold != 0.327 {
		t.Fatalf("score/threshold must be stored verbatim, got %v/%v", m.Score, m.Threshold)
	}
	if m.ScorerID != "lexical+cochange" || m.ScorerVersion != "1" {
		t.Fatalf("scorer provenance lost: %+v", m)
	}
	if !m.Auto || len(m.Evidence) != 1 {
		t.Fatalf("evidence and auto flag must survive: %+v", m)
	}
}

// Replaying the same ops must rebuild byte-identical membership, on a machine
// with no repository and no scorer present at all.
func TestChannelStateIsReplayable(t *testing.T) {
	build := func() *State {
		s, a := chState(t, "alpha", "beta", "gamma")
		do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth", Text: "auth"})
		do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "auth", Score: 0.7, ScorerID: "x"})
		do(t, s, &Op{Kind: OpLaneJoin, Token: a["gamma"].Token, Channel: "auth", Score: 0.4, ScorerID: "x"})
		do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["alpha"].Token, Channel: "auth", Body: "renaming the token field"})
		return s
	}
	a, b := build(), build()
	ca, cb := a.Channels["auth"], b.Channels["auth"]
	if len(ca.Members) != len(cb.Members) {
		t.Fatalf("member counts diverged: %d vs %d", len(ca.Members), len(cb.Members))
	}
	for id, m := range ca.Members {
		other := cb.Members[id]
		if other == nil || other.Score != m.Score || other.JoinedSerial != m.JoinedSerial {
			t.Fatalf("membership for %s diverged: %+v vs %+v", id, m, other)
		}
	}
	if a.Serial != b.Serial {
		t.Fatalf("serial diverged: %d vs %d", a.Serial, b.Serial)
	}
}

// ── exclusivity and the queue ────────────────────────────────────────────

func TestExclusiveLaneQueuesRatherThanRefuses(t *testing.T) {
	s, a := chState(t, "owner", "second", "third")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "hot work", Exclusive: true})

	r := do(t, s, &Op{Kind: OpLaneJoin, Token: a["second"].Token, Channel: "hot", Score: 0.9})
	if r["joined"] != false || r["queued"] != true {
		t.Fatalf("second must be queued, got %v", r)
	}
	if r["queue_position"] != 1 {
		t.Fatalf("want position 1, got %v", r)
	}
	r = do(t, s, &Op{Kind: OpLaneJoin, Token: a["third"].Token, Channel: "hot", Score: 0.9})
	if r["queue_position"] != 2 {
		t.Fatalf("want position 2, got %v", r)
	}
	// Re-asking must not queue you twice.
	r = do(t, s, &Op{Kind: OpLaneJoin, Token: a["second"].Token, Channel: "hot", Score: 0.9})
	if r["queue_position"] != 1 {
		t.Fatalf("re-joining must report the existing position, got %v", r)
	}
	if got := len(s.Channels["hot"].Queue); got != 2 {
		t.Fatalf("queue must hold 2, got %d", got)
	}
}

func TestReleasingExclusivityAdmitsEveryoneWaiting(t *testing.T) {
	s, a := chState(t, "owner", "second", "third")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "t", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["second"].Token, Channel: "hot"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["third"].Token, Channel: "hot"})

	do(t, s, &Op{Kind: OpLaneExclusive, Token: a["owner"].Token, Channel: "hot", Mode: "release"})
	ch := s.Channels["hot"]
	if ch.Owner != "" {
		t.Fatalf("owner should be cleared, got %q", ch.Owner)
	}
	if len(ch.Members) != 3 {
		t.Fatalf("all three should be members, got %d", len(ch.Members))
	}
	if len(ch.Queue) != 0 {
		t.Fatalf("queue should be drained, got %v", ch.Queue)
	}
}

// The owner leaving must hand the lane to the next waiter, not strand it.
func TestOwnerLeavingPromotesTheHeadOfTheQueue(t *testing.T) {
	s, a := chState(t, "owner", "second")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "t", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["second"].Token, Channel: "hot"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["owner"].Token, Channel: "hot"})

	ch := s.Channels["hot"]
	if ch.Owner != "second" {
		t.Fatalf("second should have been promoted, owner=%q", ch.Owner)
	}
	if _, ok := ch.Members["second"]; !ok {
		t.Fatal("the promoted agent must also be a member")
	}
	if _, ok := ch.Members["owner"]; ok {
		t.Fatal("the departed owner must not remain a member")
	}
}

// A fleet must never stay wedged behind an agent that crashed. This is the
// channel analogue of claim expiry and runs at the same moment.
func TestStaleOwnerYieldsExclusivityButKeepsMembership(t *testing.T) {
	s, a := chState(t, "owner", "second")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "t", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["second"].Token, Channel: "hot"})

	now := time.Unix(1700000000, 0)
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"owner"}}, now); err != nil {
		t.Fatal(err)
	}
	ch := s.Channels["hot"]
	if ch.Owner == "owner" {
		t.Fatal("a stale agent must not keep the lane locked")
	}
	// Membership is informative and costs nobody anything; a persistent agent
	// that wakes should still be where it was working.
	if _, ok := ch.Members["owner"]; !ok {
		t.Fatal("going stale should not evict the agent from the lane")
	}
	if _, ok := ch.Members["second"]; !ok {
		t.Fatal("the waiter should have been admitted when the lock lifted")
	}
}

func TestOnlyTheFirstMemberMayTakeALaneExclusively(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "shared", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "shared"})
	err := mustFail(t, s, &Op{Kind: OpLaneExclusive, Token: a["beta"].Token, Channel: "shared"})
	if !contains(err.Error(), "members") {
		t.Fatalf("error should explain the lane is already shared: %v", err)
	}
}

func TestNonOwnerCannotReleaseSomeoneElsesLane(t *testing.T) {
	s, a := chState(t, "owner", "other")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "t", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["other"].Token, Channel: "hot"}) // queued
	_ = mustFail(t, s, &Op{Kind: OpLaneExclusive, Token: a["other"].Token, Channel: "hot", Mode: "release"})
}

// ── announce and ack ─────────────────────────────────────────────────────

func TestAnnounceRequiresAckFromEveryMemberButTheSender(t *testing.T) {
	s, a := chState(t, "alpha", "beta", "gamma")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "auth"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["gamma"].Token, Channel: "auth"})

	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["alpha"].Token, Channel: "auth", Body: "renaming Token"})
	if r["must_ack"] != 2 {
		t.Fatalf("two others must ack, got %v", r)
	}
	serial := r["serial"].(uint64)

	if got := len(s.Unacked("beta")); got != 1 {
		t.Fatalf("beta owes one ack, got %d", got)
	}
	if got := len(s.Unacked("alpha")); got != 0 {
		t.Fatalf("the sender owes nothing, got %d", got)
	}

	r = do(t, s, &Op{Kind: OpLaneAck, Token: a["beta"].Token, MsgSerial: serial})
	if r["outstanding"] != 1 {
		t.Fatalf("one still outstanding, got %v", r)
	}
	if s.Announcements[serial].State != AnnounceOpen {
		t.Fatal("still open until everyone acks")
	}
	r = do(t, s, &Op{Kind: OpLaneAck, Token: a["gamma"].Token, MsgSerial: serial})
	if r["outstanding"] != 0 {
		t.Fatalf("none outstanding, got %v", r)
	}
	if s.Announcements[serial].State != AnnounceAcked {
		t.Fatal("should be settled once everyone acked")
	}
}

// An announcement into an empty room is already settled — otherwise it parks in
// the unresolved column forever and trains people to ignore that column.
func TestAnnounceWithNoOtherMembersIsSettledImmediately(t *testing.T) {
	s, a := chState(t, "alpha")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "solo", Text: "t"})
	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["alpha"].Token, Channel: "solo", Body: "note"})
	if s.Announcements[r["serial"].(uint64)].State != AnnounceAcked {
		t.Fatal("an announcement nobody must ack is already settled")
	}
}

// Waiting on the dead is how an unresolved column fills with things nobody can
// act on — so a departing member's requirement is dropped and the announcement
// stops being open. But it must not then claim to have been READ.
//
// Dropping the requirement silently settled it as `acked`, and the extreme case
// shows why that is indefensible: an announcement with acked=[] — nobody at all
// — recorded as acknowledged, and invisible on the board. A sender that checks
// later is told its freeze notice landed when zero agents saw it.
//
// Settling is right; the terminal state has to be the honest one. `unacked`
// already means "this was never acknowledged, and a person should look", which
// is exactly the situation.
func TestClosingAnAgentSettlesTheAckItOwedWithoutClaimingItRead(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "auth"})
	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["alpha"].Token, Channel: "auth", Body: "x"})
	serial := r["serial"].(uint64)

	do(t, s, &Op{Kind: OpCloseLane, Token: a["beta"].Token})
	an := s.Announcements[serial]
	if an.State == AnnounceOpen {
		t.Fatal("it must not wait on an agent that is never coming back")
	}
	if an.State == AnnounceAcked {
		t.Fatal("nobody acknowledged it; recording it as acked tells the sender a falsehood")
	}
	if !slices.Contains(an.DepartedUnacked, "beta") {
		t.Fatalf("the member that left owing it must be named, got %v", an.DepartedUnacked)
	}
	if _, ok := s.Channels["auth"].Members["beta"]; ok {
		t.Fatal("a closed agent must leave its lanes")
	}
}

// The common case is NOT the extreme one, and must not cry wolf: if the
// announcement did reach the lane — somebody read it — a later departure
// settles it as acked, with the departure recorded rather than alarmed about.
// Filling the board with red marks after every prune is how people learn to
// ignore the column that matters.
func TestADepartureAfterSomebodyReadItDoesNotRaiseAnAlarm(t *testing.T) {
	s, a := chState(t, "sender", "reader", "quitter")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["sender"].Token, Channel: "work", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["reader"].Token, Channel: "work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["quitter"].Token, Channel: "work"})
	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["sender"].Token, Channel: "work", Body: "FREEZE"})
	serial := r["serial"].(uint64)

	do(t, s, &Op{Kind: OpLaneAck, Token: a["reader"].Token, MsgSerial: serial})
	do(t, s, &Op{Kind: OpPruneLane, To: "quitter"})

	an := s.Announcements[serial]
	if an.State != AnnounceAcked {
		t.Fatalf("somebody did read it; want acked, got %q", an.State)
	}
	if !slices.Contains(an.DepartedUnacked, "quitter") {
		t.Fatalf("but the member that never read it is still recorded, got %v", an.DepartedUnacked)
	}
	if _, _, blocked := s.unackedIn("work"); blocked != 0 {
		t.Fatalf("and it is not left looking blocked, got %d", blocked)
	}
}

func TestOnlyMembersMaySpeak(t *testing.T) {
	s, a := chState(t, "alpha", "outsider")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth", Text: "t"})
	_ = mustFail(t, s, &Op{Kind: OpLanePost, Token: a["outsider"].Token, Channel: "auth", Body: "hi"})
	_ = mustFail(t, s, &Op{Kind: OpLaneAnnounce, Token: a["outsider"].Token, Channel: "auth", Body: "hi"})

	// …but subscribing is free, and does not make you a member.
	do(t, s, &Op{Kind: OpLaneSubscribe, Token: a["outsider"].Token, Channel: "auth"})
	if !s.Channels["auth"].Subs["outsider"] {
		t.Fatal("should be subscribed")
	}
	if _, ok := s.Channels["auth"].Members["outsider"]; ok {
		t.Fatal("subscribing must not create membership — only membership collides")
	}
	_ = mustFail(t, s, &Op{Kind: OpLanePost, Token: a["outsider"].Token, Channel: "auth", Body: "hi"})
}

// ── ids and gating ───────────────────────────────────────────────────────

func TestChannelIDsAreNormalisedSoOneTopicIsOneLane(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Auth Refactor", "auth-refactor"},
		{"auth-refactor", "auth-refactor"},
		{"  Auth   Refactor  ", "auth-refactor"},
		{"auth/refactor", "auth-refactor"},
		{"AUTH_REFACTOR", "auth-refactor"},
	} {
		if got := cleanID(tc.in); got != tc.want {
			t.Errorf("cleanID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestChannelOpsRespectTheAwarenessGate(t *testing.T) {
	// SPEC §6: no declaring work before acknowledging what others are doing.
	s := NewState("test", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "solo", NewToken: "tok"}, now)
	if err != nil {
		t.Fatal(err)
	}
	l := s.Lanes[res["lane_id"].(string)]
	_ = mustFail(t, s, &Op{Kind: OpLaneOpen, Token: l.Token, Channel: "x", Text: "t"})
}

// SPEC-CHANNELS.md §10.6: silence is never resolution.
//
// When redelivery gives up, the announcement must NOT be deleted and must NOT
// be treated as settled. It becomes `unacked` and stays on the board naming who
// never answered — "three agents ignored this" is precisely what a human needs
// to see, and dropping it would make the board look calm while the fleet was
// uncoordinated.
func TestExhaustedAnnouncementIsMarkedUnackedNotDropped(t *testing.T) {
	s, a := chState(t, "alpha", "beta", "gamma")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "auth"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["gamma"].Token, Channel: "auth"})
	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["alpha"].Token, Channel: "auth", Body: "x"})
	serial := r["serial"].(uint64)

	// gamma answers; beta never does.
	do(t, s, &Op{Kind: OpLaneAck, Token: a["gamma"].Token, MsgSerial: serial})

	_, evs, err := s.Apply(&Op{Kind: OpSweep, GiveUpAnnounce: []uint64{serial}}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	an := s.Announcements[serial]
	if an == nil {
		t.Fatal("the announcement must survive — dropping it hides the failure")
	}
	if an.State != AnnounceUnacked {
		t.Fatalf("state = %q, want %q", an.State, AnnounceUnacked)
	}
	var found bool
	for _, e := range evs {
		if e.Type != "lane.announce_unacked" {
			continue
		}
		found = true
		silent, _ := e.Data["silent"].([]string)
		if len(silent) != 1 || silent[0] != "beta" {
			t.Fatalf("the event must name who stayed silent, got %v", silent)
		}
		if d, _ := e.Data["detail"].(string); !strings.Contains(d, "not agreement") {
			t.Fatalf("the event must say this is loss of coordination, got %q", d)
		}
	}
	if !found {
		t.Fatal("giving up must be announced, not silent")
	}
	// And it must stop being redelivered.
	if n := len(s.Unacked("beta")); n != 0 {
		t.Fatalf("an abandoned announcement must stop nagging, still owed %d", n)
	}
}

func TestGivingUpOnAnAlreadySettledAnnouncementIsANoop(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "auth"})
	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["alpha"].Token, Channel: "auth", Body: "x"})
	serial := r["serial"].(uint64)
	do(t, s, &Op{Kind: OpLaneAck, Token: a["beta"].Token, MsgSerial: serial})

	if _, _, err := s.Apply(&Op{Kind: OpSweep, GiveUpAnnounce: []uint64{serial}}, testNow); err != nil {
		t.Fatal(err)
	}
	if got := s.Announcements[serial].State; got != AnnounceAcked {
		t.Fatalf("an acknowledged announcement must stay acknowledged, got %q", got)
	}
}

// SPEC-CHANNELS.md §8.2: spawning a subagent must not require ceremony.
//
// A subagent that had to join would be counted as a second occupant of its
// parent's own work — which is not a collision — and on an exclusive lane it
// would queue behind its own parent forever.
func TestSubagentInheritsItsParentsMembership(t *testing.T) {
	s, a := chState(t, "parent", "other")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["parent"].Token, Channel: "work", Text: "t"})

	// A subagent registers naming its parent, and joins nothing.
	res := spawnChild(t, s, a["parent"].Token, "parent", "nonce-child-0123456789abcdef")
	helper := s.Lanes[res["lane_id"].(string)]
	do(t, s, &Op{Kind: OpAckBoard, Token: helper.Token})

	ch := s.Channels["work"]
	if _, joined := ch.Members["helper"]; joined {
		t.Fatal("a subagent must not become a member in its own right")
	}
	if len(ch.Members) != 1 {
		t.Fatalf("the lane must still show ONE occupant, got %d", len(ch.Members))
	}
	// …and yet it may speak.
	r := do(t, s, &Op{Kind: OpLanePost, Token: helper.Token, Channel: "work", Body: "progress"})
	if r["lane_id"] != "work" {
		t.Fatalf("the subagent should be able to post: %v", r)
	}
	// An unrelated agent still cannot.
	_ = mustFail(t, s, &Op{Kind: OpLanePost, Token: a["other"].Token, Channel: "work", Body: "hi"})
}

func TestSubagentAnnouncementIsAttributedToTheParent(t *testing.T) {
	s, a := chState(t, "parent", "peer")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["parent"].Token, Channel: "work", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["peer"].Token, Channel: "work"})
	res := spawnChild(t, s, a["parent"].Token, "parent", "nonce-child-0123456789abcdef")
	helper := s.Lanes[res["lane_id"].(string)]
	do(t, s, &Op{Kind: OpAckBoard, Token: helper.Token})

	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: helper.Token, Channel: "work", Body: "heads up"})
	// The parent must not be asked to acknowledge its own subagent's news.
	if r["must_ack"] != 1 {
		t.Fatalf("only the peer should owe an ack, got %v", r)
	}
	an := s.Announcements[r["serial"].(uint64)]
	if an.From != "parent" {
		t.Fatalf("attributed to %q, want the parent — peers see one participant", an.From)
	}
	if an.Required["parent"] {
		t.Fatal("the parent must not owe an ack for what its own subagent said")
	}
}

// The parent stays accountable: its departure takes the subagent's access with
// it, because the access was never the subagent's to begin with.
func TestSubagentLosesAccessWhenTheParentLeaves(t *testing.T) {
	s, a := chState(t, "parent")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["parent"].Token, Channel: "work", Text: "t"})
	res := spawnChild(t, s, a["parent"].Token, "parent", "nonce-child-0123456789abcdef")
	helper := s.Lanes[res["lane_id"].(string)]
	do(t, s, &Op{Kind: OpAckBoard, Token: helper.Token})
	do(t, s, &Op{Kind: OpLanePost, Token: helper.Token, Channel: "work", Body: "before"})

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["parent"].Token, Channel: "work"})
	_ = mustFail(t, s, &Op{Kind: OpLanePost, Token: helper.Token, Channel: "work", Body: "after"})
}

// A corrupted or hand-edited ledger could contain a parent cycle. An unbounded
// walk inside the single writer loop would hang the whole daemon rather than
// fail one call.
func TestParentCycleCannotHangTheLoop(t *testing.T) {
	s, a := chState(t, "opener")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["opener"].Token, Channel: "work", Text: "t"})
	x := do(t, s, &Op{Kind: OpRegisterLane, Name: "x", NewToken: "tx", Parent: "y"})
	y := do(t, s, &Op{Kind: OpRegisterLane, Name: "y", NewToken: "ty", Parent: "x"})
	_, _ = x, y
	if got := s.speaksFor(s.Channels["work"], "x"); got != "" {
		t.Fatalf("a cycle must resolve to no membership, got %q", got)
	}
}

// ── the director (SPEC-CHANNELS.md §8.1) ────────────────────────────────

func makeCoordinator(t *testing.T, s *State, lane string) {
	t.Helper()
	if _, _, err := s.Apply(&Op{Kind: OpGrantRole, To: lane, Mode: RoleCoordinator}, testNow); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

// The whole point of the role: unsticking a lane whose owner is gone. And it
// must never be silent — the former owner is named, exactly as force_release on
// a directory claim is (SPEC §9).
func TestDirectorCanUnstickALaneAndIsNeverSilent(t *testing.T) {
	s, a := chState(t, "owner", "director", "waiter")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "t", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["waiter"].Token, Channel: "hot"}) // queued
	makeCoordinator(t, s, "director")

	r := do(t, s, &Op{
		Kind: OpLaneForceRelease, Token: a["director"].Token, Channel: "hot",
		Note: "owner's machine died",
	})
	if r["released"] != true || r["former_owner"] != "owner" {
		t.Fatalf("want a release naming the former owner, got %v", r)
	}
	ch := s.Channels["hot"]
	if ch.Owner != "" {
		t.Fatalf("lane still locked to %q", ch.Owner)
	}
	if _, in := ch.Members["waiter"]; !in {
		t.Fatal("the waiter should have been admitted once the lock lifted")
	}
}

// No agent may promote itself: the role arrives only through the human's admin
// path, and without it every director power is refused.
func TestDirectorPowersAreRefusedWithoutTheRole(t *testing.T) {
	s, a := chState(t, "owner", "nobody")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "t", Exclusive: true})
	for _, op := range []*Op{
		{Kind: OpLaneForceRelease, Token: a["nobody"].Token, Channel: "hot"},
		{Kind: OpLaneEvict, Token: a["nobody"].Token, Channel: "hot", To: "owner"},
		{Kind: OpLaneMerge, Token: a["nobody"].Token, Channel: "hot", To: "hot2"},
	} {
		err := mustFail(t, s, op)
		if !strings.Contains(err.Error(), "E_NOT_COORDINATOR") {
			t.Fatalf("%s: want E_NOT_COORDINATOR, got %v", op.Kind, err)
		}
	}
}

func TestDirectorCanEvictAndTheAgentIsTold(t *testing.T) {
	s, a := chState(t, "director", "stray")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["director"].Token, Channel: "work", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["stray"].Token, Channel: "work"})
	makeCoordinator(t, s, "director")

	r := do(t, s, &Op{
		Kind: OpLaneEvict, Token: a["director"].Token, Channel: "work",
		To: "stray", Note: "wrong lane",
	})
	if r["evicted"] != true {
		t.Fatalf("want an eviction, got %v", r)
	}
	if _, in := s.Channels["work"].Members["stray"]; in {
		t.Fatal("the evicted agent must not remain a member")
	}
}

// An announcement is the strongest thing an agent can do to a lane: it obliges
// every member to acknowledge it and re-pings them until they do. The awareness
// gate was enforced on lane_open and lane_join — the WEAKER acts — and not on
// this one, so an agent that had just reattached after losing its context could
// oblige a whole lane to answer something while never having read the board.
func TestAnnouncingRequiresHavingReadTheBoard(t *testing.T) {
	s := NewState("t", DefaultLimits())
	r, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "a", NewToken: "tok"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r["lane_id"].(string)
	do(t, s, &Op{Kind: OpAckBoard, Token: s.Lanes[id].Token})
	do(t, s, &Op{Kind: OpLaneOpen, Token: s.Lanes[id].Token, Channel: "hot", Text: "w"})

	// Losing context and reattaching re-arms the gate: a new activation has
	// read nothing.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "a", NewToken: "tok2", SessionID: "",
	}, testNow); err != nil {
		t.Fatal(err)
	}
	s.Lanes[id].AckedSerial = 0 // what a fresh activation looks like

	err = mustFail(t, s, &Op{Kind: OpLaneAnnounce, Token: s.Lanes[id].Token, Channel: "hot", Body: "FREEZE"})
	if !strings.Contains(err.Error(), "E_MUST_ACK_BOARD") {
		t.Fatalf("want the awareness gate, got %v", err)
	}
	// And acking clears it, so the gate is a step rather than a wall.
	do(t, s, &Op{Kind: OpAckBoard, Token: s.Lanes[id].Token})
	do(t, s, &Op{Kind: OpLaneAnnounce, Token: s.Lanes[id].Token, Channel: "hot", Body: "FREEZE"})
}

// The asymmetry is deliberate, and pinned so it does not get "fixed" into
// consistency. A post is traffic nobody must answer; gating a remark is
// friction without a corresponding risk.
func TestPostingIsDeliberatelyNotGated(t *testing.T) {
	s := NewState("t", DefaultLimits())
	r, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "a", NewToken: "tok"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r["lane_id"].(string)
	do(t, s, &Op{Kind: OpAckBoard, Token: s.Lanes[id].Token})
	do(t, s, &Op{Kind: OpLaneOpen, Token: s.Lanes[id].Token, Channel: "hot", Text: "w"})
	s.Lanes[id].AckedSerial = 0

	do(t, s, &Op{Kind: OpLanePost, Token: s.Lanes[id].Token, Channel: "hot", Body: "just so you know"})
}

// Context loss is the most common thing that happens to an agent, and a token
// rotation must not cost it its place. Everything the lane held has to survive:
// exclusive ownership, membership, queue position, and — the one it cannot
// reconstruct for itself — what it still owes an acknowledgement on.
func TestReattachingKeepsEverythingTheAgentHeld(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg := func(name, sess, tok string) *Lane {
		t.Helper()
		r, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: name, SessionID: sess, NewToken: tok}, testNow)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r["lane_id"].(string)
		do(t, s, &Op{Kind: OpAckBoard, Token: s.Lanes[id].Token})
		return s.Lanes[id]
	}
	owner, waiter := reg("owner", "s1", "t1"), reg("waiter", "s2", "t2")
	reg("member", "s3", "t3")
	makeCoordinator(t, s, "owner")
	do(t, s, &Op{Kind: OpLaneOpen, Token: owner.Token, Channel: "hot", Text: "w", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneAdmit, Token: owner.Token, Channel: "hot", To: "member"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: waiter.Token, Channel: "hot"})
	ar := do(t, s, &Op{Kind: OpLaneAnnounce, Token: owner.Token, Channel: "hot", Body: "FREEZE"})
	ser, _ := ar["serial"].(uint64)

	// All three lose context and come back: same name, same session, new token.
	for _, a := range [][3]string{{"owner", "s1", "t1b"}, {"waiter", "s2", "t2b"}, {"member", "s3", "t3b"}} {
		if _, _, err := s.Apply(&Op{
			Kind: OpRegisterLane, Name: a[0], SessionID: a[1], NewToken: a[2],
		}, testNow); err != nil {
			t.Fatalf("reattach %s: %v", a[0], err)
		}
	}

	ch := s.Channels["hot"]
	if ch.Owner != "owner" {
		t.Fatalf("exclusive ownership must survive a token rotation, owner=%q", ch.Owner)
	}
	if _, in := ch.Members["member"]; !in {
		t.Fatal("membership must survive")
	}
	if len(ch.Queue) != 1 || ch.Queue[0] != "waiter" {
		t.Fatalf("queue position must survive, got %v", ch.Queue)
	}
	if n := len(s.Unacked("member")); n != 1 {
		t.Fatalf("an obligation must survive the agent forgetting it, got %d", n)
	}
	// And it can still be cleared with the NEW token — acking what you owe is
	// not gated on the board, or an agent could be stuck owing something it is
	// not allowed to answer.
	do(t, s, &Op{Kind: OpLaneAck, Token: "t3b", MsgSerial: ser})
	if n := len(s.Unacked("member")); n != 0 {
		t.Fatalf("the reattached agent must be able to clear it, still %d", n)
	}
}

// SPEC-CHANNELS.md §8.2: a subagent inherits its parent's lanes and does not
// join, queue or count separately. Letting it join was not merely redundant —
// it deadlocked a parent against its own child.
//
// Observed: a subagent asking to join the lane its PARENT held exclusively was
// queued behind that parent at position 2, with a hint telling it to send the
// owner a request. The parent does not release until the subagent's work is
// done, so each waited on the other. All the while lane_post from that subagent
// already worked, because speaking is exactly what the inherited membership is
// for.
func TestASubagentInheritsItsParentsLaneRatherThanQueueingBehindIt(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg := func(name, parent string) *Lane {
		t.Helper()
		op := &Op{Kind: OpRegisterLane, Name: name, NewToken: "tok-" + name, Parent: parent}
		if parent != "" {
			op.ParentNonce = "nonce-" + name + "-0123456789abcdef"
		}
		r, _, err := s.Apply(op, testNow)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r["lane_id"].(string)
		l := s.Lanes[id]
		if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: l.Token}, testNow); err != nil {
			t.Fatal(err)
		}
		return l
	}
	parent := reg("parent", "")
	// Vouched, because parent alone is a claim anyone can make: an agent that
	// simply declared parent:"victim" used to inherit the victim's memberships,
	// skip its exclusive queue, and be exempt from its claims in the guard.
	do(t, s, &Op{Kind: OpVouchChild, Token: parent.Token, Nonce: "nonce-sub-0123456789abcdef"})
	sub := reg("sub", "parent")
	do(t, s, &Op{Kind: OpLaneOpen, Token: parent.Token, Channel: "hot", Text: "w", Exclusive: true})

	r := do(t, s, &Op{Kind: OpLaneJoin, Token: sub.Token, Channel: "hot"})
	if r["queued"] == true {
		t.Fatalf("a subagent queued behind its own parent — neither can proceed: %v", r)
	}
	if r["joined"] != true || r["under"] != "parent" {
		t.Fatalf("the subagent should already be in the lane, through its parent: %v", r)
	}
	if q := s.Channels["hot"].Queue; len(q) != 0 {
		t.Fatalf("a subagent must not queue separately, got %v", q)
	}
	if _, counted := s.Channels["hot"].Members["sub"]; counted {
		t.Fatal("a subagent must not count as a separate member")
	}
	// And the thing the inherited membership is FOR still works.
	if _, _, err := s.Apply(&Op{Kind: OpLanePost, Token: sub.Token, Channel: "hot", Body: "hi"}, testNow); err != nil {
		t.Fatalf("a subagent speaks under its parent's membership: %v", err)
	}
	// An announcement binds the parent, once, not both.
	ar := do(t, s, &Op{Kind: OpLaneOpen, Token: parent.Token, Channel: "other", Text: "x"})
	_ = ar
	if _, counted := s.Channels["hot"].Members["sub"]; counted {
		t.Fatal("still must not count separately")
	}
}

// The promotion check used to be `Status == StatusClosed`, but an agent that
// CRASHED is `stale` and one that is asleep is `dormant` — neither is closed.
// So a dead agent was handed exclusive ownership of the lane and every healthy
// agent behind it waited on a corpse. Observed exactly: sweep marked the agent
// stale, the owner left, and the lane's owner became the crashed agent.
//
// close_lane dequeues, so this only ever showed up for real crashes — which is
// the case the queue exists to survive.
func TestACrashedAgentIsNeverPromotedToOwner(t *testing.T) {
	s, a := chState(t, "owner", "crashed", "live")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "w", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["crashed"].Token, Channel: "hot"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["live"].Token, Channel: "hot"})
	// Dies the way a real agent dies: detected by the sweep, never closed.
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"crashed"}}, testNow); err != nil {
		t.Fatal(err)
	}
	if s.Lanes["crashed"].Status != StatusStale {
		t.Fatalf("precondition: want stale, got %q", s.Lanes["crashed"].Status)
	}

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["owner"].Token, Channel: "hot"})
	ch := s.Channels["hot"]
	if ch.Owner == "crashed" {
		t.Fatal("an exclusive lane was locked behind a crashed agent")
	}
	if ch.Owner != "live" {
		t.Fatalf("the first agent that could actually take it should have, owner=%q", ch.Owner)
	}
	// Skipped, not evicted: going stale has never removed an agent from a lane,
	// and a persistent agent that wakes must still find itself in line.
	if !slices.Contains(ch.Queue, "crashed") {
		t.Fatalf("the crashed agent should keep its place for when it recovers, queue=%v", ch.Queue)
	}
}

// And when nobody waiting can take it, the lane must not sit locked-open with a
// queue nothing will ever drain — that is the same "waiting forever" bug in
// another dress.
func TestALaneNobodyCanTakeIsReleasedRatherThanStranded(t *testing.T) {
	s, a := chState(t, "owner", "crashed")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "w", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["crashed"].Token, Channel: "hot"})
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"crashed"}}, testNow); err != nil {
		t.Fatal(err)
	}

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["owner"].Token, Channel: "hot"})
	ch := s.Channels["hot"]
	if ch.Owner != "" {
		t.Fatalf("no live waiter, so the lane must simply open; owner=%q", ch.Owner)
	}
	if len(ch.Queue) != 0 {
		t.Fatalf("a queue on an open lane is drained by nothing; got %v", ch.Queue)
	}
	if _, in := ch.Members["crashed"]; !in {
		t.Fatal("the waiter was waiting to work here; with the lock gone it is a member")
	}
}

// Eviction used to check membership only, so a director trying to remove an
// agent that was QUEUED got evicted:false / "not a member" — technically true,
// and it meant the director concluded the agent was not on the lane and moved
// on. Then the owner left and the agent it had tried to remove was promoted to
// OWNER of that lane. Observed exactly, in that order.
func TestEvictingAQueuedAgentRemovesIt(t *testing.T) {
	s, a := chState(t, "director", "owner", "waiter", "extra")
	makeCoordinator(t, s, "director")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "w", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["waiter"].Token, Channel: "hot"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["extra"].Token, Channel: "hot"})

	r := do(t, s, &Op{Kind: OpLaneEvict, Token: a["director"].Token, Channel: "hot", To: "waiter"})
	if r["evicted"] != true || r["from_queue"] != true {
		t.Fatalf("removing somebody from a lane must remove them, got %v", r)
	}
	if q := s.Channels["hot"].Queue; len(q) != 1 || q[0] != "extra" {
		t.Fatalf("the evicted agent must be out of the queue, got %v", q)
	}
	// The consequence that made this matter: the owner leaves, and whoever the
	// director evicted must NOT be the one promoted.
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["owner"].Token, Channel: "hot"})
	if got := s.Channels["hot"].Owner; got == "waiter" {
		t.Fatal("an evicted agent was promoted to owner of the lane it was evicted from")
	} else if got != "extra" {
		t.Fatalf("the next real waiter should have been promoted, owner=%q", got)
	}
}

// The genuinely-absent case must still be distinguishable, and must say what to
// do about it rather than just denying.
func TestEvictingSomebodyWhoIsNotThereSaysSoUsefully(t *testing.T) {
	s, a := chState(t, "director", "owner", "stranger")
	makeCoordinator(t, s, "director")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "w"})

	r := do(t, s, &Op{Kind: OpLaneEvict, Token: a["director"].Token, Channel: "hot", To: "stranger"})
	if r["evicted"] != false {
		t.Fatalf("nothing to evict, got %v", r)
	}
	if d, _ := r["detail"].(string); !strings.Contains(d, "board") {
		t.Fatalf("a denial must point at the fix, got %v", r)
	}
}

// §11's open question, answered by a human-granted coordinator rather than by a
// score: merging is destructive to context, and a threshold is the wrong thing
// to trust with it.
func TestDirectorCanMergeTwoLanesThatDriftedIntoOneJob(t *testing.T) {
	s, a := chState(t, "director", "alpha", "beta")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "auth-a", Text: "auth work"})
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["beta"].Token, Channel: "auth-b", Text: "also auth work"})
	makeCoordinator(t, s, "director")

	r := do(t, s, &Op{
		Kind: OpLaneMerge, Token: a["director"].Token,
		Channel: "auth-a", To: "auth-b", Note: "same work",
	})
	if r["moved"] != 1 {
		t.Fatalf("alpha should have moved across, got %v", r)
	}
	if _, gone := s.Channels["auth-a"]; gone {
		t.Fatal("the source lane should be gone after a merge")
	}
	dst := s.Channels["auth-b"]
	for _, want := range []string{"alpha", "beta"} {
		if _, in := dst.Members[want]; !in {
			t.Fatalf("%s should be in the merged lane, members=%v", want, dst.Members)
		}
	}
}

// A merge used to take the source lane's members and drop everything else on
// the floor. Verified before the fix: src.Queue=[waiter] became dst.Queue=[]
// and the waiter belonged to neither lane — blocked forever behind an exclusive
// owner that no longer existed, with nothing said to them.
//
// Where the queue goes depends on the destination, and both answers give the
// agent what it was waiting for: dst is open here, so waiting is over.
func TestMergingIntoAnOpenLaneAdmitsTheAgentsThatWereWaiting(t *testing.T) {
	s, a := chState(t, "director", "owner", "waiter", "host")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "src", Text: "work", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["waiter"].Token, Channel: "src"}) // queued behind owner
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["host"].Token, Channel: "dst", Text: "same work"})
	makeCoordinator(t, s, "director")
	if q := s.Channels["src"].Queue; len(q) != 1 || q[0] != "waiter" {
		t.Fatalf("precondition: waiter should be queued on src, got %v", q)
	}

	r := do(t, s, &Op{Kind: OpLaneMerge, Token: a["director"].Token, Channel: "src", To: "dst"})
	if r["admitted"] != 1 {
		t.Fatalf("the queued agent should have been admitted, got %v", r)
	}
	if _, in := s.Channels["dst"].Members["waiter"]; !in {
		t.Fatalf("waiter was dropped by the merge; members=%v", s.Channels["dst"].Members)
	}
}

// The other half: dst is exclusive, so the agent is still blocked — but blocked
// on a lane that exists, in a queue it can be promoted out of.
func TestMergingIntoAnExclusiveLaneKeepsTheWaitersQueued(t *testing.T) {
	s, a := chState(t, "director", "owner", "waiter", "host")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "src", Text: "work", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["waiter"].Token, Channel: "src"})
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["host"].Token, Channel: "dst", Text: "same work", Exclusive: true})
	makeCoordinator(t, s, "director")

	r := do(t, s, &Op{Kind: OpLaneMerge, Token: a["director"].Token, Channel: "src", To: "dst"})
	if r["queued"] != 1 {
		t.Fatalf("the waiter should still be waiting, on dst; got %v", r)
	}
	if q := s.Channels["dst"].Queue; len(q) != 1 || q[0] != "waiter" {
		t.Fatalf("waiter should be in dst's queue, got %v", q)
	}
	// And the queue still works: releasing the owner promotes them.
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["host"].Token, Channel: "dst"})
	if s.Channels["dst"].Owner != "waiter" {
		t.Fatalf("the carried-over waiter should have been promoted, owner=%q", s.Channels["dst"].Owner)
	}
}

// An outstanding announcement left naming the deleted lane is countable on NO
// board — invisible on the source because it is gone, invisible on the
// destination because it names the wrong id — while still obliging its members
// to acknowledge it. That is the abandoned-announcement failure mode exactly.
func TestMergeCarriesOutstandingAnnouncementsToTheSurvivingLane(t *testing.T) {
	s, a := chState(t, "director", "owner", "other", "host")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "src", Text: "work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["other"].Token, Channel: "src"})
	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["owner"].Token, Channel: "src", Body: "read this"})
	serial := r["serial"].(uint64)
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["host"].Token, Channel: "dst", Text: "same work"})
	makeCoordinator(t, s, "director")
	if n := len(s.Unacked("other")); n != 1 {
		t.Fatalf("precondition: other owes one ack, got %d", n)
	}

	do(t, s, &Op{Kind: OpLaneMerge, Token: a["director"].Token, Channel: "src", To: "dst"})
	if got := s.Announcements[serial].Channel; got != "dst" {
		t.Fatalf("announcement still names the deleted lane %q", got)
	}
	if waiting, _, _ := s.unackedIn("dst"); waiting != 1 {
		t.Fatalf("the carried announcement should show on dst's board, waiting=%d", waiting)
	}
	// It still binds, and acking it still resolves it.
	do(t, s, &Op{Kind: OpLaneAck, Token: a["other"].Token, MsgSerial: serial})
	if st := s.Announcements[serial].State; st != AnnounceAcked {
		t.Fatalf("acking a carried announcement should close it, state=%q", st)
	}
}

func TestMergingALaneIntoItselfIsRefused(t *testing.T) {
	s, a := chState(t, "director")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["director"].Token, Channel: "x", Text: "t"})
	makeCoordinator(t, s, "director")
	_ = mustFail(t, s, &Op{Kind: OpLaneMerge, Token: a["director"].Token, Channel: "x", To: "x"})
}

func TestDirectorCanAdmitAnAgentThatDidNotMatch(t *testing.T) {
	s, a := chState(t, "director", "outsider")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["director"].Token, Channel: "work", Text: "t"})
	makeCoordinator(t, s, "director")

	r := do(t, s, &Op{
		Kind: OpLaneAdmit, Token: a["director"].Token, Channel: "work",
		To: "outsider", Note: "belongs here", Score: 0.4, Threshold: 0.33, ScorerID: "director-call",
	})
	if r["admitted"] != true {
		t.Fatalf("want an admission, got %v", r)
	}
	m := s.Channels["work"].Members["outsider"]
	if m == nil {
		t.Fatal("the admitted agent must be a member")
	}
	// A gated join stays exactly as explainable as an automatic one (§10.3).
	if m.Score != 0.4 || m.ScorerID != "director-call" {
		t.Fatalf("the recorded score must survive an admission: %+v", m)
	}
	// And it must clear any queue entry rather than leaving a ghost.
	if len(s.Channels["work"].Queue) != 0 {
		t.Fatalf("queue should be clean, got %v", s.Channels["work"].Queue)
	}
}

func TestAdmittingIsCoordinatorOnlyAndRefusesADeadAgent(t *testing.T) {
	s, a := chState(t, "director", "nobody")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["director"].Token, Channel: "work", Text: "t"})
	_ = mustFail(t, s, &Op{Kind: OpLaneAdmit, Token: a["nobody"].Token, Channel: "work", To: "director"})

	makeCoordinator(t, s, "director")
	err := mustFail(t, s, &Op{
		Kind: OpLaneAdmit, Token: a["director"].Token,
		Channel: "work", To: "ghost",
	})
	if !strings.Contains(err.Error(), "E_NO_LANE") {
		t.Fatalf("admitting a nonexistent agent must fail clearly, got %v", err)
	}
}

// A lane outlives its members. Some work is permanent — a standing "release"
// or "security review" lane that agents register into and drop out of as they
// come and go — so emptying a lane must not destroy it, and the next agent to
// arrive must find the same lane rather than a fresh one with no history.
func TestALaneSurvivesEveryMemberLeaving(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "release", Text: "the standing release lane"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "release"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["alpha"].Token, Channel: "release"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["beta"].Token, Channel: "release"})

	ch := s.Channels["release"]
	if ch == nil {
		t.Fatal("an empty lane must persist — permanent lanes are the point")
	}
	if len(ch.Members) != 0 {
		t.Fatalf("expected no members, got %v", ch.Members)
	}
	if ch.Topic != "the standing release lane" {
		t.Fatalf("the lane's identity must survive: %q", ch.Topic)
	}
	// And a later arrival rejoins THE SAME lane, with its accumulated topic.
	res := do(t, s, &Op{Kind: OpLaneJoin, Token: a["alpha"].Token, Channel: "release"})
	if res["joined"] != true || res["topic"] != "the standing release lane" {
		t.Fatalf("rejoining should find the same lane: %v", res)
	}
}

// An agent that is only watching owes nobody an acknowledgement.
//
// Not every agent does development work: a monitor, a reporter, a reviewer
// waiting to be summoned. They may want to see a lane's traffic without joining
// it, and an announcement must never oblige them — being nagged for work you
// are not doing is how a fleet learns to ignore announcements.
func TestOnlyMembersOweAnAcknowledgement(t *testing.T) {
	s, a := chState(t, "worker", "watcher", "outsider")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["worker"].Token, Channel: "auth", Text: "auth work"})
	do(t, s, &Op{Kind: OpLaneSubscribe, Token: a["watcher"].Token, Channel: "auth"})

	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["worker"].Token, Channel: "auth", Body: "renaming Token"})
	if r["must_ack"] != 0 {
		t.Fatalf("a subscriber must not be obliged, got must_ack=%v", r["must_ack"])
	}
	for _, who := range []string{"watcher", "outsider"} {
		if n := len(s.Unacked(who)); n != 0 {
			t.Errorf("%s is not a member and owes nothing, got %d", who, n)
		}
	}
	// Joining is what creates the obligation — from then on, not retroactively.
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["watcher"].Token, Channel: "auth"})
	if n := len(s.Unacked("watcher")); n != 0 {
		t.Errorf("joining must not retroactively owe acks for old announcements, got %d", n)
	}
	do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["worker"].Token, Channel: "auth", Body: "second"})
	if n := len(s.Unacked("watcher")); n != 1 {
		t.Errorf("a member owes an ack for announcements made while a member, got %d", n)
	}
}

// An announcement that gave up must become MORE visible, not less.
//
// Only `open` was counted for the board, so an announcement that exhausted its
// retries and was marked `unacked` vanished from the roster at exactly the
// moment it became interesting — somebody was told something with collision
// risk, never acknowledged it, and Lanes had stopped asking. The constant's own
// comment claimed it "stays visible, never dropped". It did not.
func TestAnAbandonedAnnouncementStaysOnTheBoard(t *testing.T) {
	s, a := chState(t, "speaker", "ignorer")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["speaker"].Token, Channel: "auth", Text: "auth work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["ignorer"].Token, Channel: "auth"})
	r := do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["speaker"].Token, Channel: "auth", Body: "interface change"})
	serial := r["serial"].(uint64)

	waiting, abandoned, _ := s.unackedIn("auth")
	if waiting != 1 || abandoned != 0 {
		t.Fatalf("while Lanes is still asking: waiting=%d abandoned=%d", waiting, abandoned)
	}

	// Lanes gives up.
	s.Announcements[serial].State = AnnounceUnacked
	waiting, abandoned, _ = s.unackedIn("auth")
	if waiting != 0 || abandoned != 1 {
		t.Fatalf("after giving up: waiting=%d abandoned=%d", waiting, abandoned)
	}

	// And it must reach the board, under its own key — folding the two into one
	// number would hide which of them needs a person.
	b := s.Board()
	chans, _ := b["channels"].([]map[string]any)
	if len(chans) == 0 {
		t.Fatal("no channels on the board")
	}
	var found map[string]any
	for _, c := range chans {
		if c["id"] == "auth" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("auth lane missing from the board")
	}
	if got, ok := found["abandoned_announcements"]; !ok || got != 1 {
		t.Fatalf("an abandoned announcement must be on the board: %v", found)
	}
	if _, ok := found["unacked_announcements"]; ok {
		t.Errorf("nothing is awaiting an ack any more; that key should be absent: %v", found)
	}
}

// "Still asking" and "asking somebody who is not there" look identical on a
// board and are not the same problem.
//
// Redelivery is driven by the agent POLLING: a sleeping or crashed agent never
// polls, so its retry budget never spends and the announcement never reaches
// `unacked`. It sits at "awaiting ack" indefinitely while the board gives no
// hint that nothing can arrive. A standing role may legitimately sleep for a
// week, so this is not an error — it is a fact the reader cannot otherwise get
// without cross-referencing the roster.
func TestAnAnnouncementOwedOnlyByAbsenteesSaysSo(t *testing.T) {
	s, a := chState(t, "sender", "awake", "asleep")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["sender"].Token, Channel: "work", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["awake"].Token, Channel: "work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["asleep"].Token, Channel: "work"})
	do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["sender"].Token, Channel: "work", Body: "FREEZE"})

	// One member goes away; the other is still working and might yet answer.
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"asleep"}}, testNow); err != nil {
		t.Fatal(err)
	}
	waiting, _, blocked := s.unackedIn("work")
	if waiting != 1 {
		t.Fatalf("still outstanding, got waiting=%d", waiting)
	}
	if blocked != 0 {
		t.Fatalf("somebody who could answer is still here; not blocked yet (blocked=%d)", blocked)
	}

	// Now the one who could answer answers, leaving only the absentee.
	for _, ser := range s.announcementSerials() {
		do(t, s, &Op{Kind: OpLaneAck, Token: a["awake"].Token, MsgSerial: ser})
	}
	waiting, _, blocked = s.unackedIn("work")
	if waiting != 1 || blocked != 1 {
		t.Fatalf("owed only by an absentee: want waiting=1 blocked=1, got %d/%d", waiting, blocked)
	}
	// And the board carries it, or the reader still cannot see it.
	chans, _ := s.Board()["channels"].([]map[string]any)
	var shown any
	for _, cm := range chans {
		if cm["id"] == "work" {
			shown = cm["blocked_announcements"]
		}
	}
	if shown != 1 {
		t.Fatalf("the board must show it, got %v", shown)
	}
}

// Coming back must clear it, or the label becomes permanent noise.
func TestAnAbsenteeReturningUnblocksTheAnnouncement(t *testing.T) {
	s, a := chState(t, "sender")
	// Registered with a session id, because that is what makes a reattach a
	// reattach rather than a second lane.
	r, _, err := s.Apply(&Op{Kind: OpRegisterLane, Name: "away", NewToken: "tk", SessionID: "sess-away"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	awayID, _ := r["lane_id"].(string)
	do(t, s, &Op{Kind: OpAckBoard, Token: s.Lanes[awayID].Token})
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["sender"].Token, Channel: "work", Text: "t"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: s.Lanes[awayID].Token, Channel: "work"})
	do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["sender"].Token, Channel: "work", Body: "FREEZE"})
	if _, _, err := s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"away"}}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, _, blocked := s.unackedIn("work"); blocked != 1 {
		t.Fatalf("precondition: want blocked=1, got %d", blocked)
	}
	// It reattaches, the documented way.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: "away", NewToken: "fresh", SessionID: "sess-away",
	}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, _, blocked := s.unackedIn("work"); blocked != 0 {
		t.Fatalf("somebody who can answer is back; blocked=%d", blocked)
	}
}

// A standing role sleeps between activations, and what it holds while asleep is
// the difference between a fleet that survives its agents going quiet and one
// that wedges. Verified against the real transitions rather than asserted:
// exclusivity must yield (nobody waits on a sleeper), membership and
// obligations must survive (it will be back), and waking must restore it whole.
func TestAPersistentRoleSleepingYieldsLocksButKeepsItsPlace(t *testing.T) {
	s := NewState("t", DefaultLimits())
	reg := func(name, tok, nonce string) *Lane {
		t.Helper()
		op := &Op{Kind: OpRegisterLane, Name: name, NewToken: tok, PID: 4242}
		if nonce != "" {
			op.LaneKind, op.Nonce = KindPersistent, nonce
		}
		r, _, err := s.Apply(op, testNow)
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		id, _ := r["lane_id"].(string)
		do(t, s, &Op{Kind: OpAckBoard, Token: s.Lanes[id].Token})
		return s.Lanes[id]
	}
	const nonce = "nonce-standing-0123456789abcdef"
	standing, peer := reg("standing", "t1", nonce), reg("peer", "t2", "")

	do(t, s, &Op{Kind: OpLaneOpen, Token: standing.Token, Channel: "hot", Text: "long work", Exclusive: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: peer.Token, Channel: "hot"}) // queued
	do(t, s, &Op{Kind: OpLaneOpen, Token: peer.Token, Channel: "open", Text: "shared"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: standing.Token, Channel: "open"})
	ar := do(t, s, &Op{Kind: OpLaneAnnounce, Token: peer.Token, Channel: "open", Body: "FREEZE"})
	ser, _ := ar["serial"].(uint64)

	// It sleeps the way a standing role does. Persistent lanes go dormant, not
	// stale — the distinction is the whole reason the kind exists.
	if _, _, err := s.Apply(&Op{Kind: OpSweep, DeadLanes: []string{"standing"}}, testNow); err != nil {
		t.Fatal(err)
	}
	if got := s.Lanes["standing"].Status; got != StatusDormant {
		t.Fatalf("a persistent lane sleeps, it does not die; got %q", got)
	}
	if s.Channels["hot"].Owner == "standing" {
		t.Fatal("a lane must not stay locked behind an agent that is asleep")
	}
	if _, in := s.Channels["hot"].Members["peer"]; !in {
		t.Fatal("the agent that was waiting should have been admitted when the lock lifted")
	}
	if _, in := s.Channels["open"].Members["standing"]; !in {
		t.Fatal("membership survives sleep — it will be back")
	}
	if n := len(s.Unacked("standing")); n != 1 {
		t.Fatalf("an obligation survives sleep too, got %d", n)
	}

	// And it wakes with everything intact, including what it owes.
	if _, _, err := s.Apply(&Op{
		Kind: OpResumeLane, Nonce: nonce, ResumeID: "r1", NewToken: "t1b", PID: 99,
	}, testNow); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := s.Lanes["standing"].Status; got != StatusActive {
		t.Fatalf("waking makes it active, got %q", got)
	}
	if why := s.Lanes["standing"].StaleReason; why != "" {
		t.Fatalf("a working agent must not still be labelled %q", why)
	}
	do(t, s, &Op{Kind: OpLaneAck, Token: "t1b", MsgSerial: ser})
	if n := len(s.Unacked("standing")); n != 0 {
		t.Fatalf("it must be able to clear what it owed, still %d", n)
	}
}

// SPEC-CHANNELS.md §10.3 promises every auto-join is explainable. It was false
// for any agent that passed through a queue.
//
// Queue holds ids in order, which is all the wire needs — but the join op that
// put an agent there carries its whole provenance, and promotion fabricated a
// fresh Membership{ScorerID: "queue"} instead. An agent auto-matched at 0.71
// with evidence therefore surfaced, after waiting, as a manual member with
// score 0 and no reason: indistinguishable on the board from somebody who
// simply asked to be there, and an unjustified auto-join could not be audited.
//
// Found by an independent reviewer (GPT-5.6-sol) reading for exactly this
// class, after the author had stopped seeing it.
func TestAPromotedAgentKeepsWhyItWasMatched(t *testing.T) {
	s, a := chState(t, "owner", "matched")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "w", Exclusive: true})
	do(t, s, &Op{
		Kind: OpLaneJoin, Token: a["matched"].Token, Channel: "hot",
		Score: 0.71, Threshold: 0.33, ScorerID: "embed:qwen3", ScorerVersion: "2",
		Evidence: []string{"internal/core/channel.go"}, Auto: true,
	})
	if _, queued := s.Channels["hot"].Members["matched"]; queued {
		t.Fatal("precondition: an exclusive lane queues rather than admitting")
	}

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["owner"].Token, Channel: "hot"})
	m := s.Channels["hot"].Members["matched"]
	if m == nil {
		t.Fatal("the waiter should have been promoted")
	}
	if !m.Auto {
		t.Error("it was matched automatically; recording it as a manual join is a falsehood")
	}
	if m.Score != 0.71 || m.Threshold != 0.33 {
		t.Errorf("the score that justified the match must survive the wait, got %v/%v",
			m.Score, m.Threshold)
	}
	if m.ScorerID != "embed:qwen3" || m.ScorerVersion != "2" {
		t.Errorf("and which scorer said so, got %q/%q", m.ScorerID, m.ScorerVersion)
	}
	if len(m.Evidence) != 1 {
		t.Errorf("and the evidence, got %v", m.Evidence)
	}
}

// Leaving a lane ends the obligations that came WITH that lane.
//
// Only a full close dropped them, so lane_leave and lane_evict removed the
// membership and left the announcement still owed — `Unacked` kept redelivering
// it and the board kept reporting a healthy wait on somebody who was no longer
// there. Eviction is the sharpest version: it tells the agent to stop work and
// coordinate before resuming, while still nagging it to acknowledge that lane's
// traffic.
func TestLeavingALaneEndsWhatThatLaneAskedOfYou(t *testing.T) {
	s, a := chState(t, "sender", "leaver", "evicted", "stays", "dir")
	makeCoordinator(t, s, "dir")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["sender"].Token, Channel: "work", Text: "t"})
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["sender"].Token, Channel: "other", Text: "t2"})
	for _, who := range []string{"leaver", "evicted", "stays"} {
		do(t, s, &Op{Kind: OpLaneJoin, Token: a[who].Token, Channel: "work"})
	}
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["leaver"].Token, Channel: "other"})
	do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["sender"].Token, Channel: "work", Body: "FREEZE"})
	do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["sender"].Token, Channel: "other", Body: "SEPARATE"})

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["leaver"].Token, Channel: "work"})
	do(t, s, &Op{Kind: OpLaneEvict, Token: a["dir"].Token, Channel: "work", To: "evicted"})

	if n := len(s.Unacked("evicted")); n != 0 {
		t.Errorf("an agent told to stop work must not still be nagged by that lane, owes %d", n)
	}
	// Scoped: leaving ONE lane must not clear what it owes elsewhere.
	if n := len(s.Unacked("leaver")); n != 1 {
		t.Fatalf("the other lane's announcement is still owed, got %d", n)
	}
	if s.Unacked("leaver")[0].Channel != "other" {
		t.Error("and it is the other lane's, not the one it left")
	}
	// The one who stayed still owes it, and the board says so honestly.
	if n := len(s.Unacked("stays")); n != 1 {
		t.Fatalf("the remaining member still owes it, got %d", n)
	}
	waiting, _, _ := s.unackedIn("work")
	if waiting != 1 {
		t.Errorf("still genuinely waiting on the member who is there, got %d", waiting)
	}
	if got := s.departedUnackedIn("work"); got != 2 {
		t.Errorf("and the two who left without reading are recorded, got %d", got)
	}
}

// A merge changes the DESTINATION's world too, and it was told nothing.
//
// The destination silently gains another lane's members, its predicted
// footprint and its outstanding announcements — which its existing members may
// now be required to acknowledge. Only the moved side was woken, so the
// destination's owner could carry on believing its lane was unchanged and still
// exclusively its own while a whole other lane had been folded in.
//
// Raised by an independent reviewer (GPT-5.6-sol); confirmed by replaying the
// merge and listing who each event was addressed to.
func TestAMergeWakesTheLaneThatAbsorbedTheOtherOne(t *testing.T) {
	s, a := chState(t, "dir", "srcowner", "dstowner", "dstmember")
	makeCoordinator(t, s, "dir")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["srcowner"].Token, Channel: "src", Text: "src work"})
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["dstowner"].Token, Channel: "dst", Text: "dst work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["dstmember"].Token, Channel: "dst"})

	_, evs, err := s.Apply(&Op{Kind: OpLaneMerge, Token: a["dir"].Token, Channel: "src", To: "dst"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, e := range evs {
		if e.Lane != "" {
			got[e.Lane] = append(got[e.Lane], e.Type)
		}
	}
	for _, who := range []string{"dstowner", "dstmember"} {
		if !slices.Contains(got[who], "lane.absorbed") {
			t.Errorf("%s was already in the destination and must be told it absorbed a lane; got %v",
				who, got[who])
		}
	}
	// The agent that MOVED gets its own notice and must not get both — two
	// notices for one event is how a wake channel becomes noise.
	if slices.Contains(got["srcowner"], "lane.absorbed") {
		t.Errorf("the moved agent is told it joined, not that its lane absorbed one; got %v",
			got["srcowner"])
	}
	if !slices.Contains(got["srcowner"], "lane.joined") {
		t.Errorf("and it must still be told it moved; got %v", got["srcowner"])
	}
	// The coordinator did it deliberately; its own tool result already said so.
	if slices.Contains(got["dir"], "lane.absorbed") {
		t.Error("the coordinator that ran the merge does not need telling about it")
	}
}

// Every path that ends an exclusive hold must say WHO stopped owning, WHY, and
// carry SPEC §9's caution.
//
// releaseExclusive emitted a bare event with only the lane id — no former
// owner, no cause, no caution — while the two neighbouring release paths
// carried all three. It is also the path a liveness sweep uses, which is where
// the caution matters most: a consumer that cannot tell a deliberate release
// from a lapsed lease can read "released" as safe-to-take.
func TestEveryReleaseSaysWhoAndWhyAndDoesNotImplySafety(t *testing.T) {
	for _, tc := range []struct{ name, wantCause string }{
		{"released by its owner", "released by its owner"},
		{"the owner stopped coordinating", "the owner stopped coordinating"},
		{"forced by a coordinator", "forced by a coordinator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a := chState(t, "owner", "peer", "dir")
			makeCoordinator(t, s, "dir")
			do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "w", Exclusive: true})
			do(t, s, &Op{Kind: OpLaneJoin, Token: a["peer"].Token, Channel: "hot"})

			var evs []Event
			var err error
			switch tc.wantCause {
			case "released by its owner":
				_, evs, err = s.Apply(&Op{
					Kind: OpLaneExclusive, Token: a["owner"].Token, Channel: "hot", Mode: "release",
				}, testNow)
			case "the owner stopped coordinating":
				_, evs, err = s.Apply(&Op{Kind: OpSweep, StaleLanes: []string{"owner"}}, testNow)
			case "forced by a coordinator":
				_, evs, err = s.Apply(&Op{
					Kind: OpLaneForceRelease, Token: a["dir"].Token, Channel: "hot",
				}, testNow)
			}
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, e := range evs {
				if e.Type != "lane.released" {
					continue
				}
				found = true
				if e.Data["former_owner"] != "owner" {
					t.Errorf("must name who stopped owning, got %v", e.Data["former_owner"])
				}
				if e.Data["cause"] != tc.wantCause {
					t.Errorf("cause = %v, want %q", e.Data["cause"], tc.wantCause)
				}
				c, _ := e.Data["caution"].(string)
				if !strings.Contains(c, "not proof") {
					t.Errorf("a release must never imply the work stopped, got %q", c)
				}
			}
			if !found {
				t.Fatal("expected a lane.released event")
			}
		})
	}
}

// A channel's footprint is what its MEMBERS touch — all of them.
//
// An agent that queues behind an exclusive owner declares a footprint like
// anybody else, and that footprint was thrown away: not deferred, dropped. When
// the owner left and the waiter was promoted, it became a full member whose
// files the channel had no record of, so every later declaration was scored
// against a footprint missing a member's work — and the failure is silent,
// because a channel with a too-small footprint simply matches less.
func TestAPromotedWaitersFootprintCountsOnceItIsAMember(t *testing.T) {
	s, a := chState(t, "owner", "waiter")

	do(t, s, &Op{
		Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "hot", Text: "auth work",
		Exclusive: true, Predicted: []PredFile{{Path: "auth/login.go", Weight: 1}},
	})
	// The waiter declares work in different files and is queued, not joined.
	do(t, s, &Op{
		Kind: OpLaneJoin, Token: a["waiter"].Token, Channel: "hot",
		Predicted: []PredFile{{Path: "auth/session.go", Weight: 1}},
	})

	if hasPred(s.Channels["hot"].Predicted, "auth/session.go") {
		t.Error("a queued agent is not a member; its files must not count yet")
	}

	// The owner leaves and the waiter is promoted off the queue.
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["owner"].Token, Channel: "hot"})

	ch := s.Channels["hot"]
	if _, member := ch.Members["waiter"]; !member {
		t.Fatalf("setup: the waiter should have been promoted; members=%v queue=%v",
			ch.Members, ch.Queue)
	}
	if !hasPred(ch.Predicted, "auth/session.go") {
		t.Errorf("a promoted waiter is a full member; the lane must know what it touches, got %v",
			ch.Predicted)
	}
	if !hasPred(ch.Predicted, "auth/login.go") {
		t.Error("the departed owner's footprint is still part of what this lane covers")
	}
}

func hasPred(fs []PredFile, path string) bool {
	for _, f := range fs {
		if f.Path == path {
			return true
		}
	}
	return false
}

// Membership has TWO doors, and a fix on one is not a fix.
//
// promote() was taught to carry a queued agent's footprint. carryQueue is the
// other door: when a merge's destination has no owner, a source lane's WAITER
// walks straight in as a member. src.Predicted deliberately excludes queued
// agents, so merging the two lanes' footprints does not carry that agent's —
// and it arrived as a full member the lane had no file record of, which is the
// same silent under-matching the promote fix was closing.
func TestAWaiterCarriedInByAMergeBringsItsFootprint(t *testing.T) {
	s, a := chState(t, "owner", "waiter", "dest", "director")
	makeCoordinator(t, s, "director")

	// A destination lane with no owner: anyone carried across walks straight in.
	do(t, s, &Op{
		Kind: OpLaneOpen, Token: a["dest"].Token, Channel: "dst", Text: "destination",
		Predicted: []PredFile{{Path: "dst/main.go", Weight: 1}},
	})
	// A source lane held exclusively, with a waiter queued behind the owner.
	do(t, s, &Op{
		Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "src", Text: "source",
		Exclusive: true, Predicted: []PredFile{{Path: "src/owner.go", Weight: 1}},
	})
	do(t, s, &Op{
		Kind: OpLaneJoin, Token: a["waiter"].Token, Channel: "src",
		Predicted: []PredFile{{Path: "src/waiter.go", Weight: 1}},
	})

	do(t, s, &Op{
		Kind: OpLaneMerge, Token: a["director"].Token,
		Channel: "src", To: "dst", Note: "same work",
	})

	dst := s.Channels["dst"]
	if _, member := dst.Members["waiter"]; !member {
		t.Fatalf("setup: the waiter should have been carried in as a member; members=%v queue=%v",
			dst.Members, dst.Queue)
	}
	if !hasPred(dst.Predicted, "src/waiter.go") {
		t.Errorf("a waiter carried in by a merge is a full member; its files must count, got %v",
			dst.Predicted)
	}
	if !hasPred(dst.Predicted, "src/owner.go") || !hasPred(dst.Predicted, "dst/main.go") {
		t.Errorf("the merge must not lose either lane's own footprint, got %v", dst.Predicted)
	}
}

// A lane must be READABLE by its members, and an announcement must SAY something.
//
// Both halves of this test come from one incident, and neither was found by
// reading the code. A reviewing agent joined a lane, could not see the
// announcement made before it arrived, and messaged a human to ask what the lane
// was about. Investigating that turned up the second fault: the announcements it
// could not see were EMPTY, because the sender had passed the body under the
// wrong key and the missing value became "". Every one returned a serial and a
// must_ack count, so the sending side looked fine while the lane filled with
// obligations that said nothing.
func TestALaneIsReadableAndAnAnnouncementMustSaySomething(t *testing.T) {
	s, a := chState(t, "early", "member", "late", "outsider")

	do(t, s, &Op{Kind: OpLaneOpen, Token: a["early"].Token, Channel: "work", Text: "the refactor"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["member"].Token, Channel: "work"})

	// An announcement with nothing in it obliges every member to acknowledge
	// nothing. The upper bound on a body was checked; the lower one was not.
	//
	// Checked through Admit, not Apply, and that placement is itself the lesson:
	// the rule first went into Apply, which is also the fold that replays the
	// ledger — so a daemon holding announcements that were legal when written
	// refused to replay its own history and would not start. Apply must accept
	// forever whatever it has ever accepted; new restrictions bind at ingress.
	for _, empty := range []string{"", "   ", "\n\t "} {
		op := &Op{Kind: OpLaneAnnounce, Token: a["early"].Token, Channel: "work", Body: empty}
		if err := Admit(op, DefaultLimits()); err == nil {
			t.Errorf("an announcement of %q was admitted — every member would owe an "+
				"acknowledgement for nothing", empty)
		}
		// ...and Apply still takes it, because a ledger may already hold one.
		if _, _, err := s.Apply(op, testNow); err != nil {
			t.Errorf("Apply must still fold %q — refusing it breaks replay of any "+
				"ledger written before the rule existed: %v", empty, err)
		}
	}

	do(t, s, &Op{
		Kind: OpLaneAnnounce, Token: a["early"].Token, Channel: "work",
		Body: "freezing auth/retry.go until Friday",
	})

	// A newcomer joins AFTER the announcement. It must not OWE an ack — that
	// would be a retroactive obligation — but it must be able to SEE it, or a
	// lane's shared context is invisible to everyone who was not already there.
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["late"].Token, Channel: "work"})

	ch := s.Channels["work"]
	late := s.LaneHistory(ch, "late", 0)
	if len(late) == 0 {
		t.Fatal("a newcomer must be able to read what the lane has said; got nothing")
	}
	last := late[len(late)-1]
	if body, _ := last["body"].(string); body != "freezing auth/retry.go until Friday" {
		t.Errorf("the newcomer got the wrong body: %q", body)
	}
	if ack, _ := last["your_ack"].(string); !strings.Contains(ack, "not required") {
		t.Errorf("arriving late must not create an obligation, got %q", ack)
	}

	// The member who WAS present owes it, and the history says so rather than
	// leaving them to work it out.
	member := s.LaneHistory(ch, "member", 0)
	if ack, _ := member[len(member)-1]["your_ack"].(string); !strings.Contains(ack, "OWED") {
		t.Errorf("a member present at announce time still owes it, got %q", ack)
	}
	// And the sender does not acknowledge their own news.
	sender := s.LaneHistory(ch, "early", 0)
	if ack, _ := sender[len(sender)-1]["your_ack"].(string); strings.Contains(ack, "OWED") {
		t.Errorf("nobody acknowledges their own announcement, got %q", ack)
	}

	// Bodies are for the lane, not for anyone who can name it — the same rule
	// the token-less wake path follows.
	if _, err := s.MemberChannel(s.Lanes["outsider"], "work"); err == nil {
		t.Error("a non-member must not be able to read a lane's announcements")
	}
}

// Leaving a lane ends your access to it, and so does being removed from it.
//
// lane_read exists so a member can catch up on what a lane has said. The rule
// that makes it safe is membership at READ time, not membership at some point in
// the past: an agent that left, or one a coordinator evicted, must not keep a
// window into a lane it is no longer part of. Eviction in particular is how a
// human removes an agent that is doing the wrong thing — if it could still read
// the lane afterwards, the removal would be cosmetic.
func TestReadingALaneEndsWhenMembershipDoes(t *testing.T) {
	s, a := chState(t, "owner", "leaver", "removed", "director")
	makeCoordinator(t, s, "director")

	do(t, s, &Op{Kind: OpLaneOpen, Token: a["owner"].Token, Channel: "w", Text: "work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["leaver"].Token, Channel: "w"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["removed"].Token, Channel: "w"})
	do(t, s, &Op{
		Kind: OpLaneAnnounce, Token: a["owner"].Token, Channel: "w",
		Body: "freezing auth/retry.go until Friday",
	})

	// While they are members, both can read it.
	for _, who := range []string{"leaver", "removed"} {
		if _, err := s.MemberChannel(s.Lanes[who], "w"); err != nil {
			t.Fatalf("setup: %s should be able to read the lane it is in: %v", who, err)
		}
	}

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["leaver"].Token, Channel: "w"})
	do(t, s, &Op{Kind: OpLaneEvict, Token: a["director"].Token, Channel: "w", To: "removed"})

	for _, who := range []string{"leaver", "removed"} {
		if _, err := s.MemberChannel(s.Lanes[who], "w"); err == nil {
			t.Errorf("%s can still read a lane it is no longer in — membership is checked "+
				"at read time for exactly this reason", who)
		}
	}
}

// An error must name what a thing IS, not only what it is not.
//
// The wake nudge hands an agent an announcement serial and tells it to go read
// that announcement. A serial in hand makes get_message the obvious call — and
// get_message answered "no accessible message N" for something that plainly
// exists. The only reasonable conclusion from that is that the announcement was
// withdrawn, and a reviewing agent reached exactly that conclusion and messaged
// a human to ask what had happened to it.
//
// So a serial that exists but is the wrong KIND says so, and names the tool that
// reads it.
func TestAWrongKindErrorNamesWhatTheSerialActuallyIs(t *testing.T) {
	err := ErrWrongKind(84, "final-validation")
	for _, want := range []string{"84", "announcement", "final-validation", "lane_read"} {
		if !strings.Contains(err.Error()+err.Hint, want) {
			t.Errorf("the error must mention %q so the reader can act on it: %v (hint: %s)",
				want, err, err.Hint)
		}
	}
	// And it must not read as absence, which is the whole point.
	if strings.Contains(err.Error(), "no accessible") || strings.Contains(err.Error(), "not found") {
		t.Errorf("the thing exists; the error must not say otherwise: %v", err)
	}
}

// Lanes must be able to END, or automatic creation is a slow leak.
//
// Nothing removed a lane except lane_merge. E_LANE_LIMIT said "close a finished
// lane first" — naming an action that did not exist — and the cap of 64 was
// generous only while a human chose every lane. Once a declaration opens one
// automatically, a fleet working through 64 unrelated tasks exhausts the board
// permanently, and every later declaration silently gets nothing.
func TestALaneEndsWhenItsLastMemberLeaves(t *testing.T) {
	s, a := chState(t, "one", "two")

	// Auto, because that is the lane this test is about and the only kind that
	// may be reclaimed. A lane a human opened on purpose outlives its members —
	// see TestALaneSurvivesEveryMemberLeaving, which this used to contradict.
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["one"].Token, Channel: "w", Text: "work", Auto: true})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["two"].Token, Channel: "w"})

	// One member left: still a lane, because somebody is accountable for it.
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["two"].Token, Channel: "w"})
	mustApply(t, s, &Op{Kind: OpSweep}, testNow)
	if _, alive := s.Channels["w"]; !alive {
		t.Fatal("a lane with a member must not be reclaimed")
	}

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["one"].Token, Channel: "w"})
	mustApply(t, s, &Op{Kind: OpSweep}, testNow)
	if _, alive := s.Channels["w"]; alive {
		t.Error("a lane nobody is in and nobody owes anything to must be reclaimed")
	}
}

// ...but an unanswered obligation outlives the population.
//
// An announcement nobody acknowledged is the record that something was never
// answered. Reclaiming the lane under it would delete that record, which is the
// same silent-loss failure the announcement machinery exists to prevent.
func TestALaneWithAnUnansweredAnnouncementIsNotReclaimed(t *testing.T) {
	s, a := chState(t, "one", "two")

	do(t, s, &Op{Kind: OpLaneOpen, Token: a["one"].Token, Channel: "w", Text: "work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["two"].Token, Channel: "w"})
	do(t, s, &Op{
		Kind: OpLaneAnnounce, Token: a["one"].Token, Channel: "w",
		Body: "freezing auth/retry.go until Friday",
	})
	// Everyone leaves without answering it.
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["two"].Token, Channel: "w"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["one"].Token, Channel: "w"})
	mustApply(t, s, &Op{Kind: OpSweep}, testNow)

	if _, alive := s.Channels["w"]; !alive {
		t.Error("an unanswered announcement is a record that must survive its lane")
	}
}

// A reclaimed lane id must carry NOTHING into its successor.
//
// Reclamation deleted the channel and left its announcements behind, keyed by an
// id that no longer existed. LaneHistory selects purely by that id — and lane
// ids are derived from the declaration, so two agents doing the same work at
// different times naturally reuse one. The result: open a lane whose id matches
// a reclaimed one and lane_read hands you the previous lane's announcement
// bodies. Members-only content, to somebody who was never a member, surviving a
// restart.
//
// Neither change was wrong alone. Automatic reclamation created dead ids;
// lane_read gave them a reader. This is the seam between them.
func TestAReclaimedLaneLeavesNothingBehindForTheNextOne(t *testing.T) {
	s, a := chState(t, "first", "stranger")

	// Auto: reused ids are a property of lanes Lanes names from a declaration,
	// which are also the only ones reclamation touches.
	do(t, s, &Op{
		Kind: OpLaneOpen, Token: a["first"].Token, Channel: "reuse",
		Text: "original work", Auto: true,
	})
	res := do(t, s, &Op{
		Kind: OpLaneAnnounce, Token: a["first"].Token, Channel: "reuse",
		Body: "the credentials are in the vault under prod-2026",
	})
	serial, _ := res["serial"].(uint64)
	// Settled: nobody else was a member, so nothing is owed and the lane is
	// eligible for reclamation.
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["first"].Token, Channel: "reuse"})
	mustApply(t, s, &Op{Kind: OpSweep}, testNow)
	if _, alive := s.Channels["reuse"]; alive {
		t.Fatal("setup: the lane should have been reclaimed")
	}
	if _, kept := s.Announcements[serial]; kept {
		t.Error("a reclaimed lane's announcements outlived it, keyed by a dead id")
	}

	// An unrelated agent opens a lane that happens to take the same id.
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["stranger"].Token, Channel: "reuse", Text: "different work"})
	ch := s.Channels["reuse"]
	for _, entry := range s.LaneHistory(ch, "stranger", 0) {
		if body, _ := entry["body"].(string); strings.Contains(body, "prod-2026") {
			t.Errorf("the new lane's occupant can read the old lane's announcement: %q", body)
		}
	}
	if n := len(s.LaneHistory(ch, "stranger", 0)); n != 0 {
		t.Errorf("a freshly opened lane must have no history, got %d entries", n)
	}
}

// Two parts of this file believe opposite things, and only one of them runs.
//
// TestALaneSurvivesEveryMemberLeaving says an empty lane must persist, because
// standing lanes — "release", "security review" — are the point: agents drop in
// and out and must find the same lane with its history. reclaimFinishedLanes
// says "the last member leaving is what ends a lane" and deletes exactly those
// lanes. The first test passes only because it never sweeps.
//
// So this sweeps. If the lane is gone afterwards, the standing lane the other
// test protects survives only until the next sweep tick, which is minutes — and
// the agent that comes back finds a stranger's fresh lane under the same name,
// with none of the history it was promised.
func TestAStandingLaneSurvivesTheSweepThatReclaimsFinishedOnes(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	do(t, s, &Op{
		Kind: OpLaneOpen, Token: a["alpha"].Token, Channel: "release",
		Text: "the standing release lane",
	})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["beta"].Token, Channel: "release"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["alpha"].Token, Channel: "release"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["beta"].Token, Channel: "release"})

	if _, _, err := s.Apply(&Op{Kind: OpSweep}, testNow); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	ch := s.Channels["release"]
	if ch == nil {
		t.Fatal("the standing lane was reclaimed by the sweep; an agent returning to it " +
			"finds nothing, which is what TestALaneSurvivesEveryMemberLeaving forbids")
	}
	if ch.Topic != "the standing release lane" {
		t.Errorf("the lane's identity did not survive the sweep: %q", ch.Topic)
	}
}

// A coordinator can retire a finished lane, because until now nobody could.
//
// Auto-opened lanes end themselves once the last member leaves. A lane a human
// opened does NOT, deliberately — outliving its members is what a standing lane
// is for — so a board accumulated finished lanes permanently, and E_LANE_LIMIT
// advised "lane_leave the ones you are done with", which does nothing at all for
// exactly these. Naming a corrective action that does not work is this
// codebase's most persistent failure mode; this was another instance.
func TestACoordinatorCanCloseAFinishedLane(t *testing.T) {
	s, a := chState(t, "boss", "worker", "stranger")
	a["boss"].Role = RoleCoordinator
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["worker"].Token, Channel: "standing", Text: "work"})

	// Occupied: refused, and told what to do instead. Closing is tidying, not
	// eviction — a lane with agents in it is somebody's working context.
	err := mustFail(t, s, &Op{Kind: OpLaneClose, Token: a["boss"].Token, Channel: "standing"})
	if !strings.Contains(err.Error(), "member") {
		t.Errorf("closing an occupied lane failed for the wrong reason: %v", err)
	}
	if _, alive := s.Channels["standing"]; !alive {
		t.Fatal("a refused close removed the lane anyway")
	}

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["worker"].Token, Channel: "standing"})
	mustApply(t, s, &Op{Kind: OpSweep}, testNow)
	if _, alive := s.Channels["standing"]; !alive {
		t.Fatal("a human-opened lane was auto-reclaimed; this test needs one that persists")
	}

	// A STRANGER — neither coordinator nor the agent that opened it — is refused.
	// The role is human-granted and no agent can promote itself into it.
	if err := mustFail(t, s, &Op{
		Kind: OpLaneClose, Token: a["stranger"].Token, Channel: "standing",
	}); !strings.Contains(err.Error(), "coordinator") {
		t.Errorf("a stranger closed a lane, or failed for the wrong reason: %v", err)
	}

	res := mustApply(t, s, &Op{
		Kind: OpLaneClose, Token: a["boss"].Token, Channel: "standing", Note: "finished",
	}, testNow)
	if res["closed"] != true {
		t.Errorf("close result = %v, want closed:true", res)
	}
	if _, alive := s.Channels["standing"]; alive {
		t.Error("the lane survived being closed by a coordinator")
	}
}

// The agent that opened a lane may retire it without the coordinator role.
//
// lane_open is unprivileged and advertised, so an agent could create a lane it
// could never end — and the refusal it got described its own lane as "another
// agent's". Telling somebody they may not touch their own thing, in words about
// somebody else's, is worse than the missing power was.
func TestTheAgentThatOpenedALaneCanCloseIt(t *testing.T) {
	s, a := chState(t, "opener", "stranger")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["opener"].Token, Channel: "mine", Text: "my work"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["opener"].Token, Channel: "mine"})
	if _, alive := s.Channels["mine"]; !alive {
		t.Fatal("the lane was reclaimed; this test needs one that persists while empty")
	}

	// A stranger still cannot, so the exemption is about ownership rather than
	// having quietly removed the gate.
	if err := mustFail(t, s, &Op{
		Kind: OpLaneClose, Token: a["stranger"].Token, Channel: "mine",
	}); !strings.Contains(err.Error(), "coordinator") {
		t.Errorf("a stranger closed somebody else's lane: %v", err)
	}

	res := mustApply(t, s, &Op{Kind: OpLaneClose, Token: a["opener"].Token, Channel: "mine"}, testNow)
	if res["closed"] != true {
		t.Errorf("the opener could not close its own lane: %v", res)
	}
	if _, alive := s.Channels["mine"]; alive {
		t.Error("the lane survived its opener closing it")
	}
}

// ...but ownership is not a bypass of the other two guards. An opener may not
// close a lane somebody is standing in, any more than a coordinator may.
func TestAnOpenerStillCannotCloseAnOccupiedLane(t *testing.T) {
	s, a := chState(t, "opener", "peer")
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["opener"].Token, Channel: "busy", Text: "work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["peer"].Token, Channel: "busy"})
	if err := mustFail(t, s, &Op{
		Kind: OpLaneClose, Token: a["opener"].Token, Channel: "busy",
	}); !strings.Contains(err.Error(), "member") {
		t.Errorf("an opener emptied an occupied lane, or failed for the wrong reason: %v", err)
	}
}

// An unacknowledged announcement outlives the lane's population, and closing
// over one would hide it rather than settle it — the board renders
// announcements THROUGH their lane.
func TestClosingWillNotHideAnUnansweredAnnouncement(t *testing.T) {
	s, a := chState(t, "boss", "worker")
	a["boss"].Role = RoleCoordinator
	do(t, s, &Op{Kind: OpLaneOpen, Token: a["worker"].Token, Channel: "standing", Text: "work"})
	do(t, s, &Op{Kind: OpLaneJoin, Token: a["boss"].Token, Channel: "standing"})
	do(t, s, &Op{Kind: OpLaneAnnounce, Token: a["worker"].Token, Channel: "standing", Body: "FREEZE"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["worker"].Token, Channel: "standing"})
	do(t, s, &Op{Kind: OpLaneLeave, Token: a["boss"].Token, Channel: "standing"})

	err := mustFail(t, s, &Op{Kind: OpLaneClose, Token: a["boss"].Token, Channel: "standing"})
	if !strings.Contains(err.Error(), "acknowledged") {
		t.Errorf("closing over an unanswered announcement failed for the wrong reason: %v", err)
	}
	if _, alive := s.Channels["standing"]; !alive {
		t.Error("the lane was closed despite an unanswered announcement")
	}
}
