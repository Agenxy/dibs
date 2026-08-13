package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// theSpace is the agent these tests coordinate in; its name carries no meaning
// beyond being something an agent could plausibly have written.
const theSpace = "auth-work"

// openSpaceWith gets an agent into an agent and returns the key it was issued.
func openSpaceWith(t *testing.T, s *State, token string) string {
	t.Helper()
	res := mustApply(t, s, &Op{Kind: OpSpaceOpen, Token: token, Space: theSpace, Text: "the work"}, testNow)
	key, _ := res["key"].(string)
	if key == "" {
		t.Fatal("opening an agent issued no coordination key")
	}
	return key
}

// The mechanism is worth nothing unless the agent is actually handed the key.
// It was "decorative" precisely because the exact-match path existed and nothing
// ever reached it.
func TestOpeningASpaceIssuesAKeyToItsOpener(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	key := openSpaceWith(t, s, a["alpha"].Token)

	if !strings.HasPrefix(key, "key:") {
		t.Errorf("key %q does not use the key: namespace", key)
	}
	if !identifyingRef(key) {
		t.Error("a coordination key must count as identity, or the exact path is unreachable")
	}
	// Opaque: an agent that knows the agent's name, topic, or its own id must not
	// be able to reconstruct the key from them.
	for _, guessable := range []string{"auth-work", "the work", a["alpha"].ID} {
		if strings.Contains(key, guessable) {
			t.Errorf("key %q leaks %q: a guessable key is a forgeable one", key, guessable)
		}
	}
}

// The whole security of the mechanism. An agent that has SEEN a key: from a
// message, a log, the board panel: must not be able to declare it and be
// treated as having coordinated. Issued is not enough; it must be held.
func TestAKeyYouDoNotHoldIsStruckOut(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	key := openSpaceWith(t, s, a["alpha"].Token)

	if !s.holdsCoordKey(a["alpha"].ID, key) {
		t.Fatal("the opener does not hold the key it was issued")
	}
	if s.holdsCoordKey(a["beta"].ID, key) {
		t.Error("an agent that never joined holds the key")
	}
	// B copies the key into its declaration anyway.
	got := s.validatedRefs(a["beta"].ID, []string{"pr:1231", key, "goal:green"})
	for _, r := range got {
		if r == key {
			t.Fatal("a forged coordination key survived validation")
		}
	}
	// And it is REMOVED, not demoted: left in as a label it would still supply
	// shared-vocabulary evidence, which is the same laundering in smaller print.
	if len(got) != 2 || got[0] != "pr:1231" || got[1] != "goal:green" {
		t.Errorf("validatedRefs mangled the honest refs: %v", got)
	}
	// A claim about the world is not Dibs' to verify, and must pass untouched.
	if r := s.validatedRefs(a["beta"].ID, []string{"pr:1231"}); len(r) != 1 {
		t.Errorf("an ordinary ref was filtered: %v", r)
	}

	// A key of the right SHAPE that was never issued at all. Shape is the only
	// thing an attacker controls for free: the namespace and twenty hex
	// characters are trivial to type, so "looks like a key" must never be a step
	// towards being treated as one, even for the agent that opened the agent.
	invented := coordKeyNS + ":00000000000000000000"
	for _, who := range []string{a["alpha"].ID, a["beta"].ID, ""} {
		if s.holdsCoordKey(who, invented) {
			t.Errorf("%q holds a key this board never issued", who)
		}
	}
	if r := s.validatedRefs(a["alpha"].ID, []string{invented}); len(r) != 0 {
		t.Errorf("an invented key survived validation: %v", r)
	}
	// And the opener's real key is still held, so the check above is not passing
	// by rejecting everything.
	if !s.holdsCoordKey(a["alpha"].ID, key) {
		t.Fatal("the real key stopped validating; the invented-key check proves nothing")
	}
}

// Joining is one of the three ways a key is legitimately acquired, so the
// joiner must both receive it and pass validation with it.
func TestJoiningASpaceGrantsItsKey(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	key := openSpaceWith(t, s, a["alpha"].Token)

	res := mustApply(t, s, &Op{Kind: OpSpaceJoin, Token: a["beta"].Token, Space: "auth-work"}, testNow)
	if got, _ := res["key"].(string); got != key {
		t.Errorf("join returned key %q, want the agent's own %q", got, key)
	}
	if !s.holdsCoordKey(a["beta"].ID, key) {
		t.Error("a member does not hold its agent's key")
	}
	// And leaving gives it up: coordination that ended is not coordination.
	mustApply(t, s, &Op{Kind: OpSpaceLeave, Token: a["beta"].Token, Space: "auth-work"}, testNow)
	if s.holdsCoordKey(a["beta"].ID, key) {
		t.Error("an agent that left the agent still holds its key")
	}
}

// Two boards must not issue the same key, and one board must not issue it
// twice. "globally unambiguous on that board" is the property that lets a key
// stand in for identity at all.
func TestKeysAreUniquePerSpaceAndPerBoard(t *testing.T) {
	seen := map[string]bool{}
	for _, node := range []string{"node-a", "node-b"} {
		for serial := uint64(1); serial <= 50; serial++ {
			k := coordKey(node, serial)
			if seen[k] {
				t.Fatalf("duplicate key %q at %s/%d", k, node, serial)
			}
			seen[k] = true
		}
	}
	// Pinned to a literal, which is stronger than asserting determinism against
	// itself: the derivation is part of the ON-DISK contract. Change the hash,
	// the truncation, or the separator and every key in an existing ledger
	// replays to a different value: agents silently stop recognising the keys
	// their own members are declaring. If this line must change, it is a
	// migration, not a refactor.
	if got := coordKey("node-a", 7); got != "key:8c2ded975dade5962f5c" {
		t.Fatalf("coordKey derivation changed: %q: existing ledgers replay to different keys", got)
	}
}

// The payoff, end to end: a HELD key produces the exact relation, and the same
// string in the hands of an agent that never coordinated produces nothing.
//
// Both halves have to be one test. Exactness alone is the feature working, and
// exactness alone is also what a laundered guess looks like: the difference
// between them IS the mechanism, so measuring either by itself measures nothing.
func TestAHeldKeyMatchesExactlyAndAForgedOneDoesNot(t *testing.T) {
	s, a := chState(t, "alpha", "beta", "mallory")
	key := openSpaceWith(t, s, a["alpha"].Token)
	mustApply(t, s, &Op{Kind: OpSpaceJoin, Token: a["beta"].Token, Space: "auth-work"}, testNow)

	// Both members are in one repository and both declare the key. Repo identity
	// has to be known or Classify will not let ANY identifier act.
	const cwd = "/repo"
	for _, n := range []string{"alpha", "beta", "mallory"} {
		a[n].Agent = &AgentInfo{CWD: cwd}
	}
	mustApply(t, s, &Op{
		Kind: OpSetSlot, Token: a["alpha"].Token,
		Text: "rotating the refresh token", Refs: []string{key},
	}, testNow)
	ch := s.Spaces["auth-work"]
	ch.Predicted = mergePredicted(nil, fp("auth/token.go"))

	mine := Slot{Text: "the same work, described differently", Refs: []string{key}}
	got := s.MatchAgentsEvidence(a["beta"].ID, mine, cwd, cwd, nil, nil, 5)
	if len(got) == 0 {
		t.Fatal("two agents holding one coordination key did not match at all")
	}
	if got[0].Relation != RelationSameItem {
		t.Errorf("held key gave relation %v, want %v: the exact path is still unreachable",
			got[0].Relation, RelationSameItem)
	}
	if len(got[0].Evidence.Identity) == 0 {
		t.Error("the key is not reported as the identity the decision rested on")
	}

	// Mallory declares the identical string, having coordinated with nobody.
	// The agent may still surface on other evidence: that is discovery doing its
	// job, but never as the same work item, and never citing the key.
	forged := s.MatchAgentsEvidence(a["mallory"].ID,
		Slot{Text: "unrelated work", Refs: []string{key}}, cwd, cwd, nil, nil, 5)
	for _, m := range forged {
		if m.Relation == RelationSameItem {
			t.Error("a forged key produced an exact match: the key laundered a guess")
		}
		for _, id := range m.Evidence.Identity {
			if id == key {
				t.Errorf("a key mallory never held is cited as identity: %v", m.Evidence.Identity)
			}
		}
		for _, r := range m.SharedRefs {
			if r == key {
				t.Errorf("a forged key is reported as shared: %v", m.SharedRefs)
			}
		}
	}
}

// The case the key actually exists for, and the one a live probe proved was
// missing when holding meant bare membership.
//
// Matching never proposes an agent you are already in: correctly, since you are
// there. So a key only members could hold would fire precisely where it changed
// nothing, which is what "the join path is decorative" meant. The path that
// matters is delegation: a parent opens an agent, fans out subagents, and each
// child declares its OWN work while belonging to no agent at all. One
// coordination decision, made once, covering every agent it produced.
func TestAVouchedChildHoldsItsParentsKeyWithoutJoiningAnything(t *testing.T) {
	s, a := chState(t, "parent", "stranger")
	key := openSpaceWith(t, s, a["parent"].Token)

	child := spawnChild(t, s, a["parent"].Token, a["parent"].ID, "n-1")
	childID, _ := child["agent_id"].(string)
	if childID == "" {
		t.Fatal("no child agent")
	}
	if _, member := s.Spaces["auth-work"].Members[childID]; member {
		t.Fatal("the child joined the agent; this test is then about nothing")
	}
	if !s.holdsCoordKey(childID, key) {
		t.Fatal("a vouched child does not hold its parent's key: delegation carries nothing")
	}

	// An UNVOUCHED claim of the same parent inherits nothing. Lineage is proven
	// by burning a one-time secret the parent issued; naming a parent is a claim
	// anybody could make about anybody.
	liar := do(t, s, &Op{
		Kind: OpRegister, Name: "liar", NewToken: "tok-liar",
		Parent: a["parent"].ID,
	}) // no ParentNonce
	liarID, _ := liar["agent_id"].(string)
	if s.holdsCoordKey(liarID, key) {
		t.Error("an unvouched agent inherited a key by merely naming a parent")
	}
	if s.holdsCoordKey(a["stranger"].ID, key) {
		t.Error("an unrelated agent holds the key")
	}
}

// And end to end: the child's declaration is matched to the parent's agent
// exactly, on the key rather than on any resemblance between what they wrote.
// The wording is deliberately unlike the parent's, so a semantic match cannot
// be what produces the result.
func TestAChildsWorkMatchesItsParentsSpaceOnTheKeyAlone(t *testing.T) {
	s, a := chState(t, "parent")
	key := openSpaceWith(t, s, a["parent"].Token)
	const cwd = "/repo"
	a["parent"].Agent = &AgentInfo{CWD: cwd}

	mustApply(t, s, &Op{
		Kind: OpSetSlot, Token: a["parent"].Token,
		Text: "rotating the refresh token", Refs: []string{key},
	}, testNow)
	s.Spaces["auth-work"].Predicted = mergePredicted(nil, fp("auth/token.go"))

	child := spawnChild(t, s, a["parent"].Token, a["parent"].ID, "n-1")
	childID, _ := child["agent_id"].(string)
	s.Agents[childID].Agent = &AgentInfo{CWD: cwd}

	got := s.MatchAgentsEvidence(childID,
		Slot{Text: "writing the migration notes for widgets", Refs: []string{key}},
		cwd, cwd, nil, nil, 5)
	var found *AgentMatch
	for i := range got {
		if got[i].Agent == "auth-work" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatal("the child's work did not reach its parent's agent at all")
	}
	if found.Relation != RelationSameItem {
		t.Errorf("relation %v, want %v: the key did not carry the decision",
			found.Relation, RelationSameItem)
	}
	if len(found.SharedIDs) == 0 {
		t.Error("the key is not reported as the shared identifier it matched on")
	}
}

// The key must reach the agents it was issued to and nobody else.
//
// It is checked on use, so a leaked key is not immediately a forged
// coordination: holdsCoordKey still asks whether the declarer is entitled to
// it. But the board is read by every agent on the machine, and a key visible
// there is a key any of them can copy into `refs` the moment membership shifts,
// or that a subscriber can hold without ever having joined. The mechanism's
// value is that possession means something; broadcasting it makes possession
// mean nothing.
//
// Asserted against the whole serialized board rather than a field list, because
// the failure this guards against is somebody ADDING a field: an allowlist
// checked field by field would be updated in the same edit that broke it.
func TestTheBoardNeverShowsACoordinationKey(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	key := openSpaceWith(t, s, a["alpha"].Token)
	mustApply(t, s, &Op{Kind: OpSpaceJoin, Token: a["beta"].Token, Space: theSpace}, testNow)
	mustApply(t, s, &Op{
		Kind: OpSpaceAnnounce, Token: a["alpha"].Token, Space: theSpace,
		Body: "something worth acknowledging",
	}, testNow)

	blob, err := json.Marshal(s.Board())
	if err != nil {
		t.Fatalf("board does not serialize: %v", err)
	}
	if strings.Contains(string(blob), key) {
		t.Errorf("the board exposes the coordination key %q to every agent that reads it", key)
	}
	// Guard the guard: if the key stopped being derivable, or the agent never
	// opened, the search above would pass by finding nothing at all.
	if !strings.Contains(string(blob), theSpace) {
		t.Fatal("the agent is not on the board; this check would then be vacuous")
	}
	if !strings.HasPrefix(key, coordKeyNS+":") {
		t.Fatalf("no key was issued to search for: %q", key)
	}
}

// An AUTOMATIC join grants the key too, not just an explicit one.
//
// Two routes reach membership: asking with join_space, and being matched by
// declare, and only asking returned the key. That left the agent which got
// there by BEING GUESSED AT as the one with no way to stop being guessed at: the
// key is precisely what it would declare in `refs` next time to be matched by
// identity rather than by wording. Its only recovery was calling join_space on a
// agent it was already in, which hands back the key it should already have had,
// and nothing told it to.
//
// Asserted here rather than in the space e2e because an automatic join needs a
// specific board state (a shared identifying ref and no stronger match) and in
// a suite with accumulated agents the second agent matched something else
// entirely. A test that has to win a scoring contest to reach its assertion is
// not testing what it claims; this constructs the state directly.
func TestAnAutomaticJoinGrantsTheKeyAsWell(t *testing.T) {
	s, a := chState(t, "alpha", "beta")
	key := openSpaceWith(t, s, a["alpha"].Token)

	// Auto is what declare's matcher sets when it joins on its own initiative.
	res := mustApply(t, s, &Op{
		Kind: OpSpaceJoin, Token: a["beta"].Token, Space: "auth-work",
		Auto: true, Score: 0.9, Threshold: 0.33, ScorerID: "test",
		Evidence: []string{"issue:4242"},
	}, testNow)

	if joined, _ := res["joined"].(bool); !joined {
		t.Fatalf("the automatic join did not join: %v", res)
	}
	got, _ := res["key"].(string)
	if got != key {
		t.Errorf("an automatic join returned key %q, want the agent's own %q: the agent "+
			"is a member of an agent it cannot name exactly, which is the one thing the "+
			"key exists to fix", got, key)
	}
	if !s.holdsCoordKey(a["beta"].ID, key) {
		t.Error("an automatically joined member does not hold its agent's key")
	}
}
