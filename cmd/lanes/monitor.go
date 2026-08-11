package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// monitor is the engine behind a Claude Code plugin monitor (and any harness
// that turns a subprocess's stdout lines into notifications). It owns a
// persistent per-project lane, writes that lane's token where the agent can
// adopt it, then long-polls and prints ONE line per incoming message, which
// the harness delivers into the running session, so the agent notices mail
// without ever spawning a waiter or remembering to poll.
//
//	lanes monitor --lane <name> --state-dir <dir>
//
// With --lane it registers/resumes a persistent lane and writes its token to
// <state-dir>/<name>.token (0600). Without --lane it uses LANES_TOKEN and only
// watches. Each printed line is a notification; the body is NOT printed
// (private): the agent reads it with the inbox/get_message tool.
func monitor(args []string) error {
	fs := flag.NewFlagSet("monitor", flag.ContinueOnError)
	lane := fs.String("lane", "", "own+watch a persistent lane by this name (default: project dir name)")
	stateDir := fs.String("state-dir", "", "dir for the nonce+token (default: <project>/.lanes)")
	desc := fs.String("desc", "coordination lane for this session", "lane description when registering")
	watchOnly := fs.Bool("watch-only", false, "don't own a lane; watch using LANES_TOKEN")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Derive project-scoped defaults so the Claude Code monitor command can be a
	// bare `lanes monitor` with no shell substitution. CLAUDE_PROJECT_DIR is set
	// by the harness; fall back to the working directory otherwise.
	project := os.Getenv("CLAUDE_PROJECT_DIR")
	if project == "" {
		if wd, e := os.Getwd(); e == nil {
			project = wd
		} else {
			project = "."
		}
	}
	if *lane == "" && !*watchOnly {
		*lane = filepath.Base(project)
	}
	if *stateDir == "" {
		*stateDir = filepath.Join(project, ".lanes")
	}
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start lanesd once first: %w", err)
	}
	call := mcpCaller(secret)

	token := os.Getenv("LANES_TOKEN")
	if *lane != "" {
		dir := *stateDir
		if dir == "" {
			dir = "."
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		token, _, err = ensureLane(call, *lane, *desc, dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "lanes monitor: lane %q ready; token at %s\n", *lane, filepath.Join(dir, "token"))
	}
	if token == "" {
		return fmt.Errorf("no lane: pass --lane <name> or set LANES_TOKEN")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	cursor := uint64(0)
	if res, err := call("events_since", map[string]any{"token": token, "since_serial": ^uint64(0) >> 1}); err == nil {
		if s, ok := res["serial"].(float64); ok {
			cursor = uint64(s)
		}
	}
	fmt.Fprintln(os.Stderr, "lanes monitor: watching for messages (^C to stop)")
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		res, err := call("await_events", map[string]any{"token": token, "since_serial": cursor, "timeout_s": 60})
		if err != nil {
			if isCursorTooOld(err) {
				if b, e := call("ack_board", map[string]any{"token": token}); e == nil {
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
			data, _ := ev["data"].(map[string]any)
			from, _ := data["from"].(string)
			mt, _ := data["msg_type"].(string)
			serial := ""
			if s, ok := ev["serial"].(float64); ok {
				serial = strconv.FormatUint(uint64(s), 10)
			}
			// One notification line per message. Body stays private: the
			// agent reads it with the inbox/get_message tool.
			fmt.Printf("📬 Lanes: new %s from %q (serial %s): read it with the inbox or get_message tool\n", mt, from, serial)
		}
		if s, ok := res["serial"].(float64); ok && uint64(s) > cursor {
			cursor = uint64(s)
		}
	}
}

// ensureLane registers (or resumes) a persistent lane by name, keyed by a
// nonce persisted in dir, and writes the resulting token to <name>.token.
// Returns the lane token and its resolved lane id.
func ensureLane(call mcpFn, name, desc, dir string) (string, string, error) {
	// Fixed filenames: one lane per state-dir (per project), so the agent's
	// skill can reference ./.lanes/token without knowing the lane name.
	noncePath := filepath.Join(dir, "nonce")
	tokenPath := filepath.Join(dir, "token")
	nonce := ""
	// #nosec G304 -- a path inside the daemon's own data directory, or one the
	// operator pointed the CLI at. Same-user access only; refusing it would mean
	// refusing to run.
	if b, err := os.ReadFile(noncePath); err == nil {
		nonce = string(bytes.TrimSpace(b))
	}
	if nonce == "" {
		raw := make([]byte, 16)
		_, _ = rand.Read(raw)
		nonce = hex.EncodeToString(raw)
		if err := os.WriteFile(noncePath, []byte(nonce), 0o600); err != nil {
			return "", "", err
		}
	}
	// Try register; if the nonce is already bound outside the retry window,
	// resume instead.
	res, err := call("register_lane", map[string]any{
		"name": name, "kind": "persistent", "nonce": nonce, "description": desc,
	})
	if err != nil {
		if isNonceInUse(err) {
			rid := make([]byte, 8)
			_, _ = rand.Read(rid)
			res, err = call("resume_lane", map[string]any{"nonce": nonce, "resume_id": hex.EncodeToString(rid)})
		}
		if err != nil {
			return "", "", err
		}
	}
	token, _ := res["token"].(string)
	if token == "" {
		return "", "", fmt.Errorf("register/resume returned no token: %v", res)
	}
	laneID, _ := res["lane_id"].(string)
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		return "", "", err
	}
	return token, laneID, nil
}

// --- shared MCP call helper (used by monitor) ---

type mcpFn func(tool string, args map[string]any) (map[string]any, error)

func mcpCaller(secret string) mcpFn {
	return func(tool string, callArgs map[string]any) (map[string]any, error) {
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
		resp, err := (&http.Client{Timeout: 75 * time.Second}).Do(req)
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
			return payload, fmt.Errorf("%v: %v", payload["code"], payload["message"])
		}
		return payload, nil
	}
}

func isCursorTooOld(err error) bool { return err != nil && contains(err.Error(), "E_CURSOR_TOO_OLD") }
func isNonceInUse(err error) bool   { return err != nil && contains(err.Error(), "E_NONCE_IN_USE") }
func contains(s, sub string) bool   { return bytes.Contains([]byte(s), []byte(sub)) }
