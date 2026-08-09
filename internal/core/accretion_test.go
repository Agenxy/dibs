package core

import (
	"testing"
	"time"
)

// A lane must not become an easier target as it absorbs members.
//
// mergePredicted is monotonic: every join unions that member's predicted files
// into the lane's, at max weight, and nothing removes them — leaving does not
// shrink it either. So what a newcomer was compared against was not "the work
// this lane is doing" but "everything anyone in it has ever been predicted to
// touch", which is strictly easier to hit the longer the lane lives. A lane that
// matched more gained members, and gained surface by gaining them.
//
// Measured before the fix: the same unrelated newcomer scored 0.0000 against a
// one-member lane and 0.1000 against the same lane with five — crossing a real
// fleet's 0.064 join bar with no change to its work and no change to the lane's
// topic. Only the membership changed.
//
// The union still FINDS candidates, which is what breadth is good for. It no
// longer judges them: the score comes from the closest single live declaration,
// which is the thing an agent can actually be duplicating.
func TestALaneDoesNotGetEasierToMatchAsItGrows(t *testing.T) {
	newcomer := Slot{
		Text: "auth token refresh", Dirs: []string{"/repo/auth"},
		Predicted: fp("auth/token.go", "auth/session.go"),
	}

	score := func(extraMembers int) float64 {
		s := NewState("n1", DefaultLimits())
		now := time.Now()
		ch := &Channel{
			ID: "lane", Topic: "docs", Members: map[string]*Membership{},
		}
		// The founding member is doing docs — nothing to do with the newcomer.
		addMember(t, s, ch, "a", Slot{
			Text: "the documentation site", Dirs: []string{"/repo/docs"},
			Predicted: fp("docs/index.md", "docs/guide.md"),
		}, now)
		// More members, NONE of which individually overlaps the newcomer. That is
		// the whole point: accretion is the UNION matching where no single member
		// does. A member that genuinely declares the newcomer's file is a true
		// positive that must still be found, which is TestTheRightMemberIsStillFound
		// — an earlier version of this fixture confused the two by handing one
		// extra member the newcomer's own file, and then read the true positive as
		// a regression.
		extras := []Slot{
			{Text: "web router", Dirs: []string{"/repo/web"}, Predicted: fp("web/app.ts", "web/router.ts")},
			{Text: "release tooling", Dirs: []string{"/repo/build"}, Predicted: fp("build/release.go", "build/sign.go")},
			{Text: "changelog", Dirs: []string{"/repo/changelog"}, Predicted: fp("changelog/v2.md")},
			{Text: "cli flags", Dirs: []string{"/repo/cli"}, Predicted: fp("cli/main.go", "cli/flags.go")},
		}
		for i := 0; i < extraMembers && i < len(extras); i++ {
			addMember(t, s, ch, string(rune('b'+i)), extras[i], now)
		}
		// The union carries a file no CURRENT member declares — exactly how a lane
		// accretes: somebody was predicted to touch it once and merging never
		// forgets. This is what the newcomer used to be compared against.
		ch.Predicted = mergePredicted(ch.Predicted, fp("auth/token.go"))
		s.Channels = map[string]*Channel{"lane": ch}
		got := s.MatchLanesEvidence("newcomer", newcomer, "/repo", "/repo", nil, nil, 5)
		if len(got) == 0 {
			return 0
		}
		return got[0].Score
	}

	small, big := score(0), score(4)
	t.Logf("the SAME unrelated newcomer, against the SAME lane:")
	t.Logf("   1 member   score %.4f", small)
	t.Logf("   5 members  score %.4f", big)

	// The lane grew. The newcomer's work did not change and neither did the
	// lane's topic, so growth must not be what moved the number.
	if big > small {
		t.Errorf("accretion raised an unrelated agent's score from %.4f to %.4f — a lane "+
			"that has absorbed enough work matches everyone", small, big)
	}
	const fleetBar = 0.064
	if big >= fleetBar {
		t.Errorf("an unrelated newcomer clears the %.3f join bar (%.4f) because the lane "+
			"has five members", fleetBar, big)
	}
}

// A member genuinely doing the newcomer's work must still be found, or the fix
// has bought precision by going blind.
func TestTheRightMemberIsStillFound(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Now()
	ch := &Channel{ID: "lane", Topic: "mixed", Members: map[string]*Membership{}}
	addMember(t, s, ch, "a", Slot{
		Text: "docs", Dirs: []string{"/repo/docs"},
		Predicted: fp("docs/index.md"),
	}, now)
	addMember(t, s, ch, "b", Slot{
		Text: "auth token refresh", Dirs: []string{"/repo/auth"},
		Predicted: fp("auth/token.go", "auth/session.go"),
	}, now)
	s.Channels = map[string]*Channel{"lane": ch}

	got := s.MatchLanesEvidence("newcomer", Slot{
		Text: "token refresh work", Dirs: []string{"/repo/auth"},
		Predicted: fp("auth/token.go", "auth/session.go"),
	}, "/repo", "/repo", nil, nil, 5)
	if len(got) == 0 {
		t.Fatal("the lane holds a member doing exactly this work and was not surfaced")
	}
	if got[0].Relation != RelationSameSurface {
		t.Errorf("relation = %q, want same_surface — both declared /repo/auth", got[0].Relation)
	}
	if got[0].Score < 0.5 {
		t.Errorf("score %.3f: a near-identical declaration must score high, and the "+
			"strongest member must be the one reported", got[0].Score)
	}
}

// addMember registers a lane, gives it a declaration, and puts it in the channel
// — the shape production actually has.
//
// The first version of this test built channels with a merged footprint and NO
// member lanes, so the slot-to-slot path it existed to exercise was never
// reached, and it kept reporting the pre-fix number afterwards. A fixture that
// cannot reach the code under test measures the fixture.
func addMember(t *testing.T, s *State, ch *Channel, id string, sl Slot, now time.Time) {
	t.Helper()
	if _, _, err := s.Apply(&Op{
		Kind: OpRegisterLane, Name: id, NewToken: "tok-" + id,
		Agent: &AgentInfo{CWD: "/repo"},
	}, now); err != nil {
		t.Fatal(err)
	}
	sl.ID = "s1"
	s.Lanes[id].Slots = map[string]Slot{"s1": sl}
	ch.Members[id] = &Membership{}
	ch.Predicted = mergePredicted(ch.Predicted, sl.Predicted)
}

// A lane whose members have declared nothing is not a lane nobody resembles.
//
// Judging a candidate on the closest live member declaration is right, and it
// silently assumed a member declaration exists. Open a lane and start work
// before calling set_slot — the ordinary order, and what the channel e2e does —
// and there is no member slot to compare against. Scoring that zero does not
// express doubt about the match; it deletes the lane from every future match
// permanently, and nothing anywhere reports a fault.
//
// So the two cases must stay apart: compared-and-unalike is a zero, never-
// measured falls back to the only footprint that exists.
func TestALaneWithNoMemberDeclarationIsStillFindable(t *testing.T) {
	s, a := chState(t, "opener", "newcomer")
	do(t, s, &Op{
		Kind: OpLaneOpen, Token: a["opener"].Token, Channel: "guard-work",
		Text:      "claim guard denies edits to claimed paths",
		Predicted: fp("internal/core/claims.go", "internal/core/guard.go"),
	})
	// The opener never declares a slot. That is the whole scenario.
	if len(s.Lanes[a["opener"].ID].Slots) != 0 {
		t.Fatal("the opener declared a slot; this test is then about nothing")
	}

	mine := Slot{
		Text:      "guard path enforcement for exclusive claims",
		Predicted: fp("internal/core/claims.go", "internal/core/guard.go"),
	}
	got := s.MatchLanesEvidence(a["newcomer"].ID, mine, "", "", nil, nil, 5)
	if len(got) == 0 {
		t.Fatal("a lane opened for work nobody has declared yet is invisible forever")
	}
	if got[0].Score <= 0 {
		t.Errorf("score = %v; an unmeasured lane must fall back to what it does have", got[0].Score)
	}

	// And the accretion rule still binds where there IS something to judge: a
	// member declaration that does not resemble the newcomer scores zero on its
	// own merits, however much footprint the lane has accumulated.
	ch := s.Channels["guard-work"]
	ch.Predicted = mergePredicted(ch.Predicted, fp("totally/unrelated.go"))
	l := s.Lanes[a["opener"].ID]
	l.Slots = map[string]Slot{"s1": {
		Text:      "something else entirely",
		Predicted: fp("totally/unrelated.go"),
	}}
	got = s.MatchLanesEvidence(a["newcomer"].ID, mine, "", "", nil, nil, 5)
	for _, m := range got {
		if m.Lane == "guard-work" && m.Score > 0 {
			t.Errorf("a measured, unalike member still scored %v off the lane's union", m.Score)
		}
	}
}

// A lane nobody is in must not be offered as somebody else's work.
//
// Found on a live board, not in a suite: two lanes whose members had all been
// swept were still being suggested, with `members=0` printed in the suggestion
// itself. The wording a match carries — "another lane is already pursuing the
// same objective", "join it to coordinate" — is false about an empty lane, and
// following it sends an agent to an empty room.
//
// Empty lanes are not a transient state to wait out: a lane a human opened
// outlives its members on purpose, and only auto-opened ones are ever reclaimed.
// So they stay joinable by name; they just stop claiming to be occupied.
func TestAnEmptyLaneIsNotSomebodyElsesWork(t *testing.T) {
	s, a := chState(t, "opener", "newcomer")
	do(t, s, &Op{
		Kind: OpLaneOpen, Token: a["opener"].Token, Channel: "abandoned",
		Text: "rotating the refresh token", Predicted: fp("auth/token.go"),
	})
	mine := Slot{Text: "rotating the refresh token", Predicted: fp("auth/token.go")}

	// While somebody is in it, it is a real match — otherwise this test could
	// pass by breaking matching altogether.
	if got := s.MatchLanesEvidence(a["newcomer"].ID, mine, "", "", nil, nil, 5); len(got) == 0 {
		t.Fatal("an occupied lane doing identical work did not match; the check below proves nothing")
	}

	do(t, s, &Op{Kind: OpLaneLeave, Token: a["opener"].Token, Channel: "abandoned"})
	if _, alive := s.Channels["abandoned"]; !alive {
		t.Fatal("the lane was reclaimed; this test needs one that persists while empty")
	}
	for _, m := range s.MatchLanesEvidence(a["newcomer"].ID, mine, "", "", nil, nil, 5) {
		if m.Lane == "abandoned" {
			t.Errorf("an empty lane was offered as a match (members=%d, score=%v)",
				m.Members, m.Score)
		}
	}

	// A queue is occupancy: an agent waiting on an exclusive lane has not got in
	// yet but is certainly working on its subject.
	ch := s.Channels["abandoned"]
	ch.Queue = []string{a["opener"].ID}
	found := false
	for _, m := range s.MatchLanesEvidence(a["newcomer"].ID, mine, "", "", nil, nil, 5) {
		if m.Lane == "abandoned" {
			found = true
		}
	}
	if !found {
		t.Error("a lane with somebody queued for it was hidden; that agent is working on this")
	}
}
