package mcp

import "testing"

// board renders the board to a HUMAN, and the board carries agent
// descriptions, working directories, hostnames and branch names. An earlier
// version served it to any caller: it accepted a token that `inbox` had
// rejected seconds before, on the same connection, and drew everything.
//
// Reaching the daemon proves you are on this machine. It does not make you a
// participant, and the two must not be conflated.
func TestShowBoardRequiresAToken(t *testing.T) {
	for _, tool := range toolDefs {
		if tool["name"] != "board" {
			continue
		}
		schema := tool["inputSchema"].(map[string]any)
		req, _ := schema["required"].([]string)
		for _, r := range req {
			if r == "token" {
				return // good
			}
		}
		t.Fatal("board does not require a token; the board is not public")
	}
	t.Fatal("board tool not found")
}

// The board carries agent descriptions, cwd, hostnames and branch names. Two
// separate holes let it out: board deliberately not authenticating, and,
// after that was closed. SubscribeInfo succeeding on an empty token, because it
// short-circuits to serve token-less board subscriptions. It looks like an
// authenticator and is not one.
func TestShowBoardRejectsEveryFormOfMissingToken(t *testing.T) {
	s := &Server{}
	for _, tok := range []string{"", "   ", "\t"} {
		if _, err := s.showBoard(t.Context(), tok, "board"); err == nil {
			t.Errorf("showBoard served the board for token %q", tok)
		}
	}
}
