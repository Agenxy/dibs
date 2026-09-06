package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
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
	mu sync.Mutex
	// streams is one subscription per AGENT this bridge registered, keyed by
	// the agent id the register reply named (or "" when it named none). A
	// bridge serves every agent that registers through it, and the first
	// version held one token: registering a second agent retired the first
	// one's subscription, so only the last registered mailbox kept its
	// self-wake. Found by the pre-release review, round fifty-four.
	streams map[string]*inboxStream
	// reconnect is the pause between a stream ending and the next attempt;
	// zero means reconnectAfter. A field, set before start, so a test can
	// shorten it without writing a global under a running goroutine.
	reconnect time.Duration
	// waker is the ONE route to this session's socket, shared by every
	// stream: the cooldown is a promise about the session, not about a
	// mailbox, and a waker per stream gave two mailboxes two interruptions
	// microseconds apart. Found by the pre-release review, round fifty-eight.
	waker *selfWaker
	// cooldown overrides the shared waker's cooldown when set before the
	// first start; a test knob, like reconnect.
	cooldown time.Duration
}

// inboxStream is one agent's subscription: its credential and its cursor.
type inboxStream struct {
	key    string
	token  string
	cancel context.CancelFunc
	since  uint64 // the serial of the last notification seen: a reconnect resumes from it
}

// sharedWaker is the one route to this session's socket, made on first
// use; nil when this harness publishes none.
func (iw *inboxWatcher) sharedWaker() *selfWaker {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	if iw.waker == nil {
		iw.waker = newSelfWaker()
		if iw.waker != nil && iw.cooldown > 0 {
			iw.waker.cooldown = iw.cooldown
		}
	}
	return iw.waker
}

// tokens lists the credentials the watcher currently subscribes with.
func (iw *inboxWatcher) tokens() []string {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	var out []string
	for _, st := range iw.streams {
		out = append(out, st.token)
	}
	sort.Strings(out)
	return out
}

// sinceOf is the cursor of the stream subscribing with token, or 0.
func (iw *inboxWatcher) sinceOf(token string) uint64 {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	for _, st := range iw.streams {
		if st.token == token {
			return st.since
		}
	}
	return 0
}

// streamOf is the stream subscribing with token, or nil.
func (iw *inboxWatcher) streamOf(token string) *inboxStream {
	iw.mu.Lock()
	defer iw.mu.Unlock()
	for _, st := range iw.streams {
		if st.token == token {
			return st
		}
	}
	return nil
}

// reconnectAfter is the pause between a stream ending and the next attempt.
const reconnectAfter = 2 * time.Second

// listenBody is the subscription request, carrying the serial last seen so a
// reconnect is handed what arrived while the stream was down. The first
// version reconnected blind and every message in the gap woke nobody. Found
// by the pre-release review, round twelve.
func (iw *inboxWatcher) listenBody(st *inboxStream) []byte {
	meta := map[string]any{"com.dibs/token": st.token}
	// The session this stream serves, so the daemon withholds the inbox
	// while the agent is in another one: a bridge left behind by an
	// identity that moved to a second session kept waking the first.
	// Found by the pre-release review, round fifty-nine.
	if sid := sessionID(); sid != "" {
		meta[mcp.SessionMetaKey] = sid
	}
	iw.mu.Lock()
	if st.since > 0 {
		meta[mcp.SinceMetaKey] = st.since
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

// alreadySeen reports whether a notification's serial is at or behind the
// stream's cursor: a notice for it has landed, or its event moved nothing
// worth one.
func (iw *inboxWatcher) alreadySeen(st *inboxStream, meta map[string]any) bool {
	v, ok := meta[mcp.SerialMetaKey].(float64)
	if !ok || v <= 0 {
		return false
	}
	iw.mu.Lock()
	defer iw.mu.Unlock()
	return uint64(v) <= st.since
}

func (iw *inboxWatcher) noteSerial(st *inboxStream, meta map[string]any) {
	v, ok := meta[mcp.SerialMetaKey].(float64)
	if !ok || v <= 0 {
		return
	}
	iw.mu.Lock()
	if uint64(v) > st.since {
		st.since = uint64(v)
	}
	iw.mu.Unlock()
	recordWakeCursor(st.key, uint64(v)) // for the in-place upgrade's handoff
}

// start subscribes for one credential under the unnamed key: the one-agent
// bridge, and the tests written for it. See startFor.
func (iw *inboxWatcher) start(ctx context.Context, client *http.Client, url, secret, token string) {
	iw.startFor(ctx, client, url, secret, "", token, 0)
}

// startFor subscribes for the agent under key with token, seeding a cursor
// when the stream is new and seed is not zero.
//
// ONE STREAM PER AGENT, ONE CREDENTIAL PER STREAM. A reattach ROTATES the
// token, so after the next reconnect a stream holding the old one failed
// authentication with a revoked credential, quietly, forever (round three):
// a new token for the same key retires that key's stream and keeps its
// cursor. A new KEY is another agent registering through this bridge, and
// its stream stands beside the others rather than replacing them (round
// fifty-four).
func (iw *inboxWatcher) startFor(
	ctx context.Context, client *http.Client, url, secret, key, token string, seed uint64,
) {
	if token == "" {
		return
	}
	waker := iw.sharedWaker()
	if waker == nil {
		return // this harness publishes no session socket: nothing local to do
	}
	iw.mu.Lock()
	defer iw.mu.Unlock()
	if iw.streams == nil {
		iw.streams = map[string]*inboxStream{}
	}
	prev := iw.streams[key]
	if prev != nil && prev.token == token {
		return
	}
	since := seed
	if prev != nil {
		// A SEED, NOT AN ADVANCE: an established cursor names the last
		// notification seen, and a re-registration is no evidence the events
		// between were seen. Found by the pre-release review, round twenty-one.
		if prev.since > 0 {
			since = prev.since
		}
		prev.cancel()
	}
	sub, cancel := context.WithCancel(ctx)
	st := &inboxStream{key: key, token: token, cancel: cancel, since: since}
	iw.streams[key] = st
	recordWakeStream(key, token, since) // for the in-place upgrade's handoff
	go iw.run(sub, client, url, secret, st, waker)
}

func (iw *inboxWatcher) run(
	ctx context.Context, client *http.Client, url, secret string, st *inboxStream, waker *selfWaker,
) {
	pause := iw.reconnect
	if pause == 0 {
		pause = reconnectAfter
	}
	for {
		if ctx.Err() != nil {
			return
		}
		iw.stream(ctx, client, url, secret, st, waker)
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
	ctx context.Context, client *http.Client, url, secret string, st *inboxStream, waker *selfWaker,
) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(iw.listenBody(st)))
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
		if msg.Method == "notifications/subscriptions/acknowledged" {
			// The cursor to reconnect with, before any mail has moved: a
			// stream that dropped on an empty inbox used to reconnect blind
			// and the question that arrived in between woke nobody.
			iw.noteSerial(st, msg.Params.Meta)
			continue
		}
		if msg.Method != "notifications/resources/updated" {
			continue
		}
		// ONCE PER SERIAL. A resumed subscription is handed the gap twice, by
		// the daemon's filtered replay and by the channel it subscribes on
		// from the same cursor, and the second copy of a notice already
		// delivered queued another wake at the cooldown, whether or not the
		// agent had read the mail by then. Found by the pre-release review,
		// round twenty-seven.
		if iw.alreadySeen(st, msg.Params.Meta) {
			continue
		}
		if !worthAWake(msg.Params.Meta) {
			iw.noteSerial(st, msg.Params.Meta)
			continue
		}
		// THE CURSOR MOVES WHEN THE NOTICE LANDS. Advancing it first consumed
		// the notification of a wake that failed: the reconnect excluded the
		// event and nothing retried, so a socket that came back found an
		// agent asleep on stored mail. Found by the pre-release review, round
		// eighteen.
		if err := waker.wake(selfWakeNotice); err != nil {
			slog.Debug("could not put a notice into this session; keeping its cursor", "err", err)
			continue
		}
		iw.noteSerial(st, msg.Params.Meta)
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
			// KEYED BY THE AGENT the reply names, so a second agent
			// registering through this bridge gets a stream beside the first
			// one's rather than in its place, and a rotation replaces only its
			// own. The reply's serial seeds a NEW stream's cursor; an
			// established one keeps its own (round twenty-one).
			w.startFor(ctx, client, url, secret, agentIDIn(reply), tok, agentSerialIn(reply))
		}
	}
}
