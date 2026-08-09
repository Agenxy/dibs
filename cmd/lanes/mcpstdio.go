package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// mcpStdio is a stdio↔HTTP bridge for the Lanes MCP server. A harness that
// only speaks stdio MCP (or a plugin that must not hardcode the local secret)
// launches `lanes mcp-stdio`; it reads the secret from disk locally and
// forwards each newline-delimited JSON-RPC request to the loopback daemon's
// /mcp, streaming responses back. Lanes' MCP is stateless request/response
// with no server-initiated messages, so a line proxy is sufficient.
func mcpStdio(_ []string) error {
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet — start lanesd once first: %w", err)
	}
	url := "http://" + addr() + "/mcp"
	client := &http.Client{Timeout: 75 * time.Second}
	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()

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
		req.Header.Set("X-Lanes-Local", secret)
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("%w (is lanesd running?)", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		body = bytes.TrimSpace(body)
		if len(body) == 0 {
			continue // notification / 202: no response line
		}
		_, _ = out.Write(body)
		_ = out.WriteByte('\n')
		_ = out.Flush()
	}
	return sc.Err()
}
