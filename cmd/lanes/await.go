package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

// await blocks until events for the caller's lane arrive, prints them as
// JSON lines, and exits 0 — the universal adapter between Lanes and agent
// harnesses' background-task wake mechanism: an agent runs `lanes await` in
// the background, keeps working, and is woken by its harness the moment the
// command exits with mail. The shell polls; the model sleeps.
func await(args []string) error {
	fs := flag.NewFlagSet("await", flag.ContinueOnError)
	since := fs.Uint64("since", 0, "resume cursor (0 = from now)")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up after this long (exit 1)")
	tokenFlag := fs.String("token", "", "lane token (prefer LANES_TOKEN env)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	token := *tokenFlag
	if token == "" {
		token = os.Getenv("LANES_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("no lane token: set LANES_TOKEN (from register_lane) or pass --token")
	}
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet — start lanesd once first: %w", err)
	}

	call := func(tool string, callArgs map[string]any) (map[string]any, error) {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": callArgs},
		})
		req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/mcp", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Lanes-Local", secret)
		client := &http.Client{Timeout: 75 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		var rpc struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
			return nil, err
		}
		var payload map[string]any
		if len(rpc.Result.Content) > 0 {
			_ = json.Unmarshal([]byte(rpc.Result.Content[0].Text), &payload)
		}
		if rpc.Result.IsError {
			return nil, fmt.Errorf("%v: %v (hint: %v)", payload["code"], payload["message"], payload["hint"])
		}
		return payload, nil
	}

	cursor := *since
	if cursor == 0 {
		res, err := call("events_since", map[string]any{"token": token, "since_serial": ^uint64(0) >> 1})
		if err != nil {
			return err
		}
		if s, ok := res["serial"].(float64); ok {
			cursor = uint64(s)
		}
	}

	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		res, err := call("await_events", map[string]any{
			"token": token, "since_serial": cursor, "timeout_s": 60,
		})
		if err != nil {
			return err
		}
		evs, _ := res["events"].([]any)
		if len(evs) > 0 {
			for _, ev := range evs {
				line, _ := json.Marshal(ev)
				fmt.Println(string(line))
			}
			return nil // wake the harness
		}
		if s, ok := res["serial"].(float64); ok && uint64(s) > cursor {
			cursor = uint64(s)
		}
	}
	return fmt.Errorf("no events within %s", *timeout)
}
