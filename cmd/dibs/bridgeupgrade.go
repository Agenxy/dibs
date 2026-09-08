package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// The bridge replaces itself when its binary changes, without the harness
// noticing.
//
// A stdio bridge is spawned once per session and held for the session's
// lifetime, so installing a new `dibs` does nothing to the one already running:
// an agent keeps talking to the build it started with, for as long as the
// session lasts, which for the agents this product exists for is days. Every
// fix to the bridge was therefore gated on the operator restarting every
// harness on the machine, which is exactly the ceremony R12 refuses to charge
// for a daemon upgrade and has no better claim to here.
//
// It was not theoretical. The repair that attaches an agent to its harness
// session rides in the bridge, so the agent whose mail was going undelivered
// would have kept not receiving it until its session ended.
//
// Re-exec is the whole mechanism, and it works because of what exec does NOT
// touch: the process keeps its pid and its file descriptors, so stdin and
// stdout stay the same pipes the harness is holding. From the harness's side
// nothing happened at all.
//
// Three things have to be true first, and each is a way to lose a request:
//
//   - No line may be part-read. Buffered stdin lives in this process's memory,
//     not in the pipe, so exec would discard it. Bytes still in the kernel's
//     pipe buffer are safe, which is why the check is on OUR buffer alone.
//   - No request may be in flight. The check runs after a reply is written and
//     before the next line is read, which is the only quiescent point there is.
//   - Nothing this process is holding may be silently dropped. The handshake
//     identity and any open subscription are carried across in the environment
//     and re-established, because a subscription that ends quietly is a harness
//     that simply stops hearing, with nothing to notice.

// bridgeStateEnv carries what the next image must not lose.
const bridgeStateEnv = "DIBS_BRIDGE_STATE"

// bridgeState is everything an exec would otherwise discard.
//
// Listens are the caller's OWN listen requests, verbatim. Re-issuing those is
// the same rule followStream already follows across a daemon restart (R12):
// whatever the harness subscribed to is what it gets again, decided by the
// harness rather than reconstructed here.
type bridgeState struct {
	ClientInfo map[string]any `json:"client_info,omitempty"`
	WantsUI    bool           `json:"wants_ui,omitempty"`
	Listens    []string       `json:"listens,omitempty"`
	// WakeToken is the agent token the self-wake watcher subscribes with.
	// The handoff carried the caller's subscriptions and not this one, so an
	// upgraded bridge answered every call and never woke its session again
	// until the agent happened to register or resume. Found by the
	// pre-release review, round eleven.
	WakeToken string `json:"wake_token,omitempty"`
	// WakeSince is the serial the watcher last saw, so the replacement
	// subscribes with a cursor: an upgrade used to start it at the present
	// and mail that arrived in the gap woke nobody. Found by the pre-release
	// review, round fourteen.
	WakeSince uint64 `json:"wake_since,omitempty"`
	// WakePending says a notice was owed and deferred to the cooldown when
	// the image was replaced. The timer dies with the old process and the
	// cursor had already passed the event, so the next image put nothing
	// into the session and the reconnect replayed nothing: outstanding mail
	// unnoticed for as long as nothing else arrived. The next image delivers
	// the owed notice. Found by the pre-release review, round twenty-two.
	WakePending bool `json:"wake_pending,omitempty"`
	// WakeStreams is every agent this bridge watches for, with its
	// credential and cursor: a bridge serves every agent that registers
	// through it, and the single WakeToken above carried one. Found by the
	// pre-release review, round fifty-four.
	WakeStreams []wakeHandoff `json:"wake_streams,omitempty"`
	// Thread is the harness thread this bridge serves, as the harness named
	// it on its tool calls; the restored self-wake streams say they serve
	// it before the next call names it again. Found by the pre-release
	// review, round sixty.
	Thread string `json:"thread,omitempty"`
}

// wakeHandoff is one watched agent in the handoff.
type wakeHandoff struct {
	Key   string `json:"key"`
	Token string `json:"token"`
	Since uint64 `json:"since,omitempty"`
}

// liveWake is the token the self-wake watcher currently holds, for the
// handoff; the watcher itself is local to the serving loop.
var liveWake struct {
	mu      sync.Mutex
	streams map[string]*wakeHandoff // by key: see inboxWatcher.streams
	pending bool
}

func recordWakePending(pending bool) {
	liveWake.mu.Lock()
	defer liveWake.mu.Unlock()
	liveWake.pending = pending
}

func currentWakePending() bool {
	liveWake.mu.Lock()
	defer liveWake.mu.Unlock()
	return liveWake.pending
}

// recordWakeStream records the credential and cursor the watcher holds for
// one agent, replacing what it held for that key.
func recordWakeStream(key, token string, since uint64) {
	liveWake.mu.Lock()
	defer liveWake.mu.Unlock()
	if liveWake.streams == nil {
		liveWake.streams = map[string]*wakeHandoff{}
	}
	liveWake.streams[key] = &wakeHandoff{Key: key, Token: token, Since: since}
}

func recordWakeCursor(key string, since uint64) {
	liveWake.mu.Lock()
	defer liveWake.mu.Unlock()
	if st := liveWake.streams[key]; st != nil && since > st.Since {
		st.Since = since
	}
}

// currentWakeStreams is every watched agent, in key order.
func currentWakeStreams() []wakeHandoff {
	liveWake.mu.Lock()
	defer liveWake.mu.Unlock()
	out := make([]wakeHandoff, 0, len(liveWake.streams))
	for _, st := range liveWake.streams {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// resetWakeStreams forgets every watched agent (tests).
func resetWakeStreams() {
	liveWake.mu.Lock()
	defer liveWake.mu.Unlock()
	liveWake.streams = nil
}

// selfIdentity is how this process recognises that its own binary changed.
//
// Size and modification time, not a hash: the file is read by the OS on exec
// and hashing it on every request would be real work for a question that only
// needs to be approximately right. A wrong answer costs one unnecessary re-exec,
// which is invisible.
type selfIdentity struct {
	path string
	size int64
	mod  time.Time
}

func currentSelf() (selfIdentity, bool) {
	path, err := os.Executable()
	if err != nil {
		return selfIdentity{}, false
	}
	st, err := os.Stat(path)
	if err != nil {
		// `task install` removes before it copies, so a stat can legitimately
		// land in the gap. Not an error: the next request looks again.
		return selfIdentity{}, false
	}
	return selfIdentity{path: path, size: st.Size(), mod: st.ModTime()}, true
}

func (a selfIdentity) differs(b selfIdentity) bool {
	return a.path != b.path || a.size != b.size || !a.mod.Equal(b.mod)
}

// deliverOwedNotice puts into the session the notice the old image owed
// and died before its cooldown timer fired; the cursor has passed the
// event. THROUGH THE WATCHER'S WAKER: a waker of its own here stood beside
// the one the restored streams write through, and a notification arriving
// during the restore put two interruptions into the session at once. Found
// by the pre-release review, round fifty-nine.
func deliverOwedNotice(w *inboxWatcher) {
	var wk *selfWaker
	if w != nil {
		wk = w.sharedWaker()
	} else {
		wk = newSelfWaker()
	}
	if wk == nil {
		return
	}
	if err := wk.wake(selfWakeNotice); err != nil {
		slog.Debug("could not deliver the notice the old image owed", "err", err)
	}
}

// carriedState is what a previous image handed us, if this process is a re-exec.
func carriedState() (bridgeState, bool) {
	raw := os.Getenv(bridgeStateEnv)
	if raw == "" {
		return bridgeState{}, false
	}
	var s bridgeState
	if json.Unmarshal([]byte(raw), &s) != nil {
		return bridgeState{}, false
	}
	return s, true
}

// carryEnv returns this process's environment with the state to hand forward.
func carryEnv(s bridgeState) ([]string, error) {
	blob, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	out := []string{fmt.Sprintf("%s=%s", bridgeStateEnv, blob)}
	for _, kv := range os.Environ() {
		if len(kv) > len(bridgeStateEnv) && kv[:len(bridgeStateEnv)+1] == bridgeStateEnv+"=" {
			continue // replaced above
		}
		out = append(out, kv)
	}
	return out, nil
}

// liveListens records every subscription this process is holding, so an exec
// can hand them on. Guarded because streams are started from the read loop and
// read here at the moment of the exec.
var liveListens struct {
	mu sync.Mutex
	by map[string]string // keyed by the request itself: re-issuing one twice is not a second subscription
}

// noteListen remembers a subscription the harness asked for.
func noteListen(line []byte) {
	liveListens.mu.Lock()
	defer liveListens.mu.Unlock()
	if liveListens.by == nil {
		liveListens.by = map[string]string{}
	}
	liveListens.by[string(line)] = string(line)
}

// openListens is what the next image must re-issue.
func openListens() []string {
	liveListens.mu.Lock()
	defer liveListens.mu.Unlock()
	out := make([]string, 0, len(liveListens.by))
	for _, v := range liveListens.by {
		out = append(out, v)
	}
	// Deterministic, so an exec loop cannot depend on map order for anything.
	sort.Strings(out)
	return out
}

// restoreCarried re-establishes whatever a previous image was holding.
//
// The handshake is restored rather than re-negotiated: MCP introduces the
// client once, on a connection this process inherited mid-flight, so there is
// no second handshake coming and without this the agent would land on the board
// anonymous after an upgrade.
//
// Subscriptions are re-issued as the caller's own request, never reconstructed
// (R12), which is the same thing followStream does across a daemon restart.
//
// sockets is [wake] sockets as THIS image read it. The self-wake watcher and
// the notice the old image owed are the bridge's half of that switch, and the
// restore used to re-arm both with no look at it: an operator who turned the
// route off and then upgraded a running bridge got a replacement that kept
// waking its session. The setting is read at start, and an in-place upgrade
// is a start. Found by the pre-release review, round twenty-eight.
func restoreCarried(ctx context.Context, client *http.Client, url, secret string,
	out *syncWriter, streams *sync.WaitGroup, w *inboxWatcher, sockets bool,
) {
	s, ok := carriedState()
	if !ok {
		return
	}
	lastClientInfo, lastWantsUI = s.ClientInfo, s.WantsUI
	if s.Thread != "" {
		noteThread(s.Thread)
	}
	if !sockets {
		if s.WakeToken != "" || len(s.WakeStreams) > 0 || s.WakePending {
			slog.Debug("[wake] sockets = false: the self-wake the old image held is not restored",
				"owed", s.WakePending)
		}
		s.WakeToken, s.WakeStreams, s.WakePending = "", nil, false
	}
	// Every stream the old image held, or the one its single field carried
	// when it predates WakeStreams.
	watched := s.WakeStreams
	if len(watched) == 0 && s.WakeToken != "" {
		watched = []wakeHandoff{{Token: s.WakeToken, Since: s.WakeSince}}
	}
	if w != nil {
		for _, st := range watched {
			w.startFor(ctx, client, url, secret, st.Key, st.Token, st.Since)
		}
	}
	if s.WakePending {
		deliverOwedNotice(w)
	}
	for _, listen := range s.Listens {
		line := []byte(listen)
		noteListen(line)
		streams.Add(1)
		go func() {
			defer streams.Done()
			followStream(ctx, client, url, secret, line, out)
		}()
	}
}
