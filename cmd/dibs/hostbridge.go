package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// hostBridge is `dibs host-bridge`: the other half of a wake on another
// machine (docs/NETWORK.md §5, WAKE-MECHANISMS.md §5a).
//
// The hub decides THAT an agent should be woken; the agent's own machine
// decides HOW. This is the machine's half. It reads the [wake.exec] table in
// ITS OWN data directory (the one `dibs mcp-config --board` made for this
// board), tells the hub which machine it is and which harnesses that table
// can start, and holds the dibs://wake stream open. Each wake the hub
// decides on for an agent here arrives with exactly what a [wake.exec]
// entry substitutes: the harness thread, the agent, the sender, the mail
// type, and the one fixed sentence. The command is the operator's, run here
// by the same runner the daemon uses for its own agents, and the exit
// status goes back as the report the hub treats as its own observation.
//
// The hub never learns this machine's argv and never chooses it. A hub that
// could would be remote code execution with a friendlier name, which rule 5
// refuses; and the sentence is checked on this side too, because a hub that
// holds the secret could send another one, and an argv that delivers text
// into a harness should never deliver text a hub composed.
func hostBridge(args []string) error {
	// Every argument is read before anything is decided: `--service --help`
	// must print help and write nothing, the rule for every command that
	// writes outside the data directory.
	service, help := false, false
	for _, a := range args {
		switch a {
		case "--service":
			service = true
		case "-h", "--help", "help":
			help = true
		default:
			return fmt.Errorf("`dibs host-bridge` takes --service or nothing, and %q is not either", a)
		}
	}
	if help {
		fmt.Println("usage: dibs host-bridge [--service]")
		fmt.Println()
		fmt.Println("  Runs this machine's own [wake.exec] commands for its agents on a board")
		fmt.Println("  served elsewhere. Set DIBS_ADDR and DIBS_DIR as `dibs mcp-config --board`")
		fmt.Println("  printed; the [wake.exec] table is read from that DIBS_DIR's dibs.toml.")
		fmt.Println()
		fmt.Println("  --service writes a launchd/systemd unit that keeps the bridge running")
		fmt.Println("  across logins and reboots, and prints the command to load it.")
		return nil
	}
	if service {
		return hostBridgeUnit()
	}
	if err := checkConfigReadable(); err != nil {
		return err
	}
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret in %s: copy the board's secret there first, as the "+
			"recipe from `dibs mcp-config --board` says: %w", paths.DataDir(), err)
	}
	routes, err := localWakeRoutes(paths.DataDir())
	if err != nil {
		return err
	}
	host := hostID()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	b := newWakeBridge(boardOrigin(), secret, host, routes)
	slog.Info("host bridge attaching", "board", b.origin, "host", host, "harnesses", b.harnesses())
	return b.follow(ctx)
}

// localWakeRoutes is this machine's [wake.exec] table, keyed by lowercased
// harness. Empty is refused: a bridge that can start nobody would attach,
// be handed nothing, and read as coverage in the hub's doctor.
func localWakeRoutes(dir string) (map[string]boardconfig.WakeExec, error) {
	cfg, err := boardconfig.Load(dir)
	if err != nil {
		return nil, err
	}
	routes := map[string]boardconfig.WakeExec{}
	for h, x := range cfg.Wake.Exec {
		routes[strings.ToLower(strings.TrimSpace(h))] = x
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("nothing to run: %s has no [wake.exec] entries, so this bridge could start "+
			"no agent here. Add a [wake.exec.<harness>] block to it (docs/CONFIGURATION.md) and run this again",
			filepath.Join(dir, "dibs.toml"))
	}
	return routes, nil
}

// wakeBridge is one attachment: where the hub is, who we are, what we can
// start, and how a command is run (replaceable by a test).
type wakeBridge struct {
	origin  string
	secret  string
	host    string
	routes  map[string]boardconfig.WakeExec
	client  *http.Client // reports
	streams *http.Client // the listen, with no deadline
	run     func(argv, fallback []string, agent, dir string, timeout, grace time.Duration) bool
	// EVERYTHING A HUB CAN MAKE THIS MACHINE DO IS BOUNDED HERE. slots
	// bounds how many wake commands run at once; reports bounds the
	// refusals waiting to be posted, drained by one goroutine; running and
	// seen are the ids in flight and already handled, so a replay starts
	// nothing. The operator's command is the operator's, but the number of
	// them, and the memory a flood can occupy, is this machine's to bound. A
	// request past the bound is reported as a failure at once, which the hub
	// treats like a command that failed to start (one deferral, no spent
	// attempt); a refusal that cannot even be queued is logged and dropped,
	// and the hub's own timeout reads that as the same failure.
	slots   chan struct{}
	reports chan engine.WakeResult
	mu      sync.Mutex
	running map[uint64]struct{}
	seen    map[uint64]struct{}
	order   []uint64
	// busy is the agent each running command is for, by agent id. One
	// command per agent at a time, whatever the request id: the hub fails a
	// pending request when its stream reconnects and retries under a new id
	// while this machine's first command is still running, and a second
	// resume against a thread that is mid-turn is the overlap the whole wake
	// path promises not to produce. Round nine of the pre-release review.
	busy map[string]uint64
}

// maxConcurrentWakes is how many wake commands one bridge runs at once.
// Each is a harness turn; four is more than a machine's agents receive
// blocking mail at the same instant, and far fewer than a flood.
const maxConcurrentWakes = 4

// reportQueue is how many refusals may wait to be posted: past it, a hub
// that floods faster than it takes reports is told nothing and holds
// nothing here either.
const reportQueue = 64

// rememberedWakes is how many handled request ids the bridge keeps to
// refuse a replay: within one stream the hub's ids only grow, so anything
// older is not coming back. An id still running is kept regardless.
const rememberedWakes = 256

func newWakeBridge(origin, secret, host string, routes map[string]boardconfig.WakeExec) *wakeBridge {
	return &wakeBridge{
		origin:  origin,
		secret:  secret,
		host:    host,
		routes:  routes,
		client:  daemonClient(30 * time.Second),
		streams: daemonClient(0),
		run:     wakeexec.RunCommands,
		slots:   make(chan struct{}, maxConcurrentWakes),
		reports: make(chan engine.WakeResult, reportQueue),
		running: map[uint64]struct{}{},
		seen:    map[uint64]struct{}{},
		busy:    map[string]uint64{},
	}
}

// admit says whether a request id is new: not running now, not handled
// within memory. A new id is recorded as running when it takes a slot
// (started) and as handled otherwise.
func (b *wakeBridge) admit(id uint64, started bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.running[id]; dup {
		return false
	}
	if _, dup := b.seen[id]; dup {
		return false
	}
	if started {
		b.running[id] = struct{}{}
		return true
	}
	b.remember(id)
	return true
}

// engaged reports whether a command for this agent is still running here,
// and claims the agent for id when not. Caller has taken a slot.
func (b *wakeBridge) engaged(id uint64, agent string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, running := b.busy[agent]; running {
		return true
	}
	b.busy[agent] = id
	return false
}

// finish moves a running id to the handled set and frees its agent.
func (b *wakeBridge) finish(id uint64, agent string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.running, id)
	if b.busy[agent] == id {
		delete(b.busy, agent)
	}
	b.remember(id)
}

func (b *wakeBridge) remember(id uint64) {
	b.seen[id] = struct{}{}
	b.order = append(b.order, id)
	if len(b.order) > rememberedWakes {
		delete(b.seen, b.order[0])
		b.order = b.order[1:]
	}
}

// newStream forgets handled ids: a stream is one hub instance, whose ids
// only grow, and the next instance after a restart starts counting again.
// Running ids are kept, so a request the old hub handed out cannot be
// started twice by the new one either.
func (b *wakeBridge) newStream() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = map[uint64]struct{}{}
	b.order = nil
}

// reporter posts queued refusals, one at a time, for the life of the
// bridge: the bound on what a flood can hold open.
func (b *wakeBridge) reporter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case res := <-b.reports:
			if err := b.report(ctx, res); err != nil {
				slog.Warn("the hub did not take the report", "request", res.ID, "err", err)
			}
		}
	}
}

func (b *wakeBridge) harnesses() []string {
	out := make([]string, 0, len(b.routes))
	for h := range b.routes {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// cooldowns is what this machine's [wake.exec] table says per harness, for
// the hub to spend instead of its own default: a `cooldown = "30m"` here
// used to be ninety seconds there. Round nine of the pre-release review.
func (b *wakeBridge) cooldowns() map[string]string {
	out := map[string]string{}
	for h, x := range b.routes {
		if x.Cooldown > 0 {
			out[h] = x.Cooldown.String()
		}
	}
	return out
}

// listenBody is the one request this bridge makes of the MCP endpoint: a
// listen on dibs://wake stating the host and the harnesses.
func (b *wakeBridge) listenBody() []byte {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "subscriptions/listen",
		"params": map[string]any{
			"notifications": map[string]any{"resourceSubscriptions": []string{mcp.WakeURI}},
			"_meta": map[string]any{
				mcp.HostMetaKey:          b.host,
				mcp.WakeHarnessesMetaKey: b.harnesses(),
				mcp.WakeCooldownsMetaKey: b.cooldowns(),
			},
		},
	})
	return body
}

// follow holds the stream open for as long as the process lives,
// reconnecting when it ends. A hub that is restarting closes it; a hub that
// is gone refuses the dial; either way the bridge keeps asking, with a pause
// that grows to half a minute, because a machine's agents stay reachable
// only while this is attached and nobody restarts a bridge by hand at 3am.
// A REFUSAL is different: an answer rather than a stream means the hub read
// the listen and said no, and asking again does not change its mind.
func (b *wakeBridge) follow(ctx context.Context) error {
	go b.reporter(ctx)
	pause := time.Second
	for {
		err := b.attachOnce(ctx)
		select {
		case <-ctx.Done():
			return nil // stopped on purpose, whatever the last attach said
		default:
		}
		var refused *listenRefused
		if errors.As(err, &refused) {
			return refused
		}
		if err != nil {
			slog.Warn("the wake stream ended; attaching again", "err", err, "in", pause)
		} else {
			slog.Info("the wake stream ended; attaching again", "in", pause)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pause):
		}
		if pause *= 2; pause > 30*time.Second {
			pause = 30 * time.Second
		}
	}
}

// listenRefused is the hub's answer when it would not open the stream.
type listenRefused struct{ msg string }

func (r *listenRefused) Error() string { return "the hub refused the wake stream: " + r.msg }

// attachOnce opens the stream and serves requests until it ends.
func (b *wakeBridge) attachOnce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.origin+"/mcp", bytes.NewReader(b.listenBody()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dibs-Local", b.secret)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := b.streams.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if !isEventStream(resp) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// A hub that is there and not ready (restarting behind a proxy, a 502
		// or 503) is asked again; a hub that read the listen and said no (a
		// JSON-RPC error, a 4xx) has answered, and asking again does not
		// change its mind.
		if resp.StatusCode >= 500 {
			return fmt.Errorf("the hub answered HTTP %d: %s", resp.StatusCode, excerptOf(body))
		}
		return &listenRefused{msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, excerptOf(body))}
	}
	attached := false
	eachSSEData(resp.Body, func(data []byte) {
		var msg struct {
			Method string `json:"method"`
			Params struct {
				URI  string             `json:"uri"`
				Meta engine.WakeRequest `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(data, &msg) != nil {
			return
		}
		switch msg.Method {
		case "notifications/subscriptions/acknowledged":
			attached = true
			b.newStream()
			slog.Info("attached: the hub will hand this machine its agents' wakes",
				"host", b.host, "harnesses", b.harnesses())
		case "notifications/resources/updated":
			if msg.Params.URI == mcp.WakeURI {
				b.dispatch(ctx, msg.Params.Meta)
			}
		}
	})
	if !attached {
		return errors.New("the stream closed before the hub acknowledged the listen")
	}
	return nil
}

// dispatch admits one request: a replay is dropped (the hub refuses a
// second report on one id anyway), a request past the concurrency bound is
// reported as not run, and the rest each get a goroutine holding a slot.
func (b *wakeBridge) dispatch(ctx context.Context, wr engine.WakeRequest) {
	select {
	case b.slots <- struct{}{}:
		if !b.admit(wr.ID, true) {
			<-b.slots
			slog.Warn("a wake request arrived twice; the second is ignored", "request", wr.ID)
			return
		}
		if b.engaged(wr.ID, wr.Agent) {
			<-b.slots
			b.finish(wr.ID, "")
			slog.Warn("wake refused: a command for this agent is still running here",
				"request", wr.ID, "agent", wr.Agent)
			b.refuse(engine.WakeResult{
				ID: wr.ID, Host: b.host, OK: false,
				Detail: "this machine's bridge is still running a wake command for " + wr.Agent,
			})
			return
		}
	default:
		if !b.admit(wr.ID, false) {
			slog.Warn("a wake request arrived twice; the second is ignored", "request", wr.ID)
			return
		}
		slog.Warn("wake refused: this bridge is already running its bound of wake commands",
			"request", wr.ID, "agent", wr.Agent, "bound", maxConcurrentWakes)
		b.refuse(engine.WakeResult{
			ID: wr.ID, Host: b.host, OK: false,
			Detail: fmt.Sprintf("this machine's bridge is already running %d wake commands", maxConcurrentWakes),
		})
		return
	}
	go func() {
		defer func() { <-b.slots }()
		b.serve(ctx, wr)
	}()
}

// refuse queues a not-run report, or drops it when the queue is full.
func (b *wakeBridge) refuse(res engine.WakeResult) {
	select {
	case b.reports <- res:
	default:
		slog.Warn("wake refused and the refusal dropped: the hub is sending faster than it takes reports",
			"request", res.ID)
	}
}

// serve runs one wake the hub decided on and reports how it went.
func (b *wakeBridge) serve(ctx context.Context, wr engine.WakeRequest) {
	res := engine.WakeResult{ID: wr.ID, Host: b.host}
	res.OK, res.Detail = b.execute(wr)
	// The agent is free BEFORE the hub hears the outcome: a retry the hub
	// sends on reading the report must not find the agent still engaged.
	b.finish(wr.ID, wr.Agent)
	if res.OK {
		slog.Info("woke", "agent", wr.Agent, "harness", wr.Harness, "request", wr.ID)
	} else {
		slog.Warn("wake failed", "agent", wr.Agent, "harness", wr.Harness, "request", wr.ID, "detail", res.Detail)
	}
	if err := b.report(ctx, res); err != nil {
		slog.Warn("the hub did not take the report", "request", wr.ID, "err", err)
	}
}

// execute is the decision of what to run for one request, and the running
// of it. Returns whether the wake ran, with the reason when it did not.
func (b *wakeBridge) execute(wr engine.WakeRequest) (bool, string) {
	if wr.Host != b.host {
		return false, "the request names host " + wr.Host + " and this bridge is " + b.host
	}
	x, ok := b.routes[strings.ToLower(wr.Harness)]
	if !ok {
		return false, "this machine has no [wake.exec] entry for harness " + wr.Harness
	}
	if wr.Thread == "" {
		return false, "no harness thread to resume"
	}
	// THE ONE SENTENCE, decided here and not by the hub. Every wake carries
	// it; a request carrying anything else is a hub composing text for a
	// command that delivers text into a harness, which is the line rule 5
	// draws. The wake still runs, with the sentence, and the substitution is
	// logged.
	notice := wr.Notice
	if notice != wakeexec.Notice {
		slog.Warn("the hub sent a wake with a notice that is not the fixed sentence; sending the sentence",
			"request", wr.ID, "sent", notice)
		notice = wakeexec.Notice
	}
	f := wakeexec.Fields{Thread: wr.Thread, Agent: wr.Agent, From: wr.From, MsgType: wr.MsgType, Message: notice}
	var fallback []string
	if len(x.Fallback) > 0 {
		fallback = f.Apply(x.Fallback)
	}
	if b.run(f.Apply(x.Argv), fallback, wr.Agent, wr.CWD, wakeexec.Timeout, wakeexec.Grace) {
		return true, ""
	}
	return false, "the wake command exited non-zero (the fallback too, when one is configured)"
}

// report posts the outcome; the hub accepts a report once, for a request it
// handed out and is still waiting on.
func (b *wakeBridge) report(ctx context.Context, res engine.WakeResult) error {
	body, _ := json.Marshal(res)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.origin+"/api/wake-result", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dibs-Local", b.secret)
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, excerptOf(msg))
	}
	return nil
}

// eachSSEData hands every `data:` line of an event stream to fn, ignoring
// keepalive comments and blank lines, until the stream ends.
func eachSSEData(body io.Reader, fn func(data []byte)) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := bytes.TrimRight(sc.Bytes(), "\r")
		data, found := bytes.CutPrefix(line, []byte("data: "))
		if !found {
			continue
		}
		if data = bytes.TrimSpace(data); len(data) > 0 {
			fn(data)
		}
	}
}
