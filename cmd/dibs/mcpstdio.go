package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
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
			// The body is closed by the goroutine below. bodyclose cannot
			// follow a body whose owner outlives this scope, which is exactly
			// what a long-lived stream is.
			resp, err := streamClient.Do(req) //nolint:bodyclose // closed in the goroutine below
			if err != nil {
				return fmt.Errorf("%w (is dibd running?)", err)
			}
			streams.Add(1)
			go func() {
				defer streams.Done()
				defer func() { _ = resp.Body.Close() }()
				pumpSSE(resp.Body, out)
			}()
			continue
		}

		resp, err := client.Do(req)
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
