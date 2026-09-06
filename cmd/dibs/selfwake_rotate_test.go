package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A rotated token retires the old subscription and starts one with the new.
//
// sync.Once started one watcher for the life of the bridge with the FIRST
// token baked into its body. A reattach rotates the token, so after the next
// reconnect every subscription failed authentication with a revoked
// credential, quietly and forever: mail stayed fetchable and the wake this
// exists for silently stopped. Found by the pre-release review, round three.
func TestARotatedTokenReplacesTheWakeSubscription(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/nonexistent/but/present.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	seen := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Params struct {
				Meta map[string]string `json:"_meta"`
			} `json:"params"`
		}
		_ = json.Unmarshal(b, &req)
		seen <- req.Params.Meta["com.dibs/token"]
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold the stream open until the watcher lets go
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var iw inboxWatcher
	next := func(want string) {
		t.Helper()
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("the subscription carried token %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no subscription with token %q arrived", want)
		}
	}
	iw.start(ctx, srv.Client(), srv.URL, "secret", "token-A")
	next("token-A")
	// The same token again is idempotent: no second stream.
	iw.start(ctx, srv.Client(), srv.URL, "secret", "token-A")
	// A rotated token replaces the stream.
	iw.start(ctx, srv.Client(), srv.URL, "secret", "token-B")
	next("token-B")
	select {
	case extra := <-seen:
		t.Fatalf("an extra subscription appeared carrying %q: the same token started a second "+
			"stream, or the old one was not retired", extra)
	case <-time.After(300 * time.Millisecond):
	}
}
