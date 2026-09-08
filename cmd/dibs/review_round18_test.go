package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A wake that failed keeps its notification: the cursor does not pass it, so
// a reconnect replays it.
func TestAFailedWakeKeepsItsNotification(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	bodies := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		bodies <- req
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"notifications":{},"_meta":{"com.dibs/serial":5}}}`+"\n\n")
		_, _ = fmt.Fprint(w, `data: {"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"dibs://inbox","_meta":{"com.dibs/event":"message.sent","com.dibs/msg_type":"question","com.dibs/serial":7}}}`+"\n\n")
		fl.Flush()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := inboxWatcher{reconnect: 50 * time.Millisecond}
	w.start(ctx, srv.Client(), srv.URL, "local-secret", "agent-token")
	<-bodies
	select {
	case second := <-bodies:
		params, _ := second["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if got, _ := meta["com.dibs/since"].(float64); got != 5 {
			t.Fatalf("after a wake that could not be delivered the reconnect carries since=%v, want 5: "+
				"the question's notification is consumed and nothing replays it", meta["com.dibs/since"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher did not reconnect")
	}
}

// A wake that failed is retried on its own once the cooldown passes, so a
// socket that comes back gets the notice without another arrival.
func TestAFailedWakeIsRetriedWhenTheSocketReturns(t *testing.T) {
	sock := sockPath(t)
	w := &selfWaker{socket: sock, token: "tok", cooldown: 200 * time.Millisecond}
	if err := w.wake(selfWakeNotice); err == nil {
		t.Fatal("setup: a wake with nobody listening reported success")
	}
	lines := listenLines(t, sock)
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("after the socket came back %d line(s) arrived without another wake, want 2: "+
			"the agent stays asleep on stored mail until something else arrives", len(got))
	}
}
