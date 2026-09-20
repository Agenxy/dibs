package engine

import (
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Waking an agent on ANOTHER machine.
//
// The hub decides THAT an agent should be woken; the agent's own machine
// decides HOW (docs/NETWORK.md §5, WAKE-MECHANISMS.md §5a). Every decision in
// waker.go, the cooldown, the deferral, the recency window, the attempt count
// and the exit re-check, was paid for by a defect, and none of it depends on
// where the command runs. So the split is at execution alone: a remote agent
// gets a wakePlan with no argv and a host, runWake hands that plan to the
// bridge attached for the host, and the bridge's report of the outcome is the
// exit status the hub would otherwise have observed itself.
//
// The bridge on the other machine is `dibs host-bridge`. It subscribes to
// dibs://wake, stating the host it is and the harnesses its own [wake.exec]
// table can start, runs the operator's command THERE with the same
// substitutions the hub applies locally, and posts the result back. The hub
// therefore never learns a remote argv and never runs one: a hub that could
// make another machine execute a command it chose would be remote code
// execution with a friendlier name, which rule 5 refuses.
//
// What the bridge states is as strong as the bearer secret that let it in and
// no stronger: a bridge can attach for a host it is not on, and what that buys
// it is receiving the sentence "Dibs: check the board." meant for that host's
// agents, plus the thread ids their harnesses resume. SECURITY.md carries the
// row; §6 of NETWORK.md is where that becomes proved rather than asserted.

// WakeRequest is one wake the hub decided on for an agent on another machine,
// handed to that machine's bridge to run: exactly the substitutions a
// [wake.exec] entry there may take, and nothing an agent wrote.
type WakeRequest struct {
	ID      uint64 `json:"id"`
	Host    string `json:"host"`
	Agent   string `json:"agent"`
	Harness string `json:"harness"`
	Thread  string `json:"thread"`
	CWD     string `json:"cwd"`
	From    string `json:"from"`
	MsgType string `json:"msg_type"`
	Notice  string `json:"notice"`
}

// WakeResult is the bridge's report on one request: the exit status the hub
// would have seen had the command run here.
type WakeResult struct {
	ID     uint64 `json:"id"`
	Host   string `json:"host"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// HostBridgeInfo describes one attached bridge, for doctor.
type HostBridgeInfo struct {
	Host      string    `json:"host"`
	Harnesses []string  `json:"harnesses"`
	Since     time.Time `json:"since"`
}

type hostBridge struct {
	harnesses map[string]bool
	since     time.Time
	ch        chan WakeRequest
}

// hostWakes is the hub's side of the split: which hosts have a bridge attached
// and what each can start, plus the requests in flight awaiting a report.
type hostWakes struct {
	mu      sync.Mutex
	bridges map[string]*hostBridge
	pending map[uint64]pendingWake
	// next is the last wake id handed out. Seeded from the clock on first
	// use rather than from zero: a bridge that outlives a hub restart still
	// holds the ids of commands it is running, and a restarted counter
	// handed the same small numbers to new requests, which the bridge then
	// dropped as duplicates and the old commands' reports satisfied. Found
	// by the pre-release review.
	next uint64
}

// pendingWake is a request the hub is waiting on, and the host it went to:
// only that host's bridge may report it, and only that host's detaching
// fails it.
type pendingWake struct {
	host string
	ch   chan WakeResult
}

// wakeRequestBuffer bounds what a bridge that has stopped reading can hold
// up: past it a request fails at once, which the waker treats like a command
// that failed to start (one deferral, no spent attempt).
const wakeRequestBuffer = 16

// ErrNoHost is AttachHostBridge's answer to a bridge that did not say which
// machine it is.
var ErrNoHost = errors.New("a host bridge must state the host it is on")

// AttachHostBridge registers the bridge for host and returns the channel its
// wake requests arrive on and the function that detaches it.
//
// One bridge per host: a second attach REPLACES the first, closing its
// channel, because the first is most often a stale stream from a bridge that
// reconnected, and refusing the live one for the dead one's sake would leave
// the host unreachable until a timeout nobody configured. Detaching fails
// every request in flight for the host, so a wake waiting on a report is
// released as a failure rather than held for the wake timeout.
func (e *Engine) AttachHostBridge(host string, harnesses []string) (<-chan WakeRequest, func(), error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, nil, ErrNoHost
	}
	hw := &e.hostWakes
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if hw.bridges == nil {
		hw.bridges = map[string]*hostBridge{}
	}
	if old := hw.bridges[host]; old != nil {
		close(old.ch)
	}
	b := &hostBridge{harnesses: map[string]bool{}, since: time.Now(), ch: make(chan WakeRequest, wakeRequestBuffer)}
	for _, h := range harnesses {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			b.harnesses[h] = true
		}
	}
	hw.bridges[host] = b
	release := func() {
		hw.mu.Lock()
		defer hw.mu.Unlock()
		if hw.bridges[host] != b {
			return // replaced already; the replacement owns the host now
		}
		delete(hw.bridges, host)
		close(b.ch)
		for id, p := range hw.pending {
			if p.host != host {
				continue // another host's wake, still running there
			}
			select {
			case p.ch <- WakeResult{ID: id, Host: host, OK: false, Detail: "the host's bridge detached before reporting"}:
			default:
			}
		}
	}
	return b.ch, release, nil
}

// HostBridges lists the attached bridges, oldest first.
func (e *Engine) HostBridges() []HostBridgeInfo {
	hw := &e.hostWakes
	hw.mu.Lock()
	defer hw.mu.Unlock()
	out := make([]HostBridgeInfo, 0, len(hw.bridges))
	for host, b := range hw.bridges {
		hs := make([]string, 0, len(b.harnesses))
		for h := range b.harnesses {
			hs = append(hs, h)
		}
		sort.Strings(hs)
		out = append(out, HostBridgeInfo{Host: host, Harnesses: hs, Since: b.since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// ReportWakeResult delivers a bridge's report to the wake waiting on it.
// False when nothing is waiting: an unknown id, a report for a request the
// hub already gave up on, or a bridge reporting for a host it did not run.
func (e *Engine) ReportWakeResult(res WakeResult) bool {
	hw := &e.hostWakes
	hw.mu.Lock()
	p, ok := hw.pending[res.ID]
	hw.mu.Unlock()
	if !ok || p.host != strings.TrimSpace(res.Host) {
		return false // nothing waiting, or a host reporting a wake it did not run
	}
	select {
	case p.ch <- res:
		return true
	default:
		return false
	}
}

// remoteHostOf is the machine this agent is on when that is NOT this one, or
// "" for a local agent and for one that never said (unknown behaves as it
// always did: local).
func (e *Engine) remoteHostOf(l *core.Agent) string {
	if l == nil || l.Agent == nil || l.Agent.HostID == "" || e.state == nil {
		return ""
	}
	// THE HOST ID, not the ledger's node id: on a Supgang member the hub's
	// own agents are stamped with the Supgang node id, and comparing with
	// the ledger's would make every one of them look remote.
	if l.Agent.HostID == e.HostID() {
		return ""
	}
	return l.Agent.HostID
}

// hostRouteFor reports the host whose attached bridge can start this agent's
// harness, for an agent on another machine. ok false for a local agent, and
// for a remote one whose host has no bridge attached or no entry for the
// harness: the hub's own [wake.exec] is never a route to another machine.
func (e *Engine) hostRouteFor(l *core.Agent) (host string, ok bool) {
	host = e.remoteHostOf(l)
	if host == "" {
		return "", false
	}
	hw := &e.hostWakes
	hw.mu.Lock()
	defer hw.mu.Unlock()
	b := hw.bridges[host]
	if b == nil || !b.harnesses[wakeHarness(l)] {
		return host, false
	}
	return host, true
}

// requestRemoteWake hands the plan to the host's bridge and waits for its
// report, which stands in for the exit status of a local command. The wait is
// bounded by the same timeout a local command gets; a bridge that detaches
// meanwhile fails the request at once.
func (e *Engine) requestRemoteWake(plan wakePlan, agent string) bool {
	return e.requestRemoteWakeWithin(plan, agent, wakeTimeout+wakeGrace)
}

func (e *Engine) requestRemoteWakeWithin(plan wakePlan, agent string, within time.Duration) bool {
	hw := &e.hostWakes
	hw.mu.Lock()
	b := hw.bridges[plan.host]
	if b == nil {
		hw.mu.Unlock()
		slog.Info("no wake: the agent's host has no bridge attached",
			"agent", agent, "host", plan.host)
		return false
	}
	if hw.next == 0 {
		hw.next = uint64(time.Now().UnixNano())
	}
	hw.next++
	req := plan.request
	req.ID = hw.next
	result := make(chan WakeResult, 1)
	if hw.pending == nil {
		hw.pending = map[uint64]pendingWake{}
	}
	hw.pending[req.ID] = pendingWake{host: plan.host, ch: result}
	hw.mu.Unlock()
	defer func() {
		hw.mu.Lock()
		delete(hw.pending, req.ID)
		hw.mu.Unlock()
	}()

	sent := false
	func() {
		defer func() { _ = recover() }() // the channel closes when the bridge detaches
		select {
		case b.ch <- req:
			sent = true
		default:
		}
	}()
	if !sent {
		slog.Info("no wake: the host's bridge is not reading its requests",
			"agent", agent, "host", plan.host)
		return false
	}
	slog.Info("wake handed to the agent's host", "agent", agent, "host", plan.host,
		"harness", req.Harness, "request", req.ID)
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case res := <-result:
		if res.OK {
			slog.Info("the agent's host reports the wake ran", "agent", agent, "host", plan.host, "request", req.ID)
			return true
		}
		slog.Info("the agent's host reports the wake failed", "agent", agent, "host", plan.host,
			"request", req.ID, "detail", res.Detail)
		return false
	case <-timer.C:
		slog.Info("the agent's host never reported on the wake", "agent", agent, "host", plan.host, "request", req.ID)
		return false
	}
}
