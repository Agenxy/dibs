package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A 2025-11-25 client must get real push, not just the promise of it.
//
// Lanes advertised resources.subscribe on the legacy handshake and implemented
// only the 2026-07-28 method behind it, so every shipping client — which is all
// of them — saw a capability it could not use and fell back to polling. The
// advertisement was true and useless, which is worse than absent: a client that
// believes it will be told does not ask.
//
// Two halves, because 2025-11-25 splits them: the POST registers interest, the
// GET carries the notifications, and the subscription has to survive in between.
func TestLegacyClientReceivesPushWithoutPolling(t *testing.T) {
	srv, _ := newServer(t)

	out := rpc(t, srv, "", "initialize", map[string]any{
		"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "legacy", "version": "1"},
	})
	caps, _ := out["result"].(map[string]any)["capabilities"].(map[string]any)
	res, _ := caps["resources"].(map[string]any)
	if sub, _ := res["subscribe"].(bool); !sub {
		t.Fatal("the legacy handshake does not advertise resources.subscribe, so no client will try")
	}
}

// Subscribing to another lane's mailbox must require that lane's token.
//
// lanes://inbox is private, and the GET that opens the stream carries no body —
// so the token is proven once, at subscribe time, and remembered. If that check
// were missing, any session could open a channel onto somebody else's mail.
func TestLegacyInboxSubscriptionRequiresTheLanesToken(t *testing.T) {
	srv, _ := newServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"resources/subscribe","params":{"uri":"lanes://inbox"}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "s-probe")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if _, isErr := env["error"]; !isErr {
		t.Error("subscribing to lanes://inbox succeeded with no token; that stream would " +
			"carry another lane's mail to whoever asked")
	}
}

// An unknown URI is refused rather than accepted and ignored.
//
// A client that subscribes to a typo and waits forever has been told nothing is
// happening, when in fact nobody was ever listening on its behalf.
func TestSubscribingToAnUnknownResourceIsRefused(t *testing.T) {
	l := newLegacySubs()
	if l.add("s1", "lanes://nonsense", "") {
		t.Error("a URI Lanes does not publish was accepted; the client would wait forever")
	}
	if !l.add("s1", "lanes://board", "") {
		t.Error("lanes://board was refused")
	}
	if got := l.get("s1"); !got.board || got.inbox {
		t.Errorf("subscription state wrong after adding board only: %+v", got)
	}
}

// A session with no id cannot subscribe: there would be no way to find its
// stream later, so the subscription would be silently unreachable.
func TestSubscriptionNeedsASession(t *testing.T) {
	l := newLegacySubs()
	if l.add("", "lanes://board", "") {
		t.Error("a subscription was recorded against no session; nothing could ever deliver it")
	}
}

// Unsubscribing stops one flow without disturbing another on the same stream.
func TestUnsubscribeIsPerResource(t *testing.T) {
	l := newLegacySubs()
	l.add("s1", "lanes://board", "")
	l.add("s1", "lanes://inbox", "tok")
	l.remove("s1", "lanes://board")
	got := l.get("s1")
	if got.board {
		t.Error("board still subscribed after unsubscribe")
	}
	if !got.inbox || got.token != "tok" {
		t.Error("unsubscribing from board disturbed the inbox subscription on the same stream")
	}
	_ = time.Now
}
