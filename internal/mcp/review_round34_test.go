package mcp

import (
	"context"
	"testing"
	"time"
)

// Opening a subscription spends one of the agent's rate tokens, and the gap
// replay read the ring AS the agent, spending another: when the listen took
// the last one the replay got E_RATE_LIMITED, an error was an empty gap, and
// the acknowledged stream proceeded from the present past a pending
// question. The replay is the daemon's own work for a subscriber it already
// authenticated, and reads the ring the way the daemon does.
func TestAReplayIsNotChargedToTheSubscribersRateBudget(t *testing.T) {
	srv, eng, _ := newServerWithEngine(t)
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	cursor, _ := busy["serial"].(float64)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	sent := toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "busy", "type": "question", "body": "while you were away",
	})
	serial, _ := sent["msg_serial"].(float64)
	if serial == 0 || cursor == 0 {
		t.Fatalf("setup: no serials: %v %v", sent, busy)
	}
	// The listen below is busy's last call before the bucket refills.
	if err := eng.SetRateTokens(context.Background(), "busy", 1); err != nil {
		t.Fatal("setup:", err)
	}
	lines := openListen(t, srv, busy["token"].(string), cursor)
	if !awaitInboxSerial(lines, serial, 3*time.Second) {
		t.Fatal("a subscriber that opened its stream on its last rate token was handed nothing " +
			"for the gap: the replay was charged as a call, refused, and treated as empty")
	}
}
