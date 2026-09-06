package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// selfWakeNotice is the one sentence every wake route carries.
//
// Kept identical to internal/engine's wakeNotice on purpose, and asserted by
// the wake e2e. Everything past "you have mail" is forbidden on every route:
// no counts, no senders, no body, nothing an agent wrote.
const selfWakeNotice = "Dibs: check the board."

// watchInboxAndWake keeps this session awake to its own mail.
//
// The daemon decides WHETHER an agent should be woken; it cannot decide how to
// reach a Claude Code session that is idle, because its only route from outside
// is the peer socket the receiver holds in bypassPermissions mode. This closes
// that from the inside: the bridge subscribes to its own agent's inbox over the
// connection it already has, and when the daemon pushes an update it puts the
// notice into the session it is running in, where a self-sent message is
// accepted rather than held.
//
// SEP-2575 is the push channel, which Dibs already serves. Nothing new is on
// the wire and no operator configures anything.
//
// Started once per agent token, and only when this harness published a socket
// to write to.
type inboxWatcher struct {
	mu     sync.Mutex
	token  string
	cancel context.CancelFunc
}

func (iw *inboxWatcher) start(ctx context.Context, client *http.Client, url, secret, token string) {
	waker := newSelfWaker()
	if waker == nil || token == "" {
		return // this harness publishes no session socket: nothing local to do
	}
	// ONE WATCHER PER CREDENTIAL, not one per bridge. sync.Once started the
	// first subscription and kept it for the life of the process, with the
	// first token baked into its body. A reattach ROTATES the token, so after
	// the next stream reconnect every subscription failed authentication with
	// a revoked credential, quietly, forever: mail stayed fetchable and the
	// wake it exists for stopped. A new token retires the old stream and
	// starts its own. Found by the pre-release review, round three.
	iw.mu.Lock()
	defer iw.mu.Unlock()
	if iw.token == token {
		return
	}
	if iw.cancel != nil {
		iw.cancel()
	}
	sub, cancel := context.WithCancel(ctx)
	iw.token, iw.cancel = token, cancel
	go iw.run(sub, client, url, secret, token, waker)
}

func (iw *inboxWatcher) run(
	ctx context.Context, client *http.Client, url, secret, token string, waker *selfWaker,
) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "dibs-selfwake", "method": "subscriptions/listen",
		"params": map[string]any{
			"_meta":         map[string]any{"com.dibs/token": token},
			"notifications": map[string]any{"resourceSubscriptions": []string{"dibs://inbox"}},
		},
	})
	for {
		if ctx.Err() != nil {
			return
		}
		iw.stream(ctx, client, url, secret, body, waker)
		// The daemon closes this stream when it goes away. Reconnecting is the
		// whole point of a wake path: an agent whose subscription died quietly
		// is an agent that stops being reachable and never learns it did.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (iw *inboxWatcher) stream(
	ctx context.Context, client *http.Client, url, secret string, body []byte, waker *selfWaker,
) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dibs-Local", secret)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if !isEventStream(resp) {
		return // refused: see relayRefusal, and there is nobody here to relay to
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		data, found := bytes.CutPrefix(bytes.TrimRight(sc.Bytes(), "\r"), []byte("data: "))
		if !found {
			continue
		}
		var msg struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(bytes.TrimSpace(data), &msg) != nil {
			continue
		}
		// The acknowledgement is not mail. Only an actual resource update means
		// something arrived for this agent.
		if msg.Method != "notifications/resources/updated" {
			continue
		}
		if err := waker.wake(selfWakeNotice); err != nil {
			slog.Debug("could not put a notice into this session", "err", err)
		}
	}
}

// watchOnRegister returns the reply hook that starts the local wake watcher.
//
// A REGISTER REPLY IS THE ONLY THING THAT CARRIES A TOKEN, and the token is what
// lets this bridge subscribe to its own agent's mail. Split out of the read loop
// rather than written inline there: that loop is already at the complexity the
// linter allows, and a wake path is not the thing to spend the last of it on.
func watchOnRegister(
	ctx context.Context, w *inboxWatcher, client *http.Client, url, secret string,
) func(sent, reply []byte) {
	return func(sent, reply []byte) {
		if toolNameOf(sent) != "register" {
			return
		}
		if tok := agentTokenIn(reply); tok != "" {
			w.start(ctx, client, url, secret, tok)
		}
	}
}
