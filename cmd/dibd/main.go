// Command dibd is the Dibs daemon: one process owning the board state,
// the ledger, the MCP endpoint, and the web UI. Loopback by default; TLS is
// automatic on any reachable address.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
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

	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/liveness"
	"github.com/agenxy/dibs/internal/logs"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/web"
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
			fmt.Fprintf(os.Stderr, "dibd: %s\n", msg)
		} else {
			slog.Error("dibd", "err", err)
		}
		os.Exit(1)
	}
}

// registerDaemonFlags declares every flag in one place, so the manual can walk
// them rather than repeat them. A flag added here appears in `dibd -man`
// without anybody remembering to document it, which is the only arrangement
// that stays true.
// daemonOpts are the daemon's own flags, declared once so the manual walks the
// same declarations run() parses. A second copy for documentation is a second
// copy that loses.
type daemonOpts struct {
	dir           *string
	allowParallel *bool
	addr          *string
	check         *bool
}

func registerDaemonFlags(fs *flag.FlagSet) (*daemonOpts, *scorerFlags) {
	o := &daemonOpts{
		dir: fs.String("dir", defaultDir(), "data directory"),
		// Two daemons on one machine means two boards, and agents split across
		// them cannot see each other while everything still appears to work.
		// Off by default for that reason; SECURITY.md's isolation advice is the
		// case where you genuinely want it.
		allowParallel: fs.Bool("allow-parallel", false,
			"permit a second dibd on this machine (splits the fleet across two boards; "+
				"intended for isolating agents you do not trust: see SECURITY.md)"),
		addr: fs.String("addr", "",
			"listen address (override; default 127.0.0.1:4777: set a tailnet/LAN IP to serve "+
				"remote agents. TLS is handled automatically; see <dir>/dibs.toml to tune anything)"),
		// The one question an upgrade has to answer BEFORE it stops anything.
		check: fs.Bool("check", false,
			"replay the board at -dir and exit, without serving: answers whether THIS build "+
				"could take over from the daemon now running. `dibs upgrade` runs it first"),
	}
	fs.Bool("man", false, "write this daemon's manual page (mdoc) to stdout and exit")
	return o, registerScorerFlagsOn(fs)
}

func run() error {
	// `-man` before anything else: it must work with no data directory, no
	// configuration and no daemon, which is how somebody reads a manual.
	if wantsMan() {
		return manCmd(manArgs())
	}
	opts, scorer := registerDaemonFlags(flag.CommandLine)
	dir, allowParallel, addr, check := opts.dir, opts.allowParallel, opts.addr, opts.check
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
		return fmt.Errorf("reading %s/dibs.toml: %w", *dir, err)
	}
	// Before the host slot, the listener, and anything else with a side effect.
	// The whole value of this mode is that it can run against a board a
	// different daemon is currently serving.
	if *check {
		return checkReplay(*dir, cfg)
	}
	// DIBS_ADDR sits between the flag and the config file, because the CLI
	// already honours it and a daemon that ignored it silently bound the
	// default instead: on a machine already running one, that is a collision
	// reported as "address already in use" for an address the operator never
	// named. Every other Dibs binary reads this variable; this one not doing so
	// was an inconsistency, not a design.
	listenAddr := firstNonEmpty(*addr, os.Getenv("DIBS_ADDR"), cfg.Addr, "127.0.0.1:4777")
	// A scheme is a CLIENT's grammar, and this is a listen address.
	//
	// DIBS_ADDR may carry http:// or https:// because a client needs to say what
	// to speak to a remote board. net.Listen takes host:port and answers "too
	// many colons in address", after the daemon has already announced itself and
	// claimed the host slot. Reading the variable here was right; passing its
	// scheme through to Listen was not. Raised by the pre-release review, which
	// noted the test for it grepped main.go for the variable's name and stayed
	// green while the shipped daemon could not use the value.
	// The scheme is REMEMBERED, not just stripped. Removing it made net.Listen
	// work and left the transport decision to be re-inferred from the bare
	// address, which is how a daemon told to serve http:// on a LAN address
	// served TLS while its clients spoke plaintext.
	var askedScheme string
	if scheme, rest, found := strings.Cut(listenAddr, "://"); found {
		switch strings.ToLower(scheme) {
		case "http", "https":
			askedScheme, listenAddr = strings.ToLower(scheme), rest
		default:
			return fmt.Errorf("DIBS_ADDR=%q names a scheme Dibs does not speak: use "+
				"http:// or https://, or give a bare host:port", listenAddr)
		}
	}
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

	limits, err := applyLimits(cfg.Limits, core.DefaultLimits())
	if err != nil {
		return fmt.Errorf("reading %s/dibs.toml: %w", *dir, err)
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
	// Before the fold, not after it. A ledger from an older release can replay
	// perfectly and still be wrong: see oldVocabularyFailure.
	if old := oldVocabularyFailure(*dir); old != nil {
		return old
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
		// operator was left with `replay: replay apply serial 416: E_AGENT_CLOSED`
		// and no next step at all: a fault with no corrective action, on the one
		// surface where the whole product is unavailable until it is resolved.
		return fmt.Errorf("%w\n\n"+
			"  This board cannot be rebuilt from its own ledger: one record will not\n"+
			"  apply, so every record after it describes a board that no longer follows.\n"+
			"  The ledger file is intact and nothing has been changed.\n\n"+
			"  See what is there:      dibs verify %s\n"+
			"  Recover the board:      dibs admin repair-ledger\n"+
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
	wake, err := wakePolicy(cfg.Wake)
	if err != nil {
		return fmt.Errorf("reading %s/dibs.toml: %w", *dir, err)
	}
	eng.SetWakePolicy(wake)
	// On unless the operator opted out: see WakeConfig.NoticesWake. A pointer so
	// "unset" and "explicitly false" are distinguishable, which is the whole
	// reason a bool setting with a true default needs one.
	eng.SetNoticesWake(cfg.Wake.NoticesWake == nil || *cfg.Wake.NoticesWake)
	// How to REACH an agent that is not running. Operator's config only: there
	// is no tool, op or admin route that can set this, because it is arbitrary
	// code on this machine and only the person at it may name it.
	if len(cfg.Wake.Exec) > 0 {
		// Each harness keeps its OWN cooldown. Collapsing the table to its
		// largest value, which this did, let one cautious entry throttle every
		// other harness while both settings still read as configured.
		cmds := make(map[string]engine.WakeCommand, len(cfg.Wake.Exec))
		for harness, x := range cfg.Wake.Exec {
			if len(x.Argv) == 0 {
				continue
			}
			cmds[harness] = engine.WakeCommand{Argv: x.Argv, Cooldown: x.Cooldown}
		}
		eng.SetWakeCommands(cmds)
		slog.Info("the board can start an agent that is not running",
			"harnesses", len(cmds))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go eng.Run(ctx)
	// Watches this machine for subagents that have stopped working and tells
	// the agent that spawned them. Its own goroutine: it forks ps, and the
	// writer loop must never wait on a process scan. It reports and never acts.
	// Roles the operator declared. On the admin path, because the daemon IS the
	// admin path. That was not enough on its own: the config names a STRING, and
	// an agent that took the name got the role, which is self-promotion by any
	// other word. The grant now pins the credential of the agent it lands on and
	// closes its window shortly after start. See rolepin.go.
	keepDeclaredRolesApplied(ctx, *dir, eng, cfg.Roles)
	// Clears a pid an older build recorded against the operator's own row, which
	// made every restart report them as a dead process. One op, once, and only
	// on a board that already has a human on it.
	eng.RepairHumanProcess(ctx)
	// Tell the coordinator, once, if this machine cannot get a notification in
	// front of the person. Off the boot path: it shells out to the notifier, and
	// the daemon must be answering agents long before anybody asks about
	// notifications.
	go func() {
		reaches, why := notify.Reach()
		eng.CheckReachability(ctx, reaches, why)
	}()
	// After declared roles are applied, so a board configured with a coordinator
	// is not offered a claim it does not need. Asked THROUGH the engine: st is
	// the state the loop now owns, and the goroutine above may be writing to it.
	//
	// DECLARED counts, not merely granted. On a FRESH board the pinned agent has
	// not registered yet, so the pass above grants nothing and HasCoordinator is
	// false: the daemon then minted a generic claim that any same-user agent
	// could read and spend, taking coordinator before the intended identity ever
	// started. Later reconciliation grants the real one and does not demote the
	// opportunist, so the board ends with a coordinator the operator did not
	// choose holding broadcast, eviction and force-release. The claim exists for
	// boards that named nobody; an operator who named somebody has already
	// answered the question it asks.
	installCoordinatorClaim(eng, *dir,
		coordinatorAlreadyDecided(eng.HasCoordinator(ctx), cfg.Roles))
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

	tr, err := resolveTransport(*dir, listenAddr, askedScheme, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: listenAddr, Handler: newAuthGate(secret, filepath.Join(*dir, "admin.hash"), listenAddr).wrap(mux),
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
	// ListenAndServe binds and serves in one call, so "dibd up" was logged
	// before anything had been bound, and a port collision then printed a
	// confident success line immediately followed by the failure. That is not
	// cosmetic: it fooled this project's own setup checks twice, which grepped
	// for "dibd up", found it, and proceeded to talk to a DIFFERENT daemon
	// that already held the port. An operator reading a log has the same
	// problem. Nothing claims to be up until the socket is ours.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return bindFailure(listenAddr, err)
	}
	// AND THE CERTIFICATE, before announcing anything.
	//
	// Binding was made to precede the log for exactly this reason and only
	// solved half of it: ServeTLS loads and validates the pair AFTER Serve is
	// called, so a missing, malformed or mismatched key printed the same
	// confident "dibd up" and then died. `dibd -check` could not catch it
	// either, because validateTLS only checks that both path strings are
	// present. Loading here is the same work ServeTLS would do, done while
	// there is still nothing claiming to be up.
	var tlsPair []tls.Certificate
	if tr.certFile != "" {
		cert, cerr := tls.LoadX509KeyPair(tr.certFile, tr.keyFile)
		if cerr != nil {
			_ = ln.Close()
			return fmt.Errorf("the TLS certificate and key in %s cannot be used "+
				"together (%w), so this daemon would report itself up and then "+
				"immediately fail every connection. Check that tls_cert and tls_key "+
				"exist, are readable, and are a matching pair; removing both makes "+
				"dibd generate its own", *dir, cerr)
		}
		tlsPair = []tls.Certificate{cert}
	}
	// Roles are the one part of the config that grants standing privilege, and
	// a typo in an agent name means the grant silently applies to nobody. Saying
	// what it did lets an operator see that from the log rather than by reading
	// the ledger, which is what describeDeclaredRoles was written for and was
	// never called to do.
	up := []any{
		"addr", listenAddr, "node", nodeID, "dir", *dir, "transport", tr.why,
		"mcp", scheme + "://" + listenAddr + "/mcp", "board", scheme + "://" + listenAddr + "/",
	}
	if roles := describeDeclaredRoles(cfg.Roles); roles != "" {
		up = append(up, "roles", roles)
	}
	up = append(up, "hint", "run `dibs web` for a board link, `dibs mcp-config` for the agent config")
	slog.Info("dibd up", up...)
	serve := func() error { return srv.Serve(ln) }
	if len(tlsPair) > 0 {
		// The pair already loaded above, so nothing is read from disk here and
		// there is no second chance for it to fail after the log line.
		srv.TLSConfig = &tls.Config{Certificates: tlsPair, MinVersion: tls.VersionTLS12}
		serve = func() error { return srv.ServeTLS(ln, "", "") }
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
			"note", "`dibs probe --pid N` still answers on demand")
		return
	}
	go eng.Supervise(ctx, superviseSettings(c))
}

// bindFailure turns a refused port into something the reader can act on.
//
// It used to surface Go's own text and nothing else: "listen tcp
// 127.0.0.1:4777: bind: address already in use". True, and useless. Every other
// error in this project carries the corrective call, and the one an operator is
// most likely to meet on a first run did not.
//
// It does NOT fall back to a free port, and that restraint is the point. Clients
// resolve the daemon from DIBS_ADDR or the default, so a daemon that quietly
// moved is a daemon nobody can find: every agent still starts, every call still
// succeeds against nothing, and the fleet is silently partitioned. That is the
// exact failure Dibs exists to prevent, and it would be self-inflicted.
// Discovering the address from the run registry is what would make a fallback
// safe, and until that exists this says what is wrong and stops.
func bindFailure(listenAddr string, err error) error {
	if !errors.Is(err, syscall.EADDRINUSE) {
		return err
	}
	// The registry knows every dibd on this machine, so name the one holding
	// the port rather than making the reader run lsof.
	if live, lerr := paths.LiveDaemons(); lerr == nil {
		for _, d := range live {
			// Not us. This daemon registers its intended address before it binds,
			// so the first entry matching the port is our own, and reporting it
			// told the reader that dibd was already serving a port nothing was
			// serving. Found by holding the port with a plain socket and reading
			// what came out.
			if d.Addr != listenAddr || d.PID == os.Getpid() {
				continue
			}
			return fmt.Errorf("%s is already served by dibd (pid %d) on %s.\n"+
				"  If that is the daemon you want, it is already up.\n"+
				"  To replace it:   dibs stop\n"+
				"  To run alongside it, on its own port and data directory:\n"+
				"    dibd -allow-parallel -addr 127.0.0.1:4778 -dir %s",
				listenAddr, d.PID, d.Dir, "/path/to/other-data")
		}
	}
	return fmt.Errorf("something other than dibd is listening on %s.\n"+
		"  Choose another port:   dibd -addr 127.0.0.1:4778\n"+
		"  Or find the holder:    lsof -nP -iTCP:%s -sTCP:LISTEN",
		listenAddr, portOf(listenAddr))
}

// portOf is the port half of host:port, for a hint that would read oddly with
// the host still attached.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return addr
}

// oldVocabularyFailure explains a ledger written by a release before this one,
// or returns nil if this build can fold what is there.
//
// It reads the LEDGER, not a replay error, and it runs BEFORE the fold. Both are
// load-bearing, for different reasons, and both were wrong when this shipped.
//
// Reading the ledger: the obvious version matched the retired word in the error
// text and would never have fired. `register_lane` no longer matches the
// actor-free cases in Apply, so it falls through to actor resolution and dies as
// E_BAD_TOKEN, an error that never names the kind at all.
//
// Running before the fold: a retired op KIND fails replay, loudly. A retired
// FIELD name fails nothing. The record still applies, with that field silently
// zero, and Replay reports success over a board that is quietly wrong. v0.0.4
// and earlier wrote `lane_kind`; this build reads `agent_kind`, so every
// persistent agent in such a ledger folds back as EPHEMERAL, losing nonce resume
// and coordinator eligibility, and a `sweep` that recorded `dead_lanes` folds
// back having marked nobody dead. That is `state == fold(ledger)` broken with no
// error anywhere to hang a check on, which is why this cannot be gated on one.
// It was found by a persistent agent coming back ephemeral on a real board.
//
// It reads the whole ledger rather than a cheap prefix, because a partial scan
// answers "clean" for a board it has not looked at, and a false clean here is
// the exact failure being prevented. Boot already reads every line to fold it.
//
// DELETE AT 0.1.0, along with the apology. By then nobody carries a 0.0.x ledger
// forward and this is a list of words kept alive by no one remembering what it
// was for.
func oldVocabularyFailure(dir string) error {
	ledgerPath := filepath.Join(dir, "ledger.jsonl")
	word := retiredVocabulary(ledgerPath)
	if word == "" {
		return nil
	}
	return fmt.Errorf("this board was written by an older Dibs, back when an agent was a lane.\n"+
		"  Its records say %q, which this build no longer reads. Some of those\n"+
		"  records would still fold, into a board that disagrees with them: that is\n"+
		"  why this stops here rather than opening something subtly wrong.\n\n"+
		"  If you are an agent reading this, you can fix it:\n"+
		"    mv %s %[2]s.old\n"+
		"  then start dibd again, and TELL YOUR HUMAN the old board was set aside\n"+
		"  and why. Their work is untouched; the coordination history is not.\n\n"+
		"  Sorry: one clean break at 0.0.x beat two later", word, ledgerPath)
}

// retiredOpKinds are op kinds no build after v0.0.4 writes. Replay refuses
// these on its own; they are listed so the refusal can say what happened.
//
// Every word here must be one this build NO LONGER writes. The list shipped
// naming the words it had just been renamed TO: `register`, `check_in`,
// `declare`, because the sweep that renamed the vocabulary rewrote this table
// along with everything else. A guard whose trigger fires on the current
// vocabulary tells a person their perfectly good board is obsolete and to move
// it aside. TestTheRetiredWordsAreRetired is what keeps that from recurring.
var retiredOpKinds = map[string]bool{
	"register_lane": true, "resume_lane": true, "wake_lane": true,
	"ack_board": true, "update_lane": true, "close_lane": true,
	"set_slot": true, "clear_slot": true, "send_message": true,
	"ack_message": true, "prune_lane": true,
}

// retiredOpFields are op FIELD names no build after v0.0.4 writes.
//
// These are the dangerous half. An unknown kind stops the fold; an unknown
// field is simply not read, so the op applies with that field zero and nothing
// reports anything. `lane_kind` is why this exists: every release up to v0.0.4
// wrote it, this build reads `agent_kind`, and the difference is every
// persistent agent on an upgraded board folding back as ephemeral.
var retiredOpFields = map[string]bool{
	"lane":        true, // → agent_id
	"lane_kind":   true, // → agent_kind
	"dead_lanes":  true, // → dead_agents
	"stale_lanes": true, // → stale_agents
	"channel":     true, // → space
}

// retiredVocabulary reports the first retired word in a ledger, or "".
//
// DELETE AT 0.1.0. By then nobody carries a 0.0.x ledger forward and this is a
// list of words kept alive by no one remembering what it was for. The apology
// has a shelf life too.
func retiredVocabulary(ledgerPath string) string {
	f, err := os.Open(ledgerPath) // #nosec G304 -- the daemon's own data directory
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		// Into a map, so a field this build has no struct member for is still
		// visible. Decoding into core.Op is what hides these by construction.
		var rec struct {
			Op map[string]json.RawMessage `json:"op"`
		}
		if json.Unmarshal(scan.Bytes(), &rec) != nil {
			continue
		}
		var kind string
		if raw, ok := rec.Op["kind"]; ok {
			_ = json.Unmarshal(raw, &kind)
		}
		if retiredOpKinds[kind] {
			return kind
		}
		for field := range rec.Op {
			if retiredOpFields[field] {
				return field
			}
		}
	}
	return ""
}

// checkReplay answers "could this build take over this board", and answers it
// while the board is still being served by somebody else.
//
// It exists because the two ways a Dibs upgrade goes wrong are both invisible
// until the new daemon starts, and by then the old one has been stopped. A rule
// added to core.Apply instead of core.Admit is retroactive, so the fold refuses
// records an older build wrote and accepted; a retired op VOCABULARY does the
// same at a different layer. Either one turns an upgrade into a board that will
// not open, on a machine whose whole fleet was coordinating through it minutes
// earlier. Both have happened here.
//
// Running the check from the NEW binary is the point. `dibs` links its own copy
// of core and could fold the ledger itself, but that measures the CLI's build,
// and the binary the service is about to start is exactly the thing in doubt.
//
// Read-only in the sense that matters: no host slot (so it never collides with
// the running daemon), no listener, no ops, and nothing appended. It opens the
// ledger and the key because folding requires decrypting, and those files
// already exist for any board worth checking.
func checkReplay(dir string, cfg Config) error {
	if _, err := os.Stat(filepath.Join(dir, "ledger.jsonl")); err != nil {
		return fmt.Errorf("no board at %s: there is nothing to check", dir)
	}
	// LOAD, never create. This is a question about a board, and asking it must
	// not bring one into being: `dibs upgrade` runs this while the previous
	// daemon may still be serving, so anything written here is written into a
	// live data directory by a process that does not own it. A board missing
	// either file is not a board this build could take over, which is the
	// question, so saying so is the right answer rather than a failure.
	nodeID, err := os.ReadFile(filepath.Join(dir, "node_id")) // #nosec G304 -- the operator's own -dir
	if err != nil || len(nodeID) == 0 {
		return fmt.Errorf("no node_id at %s: this is not a board a daemon has served, "+
			"so there is nothing to check", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "key")); err != nil {
		return fmt.Errorf("no key at %s: the ledger is encrypted and cannot be "+
			"replayed without it", dir)
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		return err
	}
	// Same order as run(): a retired vocabulary folds "successfully" into a
	// board that disagrees with its own records, so it has to be caught first
	// or the check passes on exactly the upgrade it exists to stop.
	if old := oldVocabularyFailure(dir); old != nil {
		return old
	}
	led, err := ledger.OpenReadOnly(filepath.Join(dir, "ledger.jsonl"), string(nodeID), box)
	if err != nil {
		return err
	}
	defer func() { _ = led.Close() }()
	limits, err := applyLimits(cfg.Limits, core.DefaultLimits())
	if err != nil {
		return fmt.Errorf("reading %s/dibs.toml: %w", dir, err)
	}
	st := core.NewState(string(nodeID), limits)
	start := time.Now()
	n, err := led.Replay(st)
	if err != nil {
		return fmt.Errorf("this build cannot rebuild the board at %s: %w\n\n"+
			"  Record %d will not apply. Do NOT stop the daemon now running: it is\n"+
			"  serving a board this binary could not reconstruct, and stopping it is\n"+
			"  what makes that unrecoverable.\n\n"+
			"  Stay on the running build, and report this with `dibs verify %s`",
			dir, err, st.Serial+1, dir)
	}
	fmt.Printf("ok: %s replays %d record(s) to serial %d in %s (%d agent(s), %d space(s))\n",
		build.Version, n, st.Serial, time.Since(start).Round(time.Microsecond),
		len(st.Agents), len(st.Spaces))
	return nil
}

// wantsMan spots `-man` before the flag package parses, because the manual must
// not require a valid data directory or a config file to print.
func wantsMan() bool {
	for _, a := range os.Args[1:] {
		if a == "-man" || a == "--man" {
			return true
		}
	}
	return false
}

// manArgs is everything after -man, so `dibd -man -out dibd.8` works.
func manArgs() []string {
	for i, a := range os.Args[1:] {
		if a == "-man" || a == "--man" {
			return os.Args[i+2:]
		}
	}
	return nil
}
