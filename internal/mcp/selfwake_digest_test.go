package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// listenDeclaringSelfWake opens a 2026 listen that says this bridge will deliver
// its agent's wake notices into the session it runs in.
func listenDeclaringSelfWake(t *testing.T, srv *httptest.Server, token, session string) <-chan string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "listen", "method": "subscriptions/listen",
		"params": map[string]any{
			"_meta": map[string]any{
				metaTokenKey: token, SessionMetaKey: session, SelfWakeMetaKey: true,
			},
			"notifications": map[string]any{"resourceSubscriptions": []string{"dibs://inbox"}},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listen did not open: %s", resp.Status)
	}
	return scanLines(ctx, resp)
}

// digestOnInbox waits for an inbox notification and returns its digest, or "".
func digestOnInbox(lines <-chan string) (string, bool) {
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return "", false
			}
			data, found := strings.CutPrefix(line, "data: ")
			if !found {
				continue
			}
			var frame struct {
				Method string `json:"method"`
				Params struct {
					URI  string         `json:"uri"`
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if json.Unmarshal([]byte(data), &frame) != nil {
				continue
			}
			if frame.Method != "notifications/resources/updated" || frame.Params.URI != "dibs://inbox" {
				continue
			}
			d, _ := frame.Params.Meta[DigestMetaKey].(string)
			return d, true
		case <-deadline:
			return "", false
		}
	}
}

// A bridge that will wake its own session is told WHAT to say, and the daemon
// stops saying it.
//
// THE TWO HALVES ARE ONE MECHANISM and neither is safe alone. A Claude Code
// session has one message socket; Dibs was writing to it from the daemon and
// from the session's own stdio bridge, which did not know about each other, so
// every message produced two notifications. The bridge won the race because it
// needs no sidecar lookup, and what it sent was a fixed sentence with no facts
// in it: the operator saw the placeholder on top and the real digest underneath,
// four times, across three releases that each reworded the placeholder.
//
// So the bridge declares that it can reach its session, the daemon stands down
// for that agent, and this notification carries what the daemon would have
// written. Stand down without the digest and the surviving card says nothing;
// send the digest without standing down and there are still two.
func TestASelfWakingBridgeIsHandedTheDigestAndTheDaemonStandsDown(t *testing.T) {
	srv, _ := newServer(t)
	eng := srv.Config.Handler.(*Server).eng
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	busy := toolCall(t, srv, "register", map[string]any{
		"name": "busy", "cwd": t.TempDir(), "session_id": "9f1e2d3c-4b5a-4c6d-8e7f-0a1b2c3d4e5f",
	})
	token, _ := busy["token"].(string)
	id, _ := busy["agent_id"].(string)
	if token == "" || id == "" {
		t.Fatalf("setup: the registration named no agent or token: %v", busy)
	}

	// THE CONTROL FIRST. A stream that declares nothing must leave the socket
	// route alone, or the claim below proves only that a stream was opened.
	plain := openListen(t, srv, token, nil)
	if live, _ := eng.SelfWaking(id); live {
		t.Fatal("setup: an ordinary subscription claimed the session's socket. The daemon " +
			"would stop writing to it for every harness that never asked, which silences " +
			"the one route an idle session has")
	}
	_ = plain

	lines := listenDeclaringSelfWake(t, srv, token, "9f1e2d3c-4b5a-4c6d-8e7f-0a1b2c3d4e5f")
	// THE ACKNOWLEDGMENT IS THE EVENT TO WAIT ON, and it proves the ordering
	// too. The claim is taken before the acknowledgment is written, so that no
	// window exists in which the stream is live and the daemon still believes it
	// owns the socket; the ack arriving therefore means the claim is already in
	// place, and a poll here would be asserting something weaker.
	if !awaitAck(lines) {
		t.Fatal("setup: the subscription was never acknowledged, so nothing below " +
			"proves anything about the claim")
	}
	if live, _ := eng.SelfWaking(id); !live {
		t.Fatal("a bridge that declared it delivers its own wakes did not claim the " +
			"session by the time its subscription was acknowledged: the daemon goes on " +
			"writing to the same socket and the operator gets two notifications for " +
			"every message")
	}

	toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "busy", "type": "question", "body": "does the fold hold?",
	})
	digest, got := digestOnInbox(lines)
	if !got {
		t.Fatal("no inbox notification reached the self-waking bridge")
	}
	if digest == "" {
		t.Fatalf("the notification carried no digest. The daemon has stood down for this "+
			"agent, so this notification is the ONLY thing that will announce the message, "+
			"and the bridge has nothing of its own to say: _meta[%q] is the whole wake",
			DigestMetaKey)
	}
	if !strings.Contains(digest, "asker") {
		t.Errorf("the digest does not name the sender: %q. Whatever is missing here is "+
			"missing from the wake the agent actually sees", digest)
	}
}

// The digest goes only to the stream holding that agent's own token.
//
// IT QUOTES MESSAGE BODIES. dibs://inbox is deliberately senders-and-counts,
// because a resource is application-controlled and the host decides who sees
// it; the digest is a different thing travelling in the same envelope, and it
// exists for one subscriber that asked for it by name. A board stream, or an
// inbox stream that made no self-wake claim, has nowhere to put it and no
// business holding it.
func TestTheDigestGoesOnlyToTheBridgeThatAskedForIt(t *testing.T) {
	srv, _ := newServer(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	busy := toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	token, _ := busy["token"].(string)

	lines := openListen(t, srv, token, nil) // no self-wake declared
	toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "busy", "type": "question", "body": "quiet please",
	})
	digest, got := digestOnInbox(lines)
	if !got {
		t.Fatal("setup: no inbox notification arrived at all, so this proves nothing")
	}
	if digest != "" {
		t.Errorf("a subscriber that made no self-wake claim was handed the mail digest: %q.\n"+
			"  The daemon has NOT stood down for it, so this is a second announcement of "+
			"one message, and it carries message bodies to a stream that asked for "+
			"senders and counts", digest)
	}
}

// awaitAck reports whether the stream acknowledged the subscription.
func awaitAck(lines <-chan string) bool {
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false
			}
			data, found := strings.CutPrefix(line, "data: ")
			if !found {
				continue
			}
			var frame struct {
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(data), &frame) != nil {
				continue
			}
			if frame.Method == "notifications/subscriptions/acknowledged" {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
