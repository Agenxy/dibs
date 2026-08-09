package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// callHookTool invokes one Lanes tool over the local MCP endpoint on behalf of
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
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/mcp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Lanes-Local", secret)

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
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
