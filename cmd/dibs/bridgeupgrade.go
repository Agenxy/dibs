package main

import (
	"context"
	"encoding/json"
	"fmt"
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
func restoreCarried(ctx context.Context, client *http.Client, url, secret string,
	out *syncWriter, streams *sync.WaitGroup,
) {
	s, ok := carriedState()
	if !ok {
		return
	}
	lastClientInfo, lastWantsUI = s.ClientInfo, s.WantsUI
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
