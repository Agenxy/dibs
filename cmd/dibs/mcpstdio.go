package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
func mcpStdio(_ []string) error {
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start dibd once first: %w", err)
	}
	url := "http://" + addr() + "/mcp"
	client := &http.Client{Timeout: 75 * time.Second}
	// No timeout: this one is meant to stay open. A deadline here is a stream
	// that dies on the hour with nothing to say about why.
	streamClient := &http.Client{}
	out := &syncWriter{w: bufio.NewWriter(os.Stdout)}
	defer out.flush()
	var streams sync.WaitGroup
	defer streams.Wait()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
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

		// A listen call answers with a stream, not a reply, so it cannot be
		// awaited on the loop that reads stdin: the harness would be unable to
		// send anything else for as long as it is subscribed.
		if methodOf(line) == "subscriptions/listen" {
			req.Header.Set("Accept", "text/event-stream")
			listen := bytes.Clone(line)
			streams.Add(1)
			go func() {
				defer streams.Done()
				followStream(streamClient, url, secret, listen, out)
			}()
			continue
		}

		resp, err := doWithRestartGrace(client, req, line)
		if err != nil {
			return fmt.Errorf("%w (is dibd running?)", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		body = bytes.TrimSpace(body)
		if len(body) == 0 {
			continue // notification / 202: no response line
		}
		out.line(body)
	}
	return sc.Err()
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

// dialFailed reports a request that never reached the daemon: connection
// refused, or the listener gone between the drain and the rebind. Any other
// error may have been received and acted on, and is returned untouched.
func dialFailed(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
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
func followStream(client *http.Client, url, secret string, listen []byte, out *syncWriter) {
	deadline := time.Now().Add(upgradeGrace)
	for {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(listen))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dibs-Local", secret)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := client.Do(req)
		switch {
		case err == nil:
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
		time.Sleep(150 * time.Millisecond)
	}
}
