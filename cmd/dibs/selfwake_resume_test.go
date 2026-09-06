package main

import (
	"context"
	"net/http"
	"testing"
)

// R4-5: a resume's rotated token reaches the watcher, as a register's does.
func TestAResumeReplyStartsTheWatcherWithItsToken(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var iw inboxWatcher
	hook := watchOnRegister(ctx, &iw, &http.Client{}, "http://127.0.0.1:1/mcp", "secret")
	sent := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"resume","arguments":{"nonce":"n"}}}`)
	reply := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"agent_id\":\"a\",\"token\":\"rotated-token\",\"serial\":42}"}]}}`)
	hook(sent, reply)
	got := ""
	if ts := iw.tokens(); len(ts) > 0 {
		got = ts[0]
	}
	since := iw.sinceOf("rotated-token")
	if since != 42 {
		t.Errorf("the watcher starts with cursor %d, want the reply's serial 42: a question that "+
			"arrives before the first subscription succeeds is skipped for good", since)
	}
	if got != "rotated-token" {
		t.Fatalf("after a resume reply the watcher holds token %q: a bridge that begins with "+
			"resume never watches, and one that resumes later subscribes with a revoked token", got)
	}
}

// A re-registration's serial does not move an established cursor past
// notifications the watcher has not seen.
func TestAReRegistrationDoesNotAdvanceAnEstablishedCursor(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var iw inboxWatcher
	hook := watchOnRegister(ctx, &iw, &http.Client{}, "http://127.0.0.1:1/mcp", "secret")
	sent := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{"nonce":"n"}}}`)
	// The established cursor: this agent's first registration.
	hook(sent, []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"agent_id\":\"a\",\"token\":\"t1\",\"serial\":10}"}]}}`))
	reply := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"agent_id\":\"a\",\"token\":\"t2\",\"serial\":12}"}]}}`)
	hook(sent, reply)
	since := iw.sinceOf("t2")
	if since != 10 {
		t.Fatalf("the cursor moved to %d on a re-registration: an unseen question at 11 is skipped", since)
	}
}
