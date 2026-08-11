// Command lanesd is the Lanes daemon: one process owning the board state,
// the ledger, the MCP endpoint, and the web UI. Loopback by default; TLS is
// automatic on any reachable address.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/agenxy/lanes/internal/blobstore"
	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/engine"
	"github.com/agenxy/lanes/internal/ledger"
	"github.com/agenxy/lanes/internal/liveness"
	"github.com/agenxy/lanes/internal/logs"
	"github.com/agenxy/lanes/internal/mcp"
	"github.com/agenxy/lanes/internal/paths"
	"github.com/agenxy/lanes/internal/web"
)

func main() {
	if err := run(); err != nil {
		// Multi-line failures go to stderr as themselves.
		//
		// slog escapes newlines into \n, so an explanation written to be read,
		// what went wrong, why it matters, and the one command that resolves
		// it: arrives as a single unreadable blob at exactly the moment
		// somebody needs to read it. Structured logging is right for the running
		// daemon's stream and wrong for its dying words.
		if msg := err.Error(); strings.Contains(msg, "\n") {
			fmt.Fprintf(os.Stderr, "lanesd: %s\n", msg)
		} else {
			slog.Error("lanesd", "err", err)
		}
		os.Exit(1)
	}
}

func run() error {
	var (
		dir = flag.String("dir", defaultDir(), "data directory")
		// Two daemons on one machine means two boards, and agents split across
		// them cannot see each other while everything still appears to work.
		// Off by default for that reason; SECURITY.md's isolation advice is the
		// case where you genuinely want it.
		allowParallel = flag.Bool("allow-parallel", false,
			"permit a second lanesd on this machine (splits the fleet across two boards; "+
				"intended for isolating agents you do not trust: see SECURITY.md)")
		addr = flag.String("addr", "",
			"listen address (override; default 127.0.0.1:4777: set a tailnet/LAN IP to serve "+
				"remote agents. TLS is handled automatically; see <dir>/lanes.toml to tune anything)")
	)
	scorer := registerScorerFlags()
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}

	// Bounded in-memory log tail: the daemon can answer "what just happened?"
	// without an unbounded file, and the board can show it. Sensitive attrs are
	// redacted at capture, so no copy ever holds a token or a message body.
	logRing := logs.NewRing(2048)
	slog.SetDefault(slog.New(logs.NewHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}), logRing,
	)))

	cfg, err := loadConfig(*dir)
	if err != nil {
		return fmt.Errorf("reading %s/lanes.toml: %w", *dir, err)
	}
	// LANES_ADDR sits between the flag and the config file, because the CLI
	// already honours it and a daemon that ignored it silently bound the
	// default instead: on a machine already running one, that is a collision
	// reported as "address already in use" for an address the operator never
	// named. Every other Lanes binary reads this variable; this one not doing so
	// was an inconsistency, not a design.
	listenAddr := firstNonEmpty(*addr, os.Getenv("LANES_ADDR"), cfg.Addr, "127.0.0.1:4777")
	scorer.applyConfig(cfg.Match)

	// Exclusivity, both kinds: this data directory, and this machine.
	release, err := claimHostSlot(listenAddr, *dir, parallelAllowed(*allowParallel))
	if err != nil {
		return err
	}
	defer release()

	nodeID, err := loadOrCreateNodeID(filepath.Join(*dir, "node_id"))
	if err != nil {
		return err
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(*dir, "key"))
	if err != nil {
		return err
	}
	secret, err := loadOrCreateSecret(filepath.Join(*dir, "local.secret"))
	if err != nil {
		return err
	}
	led, err := ledger.Open(filepath.Join(*dir, "ledger.jsonl"), nodeID, box)
	if err != nil {
		return err
	}
	defer func() { _ = led.Close() }()

	limits, err := cfg.Limits.apply(core.DefaultLimits())
	if err != nil {
		return fmt.Errorf("reading %s/lanes.toml: %w", *dir, err)
	}
	st := core.NewState(nodeID, limits)
	start := time.Now()
	// Rebuild the event ring from the ledger as it replays: without this the ring
	// starts empty and the cursor floor jumps to the restart serial, so every
	// polling agent gets E_CURSOR_TOO_OLD after a restart it had no part in.
	var history []core.Event
	led.OnEvents = func(evs []core.Event) { history = append(history, evs...) }
	// A gap is survivable and the board opens anyway, but it means some earlier
	// writer allocated a serial it never appended. Say so once per gap, at WARN,
	// so the trail exists the next time somebody asks why serials skip.
	led.OnSerialGap = func(state, ledger uint64) {
		slog.Warn("ledger serial gap: a serial was allocated but never written; "+
			"resyncing and continuing (the hash chain is intact)",
			"state", state, "ledger", ledger)
	}
	// The expected artifact of a crash mid-append, and safe to discard: the
	// daemon never answered the caller, so nothing was promised. Reported
	// anyway, because a repeat means writes are being lost for some other
	// reason and the only evidence is this line.
	led.OnTornTail = func(bytes int, at int64) {
		slog.Warn("discarded a partial final ledger record: the previous run was "+
			"interrupted mid-write; the op it described was never acknowledged to "+
			"anyone and the hash chain is intact",
			"bytes", bytes, "offset", at)
	}
	n, err := led.Replay(st)
	if err != nil {
		// A record the fold refuses is the one failure this daemon cannot shrug
		// off: state IS the ledger, so a line that will not apply means every
		// line after it describes a board that can no longer be reconstructed.
		// Refusing to start is right: continuing would serve a board that
		// silently disagrees with its own history.
		//
		// What was wrong was saying so in one wrapped Go error and exiting 1. A
		// real board did this (an op accepted live that replay rejected) and the
		// operator was left with `replay: replay apply serial 416: E_LANE_CLOSED`
		// and no next step at all: a fault with no corrective action, on the one
		// surface where the whole product is unavailable until it is resolved.
		return fmt.Errorf("%w\n\n"+
			"  This board cannot be rebuilt from its own ledger: one record will not\n"+
			"  apply, so every record after it describes a board that no longer follows.\n"+
			"  The ledger file is intact and nothing has been changed.\n\n"+
			"  See what is there:      lanes verify %s\n"+
			"  Recover the board:      lanes admin repair-ledger\n"+
			"                          (keeps every record up to the last one that\n"+
			"                          applies, archives the original beside it, and\n"+
			"                          shows you what it would discard before doing it)",
			err, filepath.Join(*dir, "ledger.jsonl"))
	}
	head := led.HeadHash()
	if len(head) > 12 {
		head = head[:12] + "…"
	} else if head == "" {
		head = "(genesis)"
	}
	slog.Info(
		"ledger replayed", "ops", n, "serial", st.Serial, "took", time.Since(start).Round(time.Millisecond), "head",
		head,
	)

	bs, err := blobstore.New(*dir, box)
	if err != nil {
		return err
	}
	led.OnEvents = nil // replay is done; live events flow through the engine
	eng := engine.New(st, led, liveness.New(), history)
	eng.SetBlobs(bs)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go eng.Run(ctx)
	// Watches this machine for subagents that have stopped working and tells
	// the lane that spawned them. Its own goroutine: it forks ps, and the
	// writer loop must never wait on a process scan. It reports and never acts.
	// Roles the operator declared. On the admin path, because the daemon IS the
	// admin path: an agent still cannot promote itself.
	keepDeclaredRolesApplied(ctx, eng, cfg.Roles)
	startSupervision(ctx, eng, cfg.Supervise)
	// Indexing shells out to git across thousands of commits; the daemon must be
	// answering agents long before that finishes, so it lands asynchronously.
	scorer.install(ctx, eng)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.New(eng))
	ws, err := web.New(eng)
	if err != nil {
		return err
	}
	ws.Register(mux)
	registerLogsAPI(mux, logRing)
	registerMatchStatusAPI(mux, eng)
	registerAdminAPI(mux, eng)

	tr, err := resolveTransport(*dir, listenAddr, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: listenAddr, Handler: newAuthGate(secret, filepath.Join(*dir, "admin.hash")).wrap(mux),
		ReadHeaderTimeout: 5 * time.Second,
		// No global write timeout: long-polls and SSE hold connections open.
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	scheme := "http"
	if tr.certFile != "" {
		scheme = "https"
	}
	// BIND FIRST, announce second.
	//
	// ListenAndServe binds and serves in one call, so "lanesd up" was logged
	// before anything had been bound, and a port collision then printed a
	// confident success line immediately followed by the failure. That is not
	// cosmetic: it fooled this project's own setup checks twice, which grepped
	// for "lanesd up", found it, and proceeded to talk to a DIFFERENT daemon
	// that already held the port. An operator reading a log has the same
	// problem. Nothing claims to be up until the socket is ours.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	slog.Info("lanesd up", "addr", listenAddr, "node", nodeID, "dir", *dir, "transport", tr.why,
		"mcp", scheme+"://"+listenAddr+"/mcp", "board", scheme+"://"+listenAddr+"/",
		"hint", "run `lanes web` for a board link, `lanes mcp-config` for the agent config")
	serve := func() error { return srv.Serve(ln) }
	if tr.certFile != "" {
		serve = func() error { return srv.ServeTLS(ln, tr.certFile, tr.keyFile) }
	}
	if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// isLoopbackAddr reports whether a listen address is confined to this machine.
// Anything else is reachable by other hosts and must be encrypted.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false // wildcard binds every interface
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// firstNonEmpty implements the precedence flag > config file > built-in default.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func defaultDir() string { return paths.DataDir() }

// loadOrCreateNodeID returns the stable node identity (v1 federation tax).
func loadOrCreateNodeID(path string) (string, error) {
	// The node-id file inside the daemon's own data directory.
	// #nosec G304
	b, err := os.ReadFile(path)
	if err == nil && len(b) > 0 {
		return string(b), nil
	}
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// startSupervision begins watching for stalled subagents, unless it is off.
//
// Lifted out of run() because run() had reached its complexity budget, and
// because "is this feature on" is a decision worth reading on its own.
func startSupervision(ctx context.Context, eng *engine.Engine, c liveness.Settings) {
	if c.Off {
		slog.Info("stalled-subagent detection is off",
			"note", "`lanes probe --pid N` still answers on demand")
		return
	}
	go eng.Supervise(ctx, superviseSettings(c))
}
