package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/humankey"
)

// fakeSigner records what it was asked to sign and why, and needs no finger.
type fakeSigner struct {
	mu      sync.Mutex
	signed  []string
	reasons []string
}

func (f *fakeSigner) Create() (string, error) { return "pub", nil }

func (f *fakeSigner) Sign(msg []byte, reason string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signed = append(f.signed, string(msg))
	f.reasons = append(f.reasons, reason)
	return "sig", nil
}

// fakeBoard answers the relay's challenge and records its answers.
type fakeBoard struct {
	node    string
	mu      sync.Mutex
	answers []map[string]any
}

func (b *fakeBoard) serve() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/human/challenge", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"nonce": "n1", "node": b.node})
	})
	mux.HandleFunc("POST /api/human/answer", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.mu.Lock()
		b.answers = append(b.answers, body)
		b.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	return httptest.NewServer(mux)
}

func answerWith(a humanask.Answer) func(humanask.Message) (humanask.Answer, error) {
	return func(humanask.Message) (humanask.Answer, error) { return a, nil }
}

// Approving a grant from this Mac costs a finger on THAT approval, with a
// reason naming the effect; an ordinary answer rides the session.
func TestTheRelaySignsApprovalsThatGrantSomething(t *testing.T) {
	board := &fakeBoard{node: "node-a"}
	srv := board.serve()
	defer srv.Close()
	key := &fakeSigner{}
	st := relayState{Key: "k1", Node: "node-a"}

	r := newRelay(srv.URL, st, key, answerWith(humanask.Answer{Disposition: "approve"}))
	r.handle(engine.HumanNotice{Serial: 7, Type: "request", From: "climber", Grant: "coordinator"})
	if len(key.signed) != 1 || key.signed[0] != string(humankey.AnswerMessage("node-a", 7, "approve", "n1")) {
		t.Fatalf("signed %q, want the approval of 7", key.signed)
	}
	if !strings.Contains(key.reasons[0], "make climber coordinator") {
		t.Errorf("the Touch ID sheet reads %q, which does not say what approving does", key.reasons[0])
	}
	if board.answers[0]["signature"] != "sig" || board.answers[0]["nonce"] != "n1" {
		t.Errorf("the approval went without its signature: %v", board.answers[0])
	}

	r = newRelay(srv.URL, st, key, answerWith(humanask.Answer{Disposition: "answer", Body: "yes"}))
	r.handle(engine.HumanNotice{Serial: 8, Type: "question", From: "asker"})
	if len(key.signed) != 1 {
		t.Errorf("an ordinary answer asked for a finger: %q", key.signed)
	}
	if len(board.answers) != 2 || board.answers[1]["body"] != "yes" {
		t.Errorf("the answer did not reach the board: %v", board.answers)
	}

	// Dismissed is not an answer, and nothing is sent.
	r = newRelay(srv.URL, st, key, answerWith(humanask.Answer{}))
	r.handle(engine.HumanNotice{Serial: 9, Type: "question", From: "asker"})
	if len(board.answers) != 2 {
		t.Errorf("a dismissed question sent an answer: %v", board.answers[len(board.answers)-1])
	}
}

// A different board behind the same address is not the one this key
// enrolled with, and the relay will not sign for it.
func TestTheRelayWillNotSignForAnotherBoard(t *testing.T) {
	board := &fakeBoard{node: "impostor"}
	srv := board.serve()
	defer srv.Close()
	key := &fakeSigner{}
	r := newRelay(srv.URL, relayState{Key: "k1", Node: "node-a"}, key,
		answerWith(humanask.Answer{Disposition: "approve"}))
	r.handle(engine.HumanNotice{Serial: 7, Type: "request", From: "climber", Grant: "coordinator"})
	if len(key.signed) != 0 {
		t.Errorf("the relay signed for a board it did not enrol with: %q", key.signed)
	}
	if len(board.answers) != 0 {
		t.Errorf("an answer went to the impostor: %v", board.answers)
	}
}
