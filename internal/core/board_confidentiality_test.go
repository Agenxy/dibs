package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// Board() is not the operator's private view. It is what ack_board returns to
// EVERY agent on every activation, and what /api/board serves to anything
// holding the coordination secret, which every agent must hold to call /mcp at
// all. So anything in it is public to the whole machine.
//
// It carried every announcement body from every channel. An agent that had
// joined nothing received them all, without asking, on the one call it is
// required to make before it does anything else.
func TestTheBoardCarriesNoLaneText(t *testing.T) {
	st := NewState("test", DefaultLimits())
	now := t0
	reg := func(name string) string {
		tok := "tok-" + name
		if _, _, err := st.Apply(&Op{Kind: OpRegisterLane, Name: name, NewToken: tok}, now); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, _, err := st.Apply(&Op{Kind: OpAckBoard, Token: tok}, now); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
		return tok
	}
	insider := reg("insider")
	reg("outsider")

	if _, _, err := st.Apply(&Op{
		Kind: OpLaneOpen, Token: insider, Channel: "secret", Text: "t",
	}, now); err != nil {
		t.Fatal(err)
	}
	const announced = "THE-ANNOUNCEMENT-TEXT"
	const posted = "THE-POST-TEXT"
	if _, _, err := st.Apply(&Op{
		Kind: OpLaneAnnounce, Token: insider, Channel: "secret", Body: announced,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Apply(&Op{
		Kind: OpLanePost, Token: insider, Channel: "secret", Body: posted,
	}, now); err != nil {
		t.Fatal(err)
	}

	blob, err := json.Marshal(st.Board())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{announced, posted} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("the board carries %q: every agent gets this from ack_board, "+
				"whether or not it is in the lane", secret)
		}
	}
	// It must still say an announcement HAPPENED, or the board stops being a
	// board: the count of what is hanging over a lane is the point.
	if !strings.Contains(string(blob), `"said"`) {
		t.Errorf("the board no longer mentions announcements at all: %s", blob)
	}
}
