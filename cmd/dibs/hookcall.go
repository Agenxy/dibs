package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/mcp"
)

// callHookTool invokes one Dibs tool over the local MCP endpoint on behalf of
// a lifecycle hook.
//
// Short timeout and errors swallowed by every caller: this runs in front of an
// agent's shell command, so a slow or absent daemon must cost that command
// nothing. A hook that can stall the thing it decorates is a hook that gets
// removed.
func callHookTool(tool string, args map[string]any, out any) error {
	secret, err := localSecret()
	if err != nil {
		return err
	}
	params := map[string]any{"name": tool, "arguments": args}
	// WHICH MACHINE, as the stdio bridge says on every call. The host-scoped
	// lookups take it from here, and a call without it that arrives on
	// loopback is stamped as the daemon's own machine: through the
	// documented `ssh -L` forward, a remote agent's guard then resolved to
	// nobody and its edit went ahead past an exclusive claim. Round
	// seventeen of the pre-release review.
	if hid := hostID(); hid != "" {
		params["_meta"] = map[string]any{mcp.HostMetaKey: hid}
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params,
	})
	req, err := http.NewRequest(http.MethodPost, mcpEndpoint(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Dibs-Local", secret)

	resp, err := daemonClient(2 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if env.Result.IsError || len(env.Result.Content) == 0 {
		return fmt.Errorf("%s returned nothing usable", tool)
	}
	return json.Unmarshal([]byte(env.Result.Content[0].Text), out)
}

// mcpEndpoint is where a lifecycle hook posts, and every other command that
// posts to the daemon on an agent's behalf (await, watch, monitor): the hub
// as Supgang says it is NOW when the config named it as a peer
// (boardOrigin), not the address the config was printed with. The stdio
// bridge already dialled through boardOrigin and followed a hub that moved;
// the `dibs hook-poll` and `dibs hook` subcommands dialled origin() and kept
// timing out against the saved address, so a Gemini session lost its
// start-of-session delivery while its bridge reconnected fine. Resolved once
// per process, as the bridge resolves once per start: a command that polls
// must not spawn Supgang per poll. Round eighteen of the pre-release review.
func mcpEndpoint() string {
	mcpEndpointOnce.Do(func() { mcpEndpointValue = boardOrigin() + "/mcp" })
	return mcpEndpointValue
}

var (
	mcpEndpointOnce  sync.Once
	mcpEndpointValue string
)
