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
	reply := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"agent_id\":\"a\",\"token\":\"rotated-token\"}"}]}}`)
	hook(sent, reply)
	iw.mu.Lock()
	got := iw.token
	iw.mu.Unlock()
	if got != "rotated-token" {
		t.Fatalf("after a resume reply the watcher holds token %q: a bridge that begins with "+
			"resume never watches, and one that resumes later subscribes with a revoked token", got)
	}
}
