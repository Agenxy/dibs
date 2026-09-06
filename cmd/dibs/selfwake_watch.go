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

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mcp"
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
	since  uint64 // the serial of the last notification seen: a reconnect resumes from it
	// reconnect is the pause between a stream ending and the next attempt;
	// zero means reconnectAfter. A field, set before start, so a test can
	// shorten it without writing a global under a running goroutine.
	reconnect time.Duration
}

// reconnectAfter is the pause between a stream ending and the next attempt.
const reconnectAfter = 2 * time.Second

// listenBody is the subscription request, carrying the serial last seen so a
// reconnect is handed what arrived while the stream was down. The first
// version reconnected blind and every message in the gap woke nobody. Found
// by the pre-release review, round twelve.
func (iw *inboxWatcher) listenBody(token string) []byte {
	meta := map[string]any{"com.dibs/token": token}
	iw.mu.Lock()
	if iw.since > 0 {
		meta[mcp.SinceMetaKey] = iw.since
	}
	iw.mu.Unlock()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "dibs-selfwake", "method": "subscriptions/listen",
		"params": map[string]any{
			"_meta":         meta,
			"notifications": map[string]any{"resourceSubscriptions": []string{"dibs://inbox"}},
		},
	})
	return body
}

func (iw *inboxWatcher) noteSerial(meta map[string]any) {
	v, ok := meta[mcp.SerialMetaKey].(float64)
	if !ok || v <= 0 {
		return
	}
	iw.mu.Lock()
	if uint64(v) > iw.since {
		iw.since = uint64(v)
	}
	iw.mu.Unlock()
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
	recordWakeToken(token) // for the in-place upgrade's handoff
	go iw.run(sub, client, url, secret, token, waker)
}

func (iw *inboxWatcher) run(
	ctx context.Context, client *http.Client, url, secret, token string, waker *selfWaker,
) {
	pause := iw.reconnect
	if pause == 0 {
		pause = reconnectAfter
	}
	for {
		if ctx.Err() != nil {
			return
		}
		iw.stream(ctx, client, url, secret, iw.listenBody(token), waker)
		// The daemon closes this stream when it goes away. Reconnecting is the
		// whole point of a wake path: an agent whose subscription died quietly
		// is an agent that stops being reachable and never learns it did.
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
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
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(bytes.TrimSpace(data), &msg) != nil {
			continue
		}
		// The acknowledgement is not mail. Only an actual resource update means
		// something arrived for this agent, and only some of that is worth a
		// notice.
		if msg.Method != "notifications/resources/updated" {
			continue
		}
		iw.noteSerial(msg.Params.Meta)
		if !worthAWake(msg.Params.Meta) {
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
// worthAWake applies the daemon's own rule to the event the notification
// names. Every inbox change used to put a notice into the session, a notify
// included, which the daemon's waker never does for its own routes. A
// notification that names no event comes from a daemon older than the field,
// and wakes as before. Found by the pre-release review, round four.
func worthAWake(meta map[string]any) bool {
	evType, ok := meta[mcp.EventMetaKey].(string)
	if !ok {
		return true
	}
	msgType, _ := meta[mcp.MsgTypeMetaKey].(string)
	return core.WakeWorthy(evType, msgType)
}

func watchOnRegister(
	ctx context.Context, w *inboxWatcher, client *http.Client, url, secret string,
) func(sent, reply []byte) {
	return func(sent, reply []byte) {
		// register AND resume: both mint the credential the watcher subscribes
		// with. Handling only the first left a bridge that began with resume
		// never watching, and one that resumed later subscribing with a token
		// the resume had just revoked. Found by the pre-release review, round
		// four.
		if n := toolNameOf(sent); n != "register" && n != "resume" {
			return
		}
		if tok := agentTokenIn(reply); tok != "" {
			w.start(ctx, client, url, secret, tok)
		}
	}
}
