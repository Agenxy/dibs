package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// mcpStdio is a stdio↔HTTP bridge for the Dibs MCP server. A harness that
// only speaks stdio MCP (or a plugin that must not hardcode the local secret)
// launches `dibs mcp-stdio`; it reads the secret from disk locally and
// forwards each newline-delimited JSON-RPC request to the loopback daemon's
// /mcp, writing responses back.
//
// Most of MCP here is request/response, and for those a line proxy is exactly
// right. `subscriptions/listen` (SEP-2575) is not: it is one request whose
// response is a long-lived SSE stream of notifications. This comment used to
// say a line proxy sufficed "with no server-initiated messages", which was true
// when it was written and stopped being true when listen landed. Nothing
// failed loudly. The request went out, ReadAll sat on an endless body, the
// 75-second client timeout eventually killed it, and every harness on the
// plugin path silently had no push at all: it polled, while a client
// configured over direct HTTP got notifications. A capability that exists on
// one transport and quietly not the other is the worst of both.
//
// So listen is streamed instead, on its own goroutine, and stdout is
// serialised because notifications now interleave with ordinary replies.
func mcpStdio(args []string) error {
	if err := bridgePreflight(); err != nil {
		return err
	}
	return runBridge(args)
}

// bridgePreflight answers "is there a board to talk to" before anything is
// sent, because everything the bridge sends carries this board's local secret.
//
// Separated so mcpStdio stays inside the complexity budget, and because these
// are the two questions with the same answer: if either fails, no request
// should leave this process.
func bridgePreflight() error {
	// A dibs.toml the daemon will not start on is a reason to stop here rather
	// than guess an endpoint and send the credential to whatever answers.
	return checkConfigReadable()
}

func runBridge(_ []string) error {
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start dibd once first: %w", err)
	}
	url := origin() + "/mcp"
	client := daemonClient(75 * time.Second)
	// No timeout: this one is meant to stay open. A deadline here is a stream
	// that dies on the hour with nothing to say about why.
	//
	// Built through daemonClient so it carries the same trusted certificates as
	// every other request. A bare client here verified against the system roots
	// alone, so against a remote daemon with a self-signed certificate the
	// ordinary calls succeeded and the push stream silently did not: the agent
	// would poll forever and nothing would say why.
	streamClient := daemonClient(0)
	out := &syncWriter{w: bufio.NewWriter(os.Stdout)}
	defer out.flush()
	var streams sync.WaitGroup
	// Streams outlive individual requests, and must not outlive the SESSION.
	//
	// followStream re-issues the caller's listen every time the stream ends,
	// which is what keeps a subscription alive across a daemon restart and,
	// without a way to stop it, also keeps it alive across the harness going
	// away: `defer streams.Wait()` then waits on a goroutine that reconnects
	// forever, so a bridge with an open subscription never exits on EOF. Found
	// by closing stdin on a bridge that had one, which hung.
	// One shutdown path, with three ways in, so no single one has to be
	// airtight: stdin reaching EOF, the harness process exiting, or a signal.
	ctx, endSession := context.WithCancel(context.Background())
	defer endSession()
	// Bounded, deliberately. `streams.Wait()` alone made the guarantee "this
	// process exits IF its goroutines cooperate", and a bridge that fails to
	// exit is one orphan per session holding a stream open against the daemon
	// forever. The guarantee has to be that it exits.
	defer waitBounded(&streams, 3*time.Second)

	// A harness that dies while a sibling still holds the pipe's write end
	// leaves stdin open forever, so EOF cannot be the only signal. The kernel
	// is the one party that always knows.
	//
	// These paths EXIT rather than unwind, and that is the correction that
	// makes the guarantee real. Cancelling a context does not interrupt a
	// blocking read on stdin: the loop below is parked in that read and will
	// never look at ctx again, so a polite shutdown is one the process never
	// performs. Measured, with a sibling holding the write end: the bridge
	// outlived a SIGKILLed harness indefinitely with its context already
	// cancelled.
	//
	// Exiting is also simply correct here. Both paths mean the session is over:
	// the harness is gone, or something is stopping this process on purpose.
	// Nothing in flight is worth finishing for a client that is not there, so
	// the only thing owed is the flush.
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go endSessionWhenTheHarnessGoes(ctx, sigCtx, endSession, out)

	// A Reader rather than a Scanner, for one reason: Buffered().
	//
	// The bridge replaces itself in place when its binary changes, and anything
	// this process has read out of the pipe but not yet handled would be
	// discarded by that exec. Bytes still in the kernel's pipe buffer survive,
	// because the fd does; bytes in a userspace buffer do not. Scanner cannot
	// answer how much it is holding, so it cannot be made safe here.
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	self, haveSelf := currentSelf()
	// Anything a previous image was holding, re-established before the first
	// line is read, so the handshake identity and any subscription are in place
	// by the time they matter.
	restoreCarried(ctx, streamClient, url, secret, out, &streams)

	for {
		if ctx.Err() != nil {
			return nil // the harness went away
		}
		// Between a reply and the next request is the only quiescent point
		// there is, so it is where an upgrade can happen without losing one.
		if haveSelf {
			self = upgradeIfReplaced(in, self)
		}
		line, err := readLine(in)
		if err != nil {
			return err
		}
		if line == nil {
			return nil // EOF: the harness closed stdin
		}
		if len(line) == 0 {
			continue
		}
		// Registration is the one message worth touching: the harness environment we were
		// spawned into knows things the agent does not.
		line = enrichRegister(line)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(line))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dibs-Local", secret)
		setPinnedIdentity(req)

		// A listen call answers with a stream, not a reply, so it cannot be
		// awaited on the loop that reads stdin: the harness would be unable to
		// send anything else for as long as it is subscribed.
		if methodOf(line) == "subscriptions/listen" {
			req.Header.Set("Accept", "text/event-stream")
			listen := bytes.Clone(line)
			noteListen(listen)
			streams.Add(1)
			go func() {
				defer streams.Done()
				followStream(ctx, streamClient, url, secret, listen, out)
			}()
			continue
		}

		resp, err := doWithRestartGrace(client, req, line)
		if err != nil {
			return reachErr(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		body = bytes.TrimSpace(body)
		if len(body) == 0 {
			continue // notification / 202: no response line
		}
		out.line(body)
	}
}

// readLine reads one newline-delimited message, or nil at EOF.
//
// Bounded at the same 16 MiB the Scanner it replaced allowed. A local harness
// is trusted, but "trusted" is not "may hand this process an unbounded
// allocation", and the daemon caps a request body anyway.
func readLine(in *bufio.Reader) ([]byte, error) {
	const maxLine = 1 << 24
	var acc []byte
	for {
		chunk, err := in.ReadSlice('\n')
		acc = append(acc, chunk...)
		if len(acc) > maxLine {
			return nil, fmt.Errorf("a single JSON-RPC line exceeded %d bytes", maxLine)
		}
		switch {
		case err == nil:
			return bytes.TrimSpace(acc), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue // a long line: keep accumulating
		case errors.Is(err, io.EOF):
			if line := bytes.TrimSpace(acc); len(line) > 0 {
				return line, nil // a final line with no newline
			}
			return nil, nil
		default:
			return nil, err
		}
	}
}

// upgradeBridge hands this session to the binary now on disk.
//
// The stderr line is the only trace this leaves. It is not the protocol
// (stdout is), so it cannot disturb the harness, and it lands in the harness's
// debug log, where "why is this session on a different build than it started
// with" is otherwise unanswerable.
func upgradeBridge(now selfIdentity) error {
	fmt.Fprintf(os.Stderr, "dibs: bridge upgrading in place to %s (pid %d keeps its pipes)\n",
		now.path, os.Getpid())
	env, err := carryEnv(bridgeState{
		ClientInfo: lastClientInfo,
		WantsUI:    lastWantsUI,
		Listens:    openListens(),
	})
	if err != nil {
		return err
	}
	return reexec(now.path, env)
}

// syncWriter serialises stdout across the request loop and any open streams.
//
// Without it the two interleave mid-line and the harness sees corrupt JSON,
// which is the kind of fault that looks like a protocol bug for a week.
type syncWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

// line writes one newline-delimited JSON-RPC message.
func (s *syncWriter) line(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(b)
	_ = s.w.WriteByte('\n')
	_ = s.w.Flush()
}

func (s *syncWriter) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.w.Flush()
}

// methodOf reports a JSON-RPC request's method without disturbing the bytes.
// The line is forwarded verbatim either way: this only decides how to read the
// response.
func methodOf(line []byte) string {
	var req struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(line, &req) != nil {
		return ""
	}
	return req.Method
}

// pumpSSE unwraps `data: ` frames into newline-delimited JSON-RPC messages
// until the stream ends.
//
// Keepalive comments (`: ...`) and blank separators carry no message and are
// dropped rather than forwarded: they are not JSON, and a harness that gets
// them on stdin has to decide what a non-JSON line means, which is a question
// no MCP client should be asked.
func pumpSSE(body io.Reader, out *syncWriter) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := bytes.TrimRight(sc.Bytes(), "\r")
		data, found := bytes.CutPrefix(line, []byte("data: "))
		if !found {
			continue
		}
		if data = bytes.TrimSpace(data); len(data) > 0 {
			out.line(data)
		}
	}
}

// upgradeGrace is how long a call waits for a daemon that is restarting.
//
// An upgrade is drain, swap, start: the old daemon finishes what it is holding
// and the new one replays the ledger before it binds. Replay is milliseconds on
// an ordinary board, so the window a client can see is short and, crucially,
// it is a window in which NOTHING WAS RECEIVED: the connection was refused.
//
// That distinction is what makes waiting safe rather than reckless. A refused
// dial means the request never reached the daemon, so re-sending it cannot
// duplicate an effect; anything that did reach it is not retried here at all.
// Without this an upgrade turns every in-flight agent call into a hard error,
// and an operator who has watched that happen once will stay on an old build
// rather than risk it, which is the real cost of a disruptive update.
const upgradeGrace = 10 * time.Second

// dialFailed reports a request that PROVABLY never reached the daemon.
//
// Connection refused, and nothing else. Refused means no listener accepted the
// connection, so the request was never read and re-sending it cannot duplicate
// an effect. That is the entire safety argument for retrying at all.
//
// ECONNRESET was in here and does not qualify. A reset says the peer tore the
// connection down; it says nothing about how much it had already done. The
// daemon can read a request, apply it, ledger it, and be killed before the
// response leaves, and the reset that reaches us is indistinguishable from one
// that arrived before the daemon looked at it. Retrying a send in that state
// delivers the message twice, and a retried claim or role change is worse.
//
// This mattered because upgrades are exactly when resets happen, which is also
// exactly when a duplicate is least likely to be noticed. Found by a
// pre-release review; an agent that wants a retry it can trust has op_id, which
// is what op_id is for.
func dialFailed(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// doWithRestartGrace sends one request, waiting out a daemon that is restarting.
func doWithRestartGrace(client *http.Client, req *http.Request, body []byte) (*http.Response, error) {
	deadline := time.Now().Add(upgradeGrace)
	for {
		resp, err := client.Do(req)
		if err == nil || !dialFailed(err) || time.Now().After(deadline) {
			return resp, err
		}
		time.Sleep(150 * time.Millisecond)
		// A request body is read once, so it has to be rebuilt for the retry.
		next, buildErr := http.NewRequest(http.MethodPost, req.URL.String(), bytes.NewReader(body))
		if buildErr != nil {
			return nil, err
		}
		next.Header = req.Header.Clone()
		req = next
	}
}

// followStream keeps a subscriptions/listen stream open across a daemon
// restart, re-issuing the caller's own listen request each time it ends.
//
// A stream is the one thing a restart cannot hand back on its own: the harness
// asked to be told about changes once, and if that ends silently it simply
// stops hearing, with nothing to notice. Re-issuing the ORIGINAL request is
// what keeps this honest: whatever the harness subscribed to is what it gets
// again, decided by the harness rather than reconstructed here.
func followStream(ctx context.Context, client *http.Client, url, secret string, listen []byte, out *syncWriter) {
	deadline := time.Now().Add(upgradeGrace)
	for {
		if ctx.Err() != nil {
			return // the session ended; stop reconnecting
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(listen))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dibs-Local", secret)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := client.Do(req)
		switch {
		case err == nil:
			if !isEventStream(resp) {
				// Not a stream: an ANSWER. The daemon refuses a listen with an
				// ordinary JSON-RPC error (no token for dibs://inbox, a token
				// it does not know, a writer it cannot flush), and a refusal is
				// not a condition that improves by asking again. Hand it to the
				// harness as the reply to its own call and stop. Retrying it
				// discarded the error, hammered the daemon, and left a harness
				// waiting forever for events that were never coming.
				relayRefusal(resp, listen, out)
				_ = resp.Body.Close()
				return
			}
			pumpSSE(resp.Body, out)
			_ = resp.Body.Close()
			// The stream ended. A daemon that is going away closes it, so try
			// again: the grace window restarts, because this stream did work.
			deadline = time.Now().Add(upgradeGrace)
		case dialFailed(err) && time.Now().Before(deadline):
			// Not up yet.
		default:
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// isEventStream reports whether a listen response is the stream that was asked
// for, rather than a reply refusing it.
//
// The status code is not the test: one of the daemon's three refusal paths
// answers 200 with a JSON-RPC error in the body, because that is what JSON-RPC
// over HTTP looks like. The content type is what distinguishes a stream.
func isEventStream(resp *http.Response) bool {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && mt == "text/event-stream"
}

// relayRefusal hands a refused subscription back as the reply to the harness's
// own call, so it can pair the two by id and stop waiting.
//
// stdout is a JSON-RPC channel, which is why pumpSSE drops keepalive comments
// rather than forwarding them: a harness that gets a non-JSON line has to
// decide what it means, and that is a question no MCP client should be asked.
// So a body that is not a JSON-RPC message (a proxy's HTML error page, an empty
// 502) becomes one here rather than being passed through.
func relayRefusal(resp *http.Response, listen []byte, out *syncWriter) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if body = bytes.TrimSpace(body); json.Valid(body) && bytes.HasPrefix(body, []byte("{")) {
		out.line(body)
		return
	}
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      idOf(listen),
		"error": map[string]any{
			"code": -32603,
			"message": fmt.Sprintf(
				"subscriptions/listen was refused with HTTP %d and no JSON-RPC reply", resp.StatusCode),
			"data": map[string]any{
				"hint": "the daemon did not accept this subscription; call await_events to wait for " +
					"the same changes, or check `dibs doctor` if this daemon is behind a proxy",
			},
		},
	})
	out.line(msg)
}

// idOf reports a JSON-RPC request's id so a synthesized reply can be paired
// with the call it answers. A request with no id gets JSON null, which is what
// the spec says to use when the id cannot be determined.
func idOf(line []byte) json.RawMessage {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(line, &req) != nil || len(req.ID) == 0 {
		return json.RawMessage("null")
	}
	return req.ID
}

// upgradeIfReplaced hands this session to a newly installed binary, and returns
// the identity to compare against next time.
//
// Returns only when it did NOT upgrade: a successful exec never comes back.
func upgradeIfReplaced(in *bufio.Reader, self selfIdentity) selfIdentity {
	// A part-read request lives in this process's memory and would be discarded
	// by the exec. Bytes still in the kernel's pipe buffer are safe.
	if in.Buffered() != 0 {
		return self
	}
	now, ok := currentSelf()
	if !ok || !now.differs(self) {
		return self
	}
	// Never fail the session over an upgrade. The build already running is the
	// one the agent has been talking to and it still works, so a failed exec
	// means carrying on and not trying this same binary again.
	_ = upgradeBridge(now)
	return now
}

// waitBounded waits for the stream goroutines, then gives up.
//
// Giving up is the point. Every goroutine here is cancelled through ctx and
// should stop at once, and "should" is exactly the word that produced an
// orphaned bridge in the first place: this process exiting must not depend on
// any of them behaving. Whatever is still running goes with the process.
func waitBounded(wg *sync.WaitGroup, limit time.Duration) {
	done := make(chan struct{})
	go func() { defer close(done); wg.Wait() }()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// endSessionWhenTheHarnessGoes ends this process when its harness does.
//
// It EXITS rather than unwinding, and that is what makes the guarantee real:
// cancelling a context does not interrupt a blocking read on stdin, so the read
// loop is parked in that read and will never look at ctx again. Measured, with
// a sibling holding the pipe's write end, the bridge outlived a SIGKILLed
// harness indefinitely with its context already cancelled.
//
// Exiting is also simply correct. Both triggers mean the session is over: the
// harness is gone, or something is stopping this process deliberately. Nothing
// in flight is worth finishing for a client that is not there, so the only
// thing owed is the flush.
func endSessionWhenTheHarnessGoes(ctx, sigCtx context.Context, endSession func(), out *syncWriter) {
	gone := parentGone(ctx)
	select {
	case <-ctx.Done(): // ordinary EOF: the loop is returning on its own
		return
	case <-gone:
	case <-sigCtx.Done():
	}
	endSession() // stop the streams before the process goes
	out.flush()
	os.Exit(0)
}

// setPinnedIdentity forwards an identity the operator pinned in the harness's
// own config, to where every transport can carry it.
//
// stdio configs set env and HTTP configs set headers; the daemon reads one
// thing either way, so reattaching stops depending on which transport a harness
// happens to speak. The bridge's own nonce store stays as the automatic path
// for a harness that pins nothing: ergonomics, no longer the mechanism.
func setPinnedIdentity(req *http.Request) {
	if n := pinnedNonce(); n != "" {
		req.Header.Set("X-Dibs-Agent-Nonce", n)
	}
}

// pinnedNonce is the identity the OPERATOR configured, if any.
//
// One reader, because two places now need it: the header this sets, and
// enrichNonce, which must not overrule it with something the bridge remembered.
func pinnedNonce() string { return strings.TrimSpace(os.Getenv("DIBS_AGENT_NONCE")) }
