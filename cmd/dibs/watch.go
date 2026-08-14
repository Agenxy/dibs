package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// watch is the persistent sibling of `await`: it stays running and executes a
// shell command every time a matching message arrives for the caller's agent,
// the "summoner". A cheap non-agent process holds the socket; when mail lands
// it runs --exec (e.g. to launch a fresh agent session, ping a webhook, or
// wake a harness). No agent ever polls; the daemon-adjacent watcher does.
func watch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	execCmd := fs.String("exec", "", "shell command to run per matching message (required)")
	tokenFlag := fs.String("token", "", "agent token (prefer DIBS_TOKEN env)")
	typesCSV := fs.String("types", "", "only fire on these message types (csv: notify,question,request,handoff); "+
		"empty = all")
	since := fs.Uint64("since", 0, "start cursor (0 = from now)")
	once := fs.Bool("once", false, "exit after the first firing")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*execCmd) == "" {
		return fmt.Errorf("--exec is required (the command to run when a message arrives)")
	}
	token := *tokenFlag
	if token == "" {
		token = os.Getenv("DIBS_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("no agent token: set DIBS_TOKEN (from register) or pass --token")
	}
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start dibd once first: %w", err)
	}

	typeFilter := map[string]bool{}
	for _, t := range strings.Split(*typesCSV, ",") {
		if t = strings.TrimSpace(t); t != "" {
			typeFilter[t] = true
		}
	}

	call := func(tool string, callArgs map[string]any) (map[string]any, error) {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": callArgs},
		})
		req, err := http.NewRequest(http.MethodPost, origin()+"/mcp", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dibs-Local", secret)
		resp, err := daemonClient(75 * time.Second).Do(req)
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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	cursor := *since
	if cursor == 0 {
		if res, err := call("events_since", map[string]any{"token": token, "since_serial": ^uint64(0) >> 1}); err == nil {
			if s, ok := res["serial"].(float64); ok {
				cursor = uint64(s)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "dibs watch: running `%s` on each new message (^C to stop)\n", *execCmd)

	for {
		select {
		case <-stop:
			fmt.Fprintln(os.Stderr, "dibs watch: stopped")
			return nil
		default:
		}
		res, err := call("await_events", map[string]any{"token": token, "since_serial": cursor, "timeout_s": 60})
		if err != nil {
			if strings.Contains(err.Error(), "E_CURSOR_TOO_OLD") {
				if b, e := call("check_in", map[string]any{"token": token}); e == nil {
					if s, ok := b["serial"].(float64); ok {
						cursor = uint64(s)
					}
				}
				continue
			}
			return err
		}
		evs, _ := res["events"].([]any)
		for _, raw := range evs {
			ev, _ := raw.(map[string]any)
			if ev["type"] != "message.sent" {
				continue
			}
			// The server already filters await_events to events addressed to
			// this agent, so every message.sent here is for us.
			data, _ := ev["data"].(map[string]any)
			mt, _ := data["msg_type"].(string)
			if len(typeFilter) > 0 && !typeFilter[mt] {
				continue
			}
			runExec(*execCmd, ev, data)
			if *once {
				return nil
			}
		}
		if s, ok := res["serial"].(float64); ok && uint64(s) > cursor {
			cursor = uint64(s)
		}
	}
}

// runExec runs the command with the event on stdin and DIBS_* env vars set.
func runExec(command string, ev, data map[string]any) {
	evJSON, _ := json.Marshal(ev)
	serial := ""
	if s, ok := ev["serial"].(float64); ok {
		serial = strconv.FormatUint(uint64(s), 10)
	}
	from, _ := data["from"].(string)
	mt, _ := data["msg_type"].(string)
	fmt.Fprintf(os.Stderr, "dibs watch: fired on serial %s (%s from %s)\n", serial, mt, from)

	// #nosec G204 -- running the operator's own command IS the feature: `dibs watch
	// --exec CMD` is documented as "run CMD on each message". The string comes from
	// this process's argv, never from an agent or the network.
	c := exec.Command("sh", "-c", command)
	c.Env = append(
		os.Environ(),
		"DIBS_EVENT="+string(evJSON),
		"DIBS_EVENT_TYPE=message.sent",
		"DIBS_EVENT_SERIAL="+serial,
		"DIBS_MSG_TYPE="+mt,
		"DIBS_MSG_FROM="+from,
	)
	c.Stdin = bytes.NewReader(evJSON)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "dibs watch: exec error: %v\n", err)
	}
}
