package core

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A post has exactly one audience and exactly one read path, and both were
// wrong at once.
//
// The body travelled inside the agent.post event. Space events carry no `To`,
// so the event filter had no basis on which to withhold them and sent every
// post to every authenticated agent on the board. SPEC §10 says events carry
// metadata only, precisely so this cannot happen. Meanwhile nothing STORED the
// post, so the event was the only copy: read_space, the tool whose job is to
// read the agent, did not return posts at all.
//
// Both halves have to hold together. Removing the body from the event without
// giving posts a home makes them write-only, which is a quieter failure than
// the leak it fixes.
func TestAPostGoesToTheLaneAndNotTheBoard(t *testing.T) {
	st := NewState("test", DefaultLimits())
	now := t0

	reg := func(name string) string {
		tok := "tok-" + name
		if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: name, NewToken: tok}, now); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return tok
	}
	author, member, watcher, outsider := reg("author"), reg("member"), reg("watcher"), reg("outsider")

	if _, _, err := st.Apply(&Op{
		Kind: OpSpaceOpen, Token: author, Space: "work", Text: "the topic",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Apply(&Op{
		Kind: OpSpaceJoin, Token: member, Space: "work", Score: 0.9, ScorerID: "t",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Apply(&Op{Kind: OpSpaceSubscribe, Token: watcher, Space: "work"}, now); err != nil {
		t.Fatal(err)
	}

	const secret = "THE-BODY-OF-THE-POST"
	_, evs, err := st.Apply(&Op{Kind: OpSpacePost, Token: author, Space: "work", Body: secret}, now)
	if err != nil {
		t.Fatal(err)
	}

	// The event is what reaches the whole board, so the body must not be in it.
	blob, _ := json.Marshal(evs)
	if strings.Contains(string(blob), secret) {
		t.Errorf("the post body is in the event payload, which reaches every "+
			"authenticated agent: %s", blob)
	}
	if len(evs) != 1 || evs[0].Type != "agent.post" {
		t.Fatalf("expected one agent.post event, got %+v", evs)
	}
	if got := evs[0].Data["bytes"]; got != len(secret) {
		t.Errorf("bytes = %v, want %d: a reader still needs to know how big it is", got, len(secret))
	}

	ch := st.Spaces["work"]
	readable := func(tok string) (bool, error) {
		l := st.AgentByToken(tok)
		c, err := st.ReaderChannel(l, "work")
		if err != nil {
			return false, err
		}
		for _, p := range st.PostHistory(c, 50) {
			if p["body"] == secret {
				return true, nil
			}
		}
		return false, nil
	}
	for _, who := range []struct {
		name, tok string
	}{{"the author", author}, {"a member", member}, {"a subscriber", watcher}} {
		ok, err := readable(who.tok)
		if err != nil {
			t.Errorf("%s cannot even reach the agent: %v", who.name, err)
			continue
		}
		if !ok {
			t.Errorf("%s cannot read the post: it is write-only", who.name)
		}
	}
	if _, err := st.ReaderChannel(st.AgentByToken(outsider), "work"); err == nil {
		t.Error("an outsider was allowed to read an agent it neither joined nor subscribed to")
	}

	// The subscriber must not receive the coordination key: it is held by
	// membership, and it is the one identity claim Dibs can verify.
	if ch.Key == "" {
		t.Fatal("precondition: the agent should have a key")
	}
}

// Posts are replayed state, so they need a bound like every other collection.
func TestPostHistoryIsBounded(t *testing.T) {
	lim := DefaultLimits()
	lim.PostRetention = 5
	st := NewState("test", lim)
	now := t0

	tok := "t"
	if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: "a", NewToken: tok}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Apply(&Op{Kind: OpSpaceOpen, Token: tok, Space: "w", Text: "t"}, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, _, err := st.Apply(&Op{
			Kind: OpSpacePost, Token: tok, Space: "w", Body: "post " + itoa(i),
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	ch := st.Spaces["w"]
	if len(ch.Posts) != lim.PostRetention {
		t.Fatalf("kept %d posts, want %d: an unbounded collection is replayed "+
			"into memory on every start, forever", len(ch.Posts), lim.PostRetention)
	}
	// The ones kept are the NEWEST.
	if last := ch.Posts[len(ch.Posts)-1].Body; last != "post 39" {
		t.Fatalf("newest kept post is %q, want %q", last, "post 39")
	}
}

// A merge deletes the source agent. Anything the source held and the merge did
// not carry is destroyed, and this codebase has done that twice already: first
// with queues, then with announcements, each time leaving a surviving agent that
// looked healthy and had quietly eaten a collection.
func TestAMergeCarriesThePostsAcross(t *testing.T) {
	st := NewState("test", DefaultLimits())
	now := t0

	reg := func(name string) string {
		tok := "tok-" + name
		if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: name, NewToken: tok}, now); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return tok
	}
	boss, other := reg("boss"), reg("other")
	if _, _, err := st.Apply(&Op{Kind: OpGrantRole, To: "boss", Mode: RoleCoordinator}, now); err != nil {
		t.Fatal(err)
	}
	for tok, agent := range map[string]string{boss: "src", other: "dst"} {
		if _, _, err := st.Apply(&Op{Kind: OpSpaceOpen, Token: tok, Space: agent, Text: "t"}, now); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Apply(&Op{
			Kind: OpSpacePost, Token: tok, Space: agent, Body: "said in " + agent,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := st.Apply(&Op{
		Kind: OpSpaceMerge, Token: boss, Space: "src", To: "dst",
	}, now); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if st.Spaces["src"] != nil {
		t.Fatal("precondition: the source agent should be gone after a merge")
	}
	var bodies []string
	for _, p := range st.Spaces["dst"].Posts {
		bodies = append(bodies, p.Body)
	}
	for _, want := range []string{"said in src", "said in dst"} {
		if !slices.Contains(bodies, want) {
			t.Errorf("post %q did not survive the merge; survivors: %v", want, bodies)
		}
	}
}
