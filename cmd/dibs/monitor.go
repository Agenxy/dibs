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

	"github.com/agenxy/dibs/internal/paths"
)

// monitor is the engine behind a Claude Code plugin monitor (and any harness
// that turns a subprocess's stdout lines into notifications). It owns a
// persistent per-project agent, writes that agent's token where the agent can
// adopt it, then long-polls and prints ONE line per incoming message, which
// the harness delivers into the running session, so the agent notices mail
// without ever spawning a waiter or remembering to poll.
//
//	dibs monitor --agent <name> --state-dir <dir>
//
// With --agent it registers/resumes a persistent agent and writes its token to
// <state-dir>/<name>.token (0600). Without --agent it uses DIBS_TOKEN and only
// watches. Each printed line is a notification; the body is NOT printed
// (private): the agent reads it with the inbox/read_mail tool.
func monitor(args []string) error {
	fs := flag.NewFlagSet("monitor", flag.ContinueOnError)
	agent := fs.String("agent", "", "own+watch a persistent agent by this name (default: project dir name)")
	stateDir := fs.String("state-dir", "", "dir for the nonce+token (default: <project>/.dibs)")
	desc := fs.String("desc", "coordination agent for this session", "agent description when registering")
	watchOnly := fs.Bool("watch-only", false, "don't own an agent; watch using DIBS_TOKEN")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Derive project-scoped defaults so the Claude Code monitor command can be a
	// bare `dibs monitor` with no shell substitution. CLAUDE_PROJECT_DIR is set
	// by the harness; fall back to the working directory otherwise.
	project := os.Getenv("CLAUDE_PROJECT_DIR")
	if project == "" {
		if wd, e := os.Getwd(); e == nil {
			project = wd
		} else {
			project = "."
		}
	}
	if *agent == "" && !*watchOnly {
		*agent = filepath.Base(project)
	}
	if *stateDir == "" {
		*stateDir = paths.ProjectStateDir(project)
	}
	secret, err := localSecret()
	if err != nil {
		return fmt.Errorf("no local secret yet: start dibd once first: %w", err)
	}
	call := mcpCaller(secret)

	token := os.Getenv("DIBS_TOKEN")
	if *agent != "" {
		dir := *stateDir
		if dir == "" {
			dir = "."
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		token, _, err = ensureAgent(call, *agent, *desc, dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "dibs monitor: agent %q ready; token at %s\n", *agent, filepath.Join(dir, "token"))
	}
	if token == "" {
		return fmt.Errorf("no agent: pass --agent <name> or set DIBS_TOKEN")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	cursor := uint64(0)
	if res, err := call("events_since", map[string]any{"token": token, "since_serial": ^uint64(0) >> 1}); err == nil {
		if s, ok := res["serial"].(float64); ok {
			cursor = uint64(s)
		}
	}
	fmt.Fprintln(os.Stderr, "dibs monitor: watching for messages (^C to stop)")
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		res, err := call("await_events", map[string]any{"token": token, "since_serial": cursor, "timeout_s": 60})
		if err != nil {
			if isCursorTooOld(err) {
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
			data, _ := ev["data"].(map[string]any)
			from, _ := data["from"].(string)
			mt, _ := data["msg_type"].(string)
			serial := ""
			if s, ok := ev["serial"].(float64); ok {
				serial = strconv.FormatUint(uint64(s), 10)
			}
			// One notification line per message. Body stays private: the
			// agent reads it with the inbox/read_mail tool.
			fmt.Printf("📬 Dibs: new %s from %q (serial %s): read it with the inbox or read_mail tool\n", mt, from, serial)
		}
		if s, ok := res["serial"].(float64); ok && uint64(s) > cursor {
			cursor = uint64(s)
		}
	}
}

// ensureAgent registers (or resumes) a persistent agent by name, keyed by a
// nonce persisted in dir, and writes the resulting token to <name>.token.
// Returns the agent token and its resolved agent id.
func ensureAgent(call mcpFn, name, desc, dir string) (string, string, error) {
	// Fixed filenames: one agent per state-dir (per project), so the agent's
	// skill can reference ./.dibs/token without knowing the agent name.
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
	res, err := call("register", map[string]any{
		"name": name, "kind": "persistent", "nonce": nonce, "description": desc,
	})
	if err != nil {
		if isNonceInUse(err) {
			rid := make([]byte, 8)
			_, _ = rand.Read(rid)
			res, err = call("resume", map[string]any{"nonce": nonce, "resume_id": hex.EncodeToString(rid)})
		}
		if err != nil {
			return "", "", err
		}
	}
	token, _ := res["token"].(string)
	if token == "" {
		return "", "", fmt.Errorf("register/resume returned no token: %v", res)
	}
	agentID, _ := res["agent_id"].(string)
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		return "", "", err
	}
	return token, agentID, nil
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
		req.Header.Set("X-Dibs-Local", secret)
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
