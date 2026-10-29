// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (r *runner) smoke(bin, home string, env []string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return err
	}
	dir := filepath.Join(home, ".dibs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	} // #nosec G703 -- isolated fixture state
	// #nosec G304 G703 -- fixed daemon log below this invocation's fixture home
	log, err := os.OpenFile(filepath.Join(home, "daemon.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	// #nosec G204 G702 -- signature-verified archive, public go install, or checked public cask binary
	cmd := exec.CommandContext(r.ctx, filepath.Join(bin, "dibd"), "-dir", dir, "-addr", addr)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env, home, log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready, cancel := context.WithTimeout(r.ctx, time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	secret, err := awaitDaemon(ready, client, addr, dir)
	if err != nil {
		return fmt.Errorf("private daemon readiness (see %s): %w", log.Name(), err)
	}
	for i, protocol := range []string{"2026-07-28", "2025-11-25"} {
		call := func(name string, args map[string]any) (map[string]any, error) {
			return callTool(r.ctx, client, addr, secret, protocol, name, args)
		}
		if err := coordination(call, home, i); err != nil {
			return fmt.Errorf("%s: %w", protocol, err)
		}
		r.report.MCPRoundTrips++
	}
	return nil
}

func awaitDaemon(ctx context.Context, client *http.Client, addr, dir string) (string, error) {
	var secret []byte
	for {
		var err error
		secret, err = os.ReadFile(filepath.Join(dir, "local.secret")) // #nosec G304 G703 -- fixture daemon's own credential
		if err == nil {
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/board", nil)
			if requestErr != nil {
				return "", requestErr
			}
			req.Header.Set("X-Dibs-Local", strings.TrimSpace(string(secret)))
			resp, requestErr := client.Do(req)
			if requestErr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return strings.TrimSpace(string(secret)), nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type toolCaller func(string, map[string]any) (map[string]any, error)

func coordination(call toolCaller, home string, index int) error {
	names := []string{fmt.Sprintf("surface-a-%d", index), fmt.Sprintf("surface-b-%d", index)}
	tokens := make([]string, 2)
	for i, name := range names {
		token, d, err := registerWork(call, home, name)
		if err != nil {
			return err
		}
		tokens[i] = token
		if i == 1 {
			overlaps, _ := d["overlaps"].([]any)
			encoded, _ := json.Marshal(overlaps)
			if len(overlaps) == 0 || !bytes.Contains(encoded, []byte(names[0])) {
				return errors.New("second declaration omitted the first agent's collision")
			}
		}
	}
	q, err := call("send", map[string]any{
		"token": tokens[0], "to": names[1], "type": "question", "body": "Are you checking this installation?",
	})
	if err != nil {
		return err
	}
	serial, ok := q["msg_serial"].(float64)
	if !ok || serial == 0 {
		return errors.New("question has no serial")
	}
	inbox, err := call("inbox", map[string]any{"token": tokens[1]})
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(inbox)
	if !bytes.Contains(encoded, []byte("Are you checking this installation?")) {
		return errors.New("inbox omitted sent question")
	}
	_, err = call("respond", map[string]any{
		"token": tokens[1], "msg_serial": serial, "disposition": "answer", "body": "Yes, installation checked.",
	})
	if err != nil {
		return err
	}
	back, err := call("read_mail", map[string]any{"token": tokens[0], "msg_serial": serial, "include_body": true})
	if err != nil {
		return err
	}
	encoded, _ = json.Marshal(back)
	if !bytes.Contains(encoded, []byte("Yes, installation checked.")) {
		return errors.New("sender cannot read the answer")
	}
	return nil
}

func registerWork(call toolCaller, home, name string) (string, map[string]any, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	a, err := call("register", map[string]any{
		"name": name, "description": "public installation smoke agent", "pid": os.Getpid(),
		"nonce": hex.EncodeToString(nonce), "cwd": home, "host": host,
	})
	if err != nil {
		return "", nil, err
	}
	token, _ := a["token"].(string)
	if token == "" {
		return "", nil, errors.New("registration returned no credential")
	}
	if _, err := call("check_in", map[string]any{"token": token}); err != nil {
		return "", nil, err
	}
	d, err := call("declare", map[string]any{
		"token": token, "text": "implement the public installation coordination check",
		"activity": "implement", "dirs": []string{filepath.Join(home, "work")},
	})
	if err != nil {
		return "", nil, err
	}
	if d["slot_id"] == nil {
		return "", nil, errors.New("declaration returned no slot")
	}
	return token, d, nil
}

func callTool(
	ctx context.Context, client *http.Client, addr, secret, protocol, name string, args map[string]any,
) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", protocol)
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d", name, resp.StatusCode)
	}
	var env struct {
		Error  json.RawMessage
		Result struct {
			IsError bool
			Content []struct{ Type, Text string }
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&env); err != nil {
		return nil, err
	}
	if (len(env.Error) > 0 && string(env.Error) != "null") || env.Result.IsError || len(env.Result.Content) == 0 {
		return nil, fmt.Errorf("%s refused (private fixture details omitted)", name)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &result); err != nil {
		return nil, err
	}
	if result["code"] != nil {
		return nil, fmt.Errorf("%s returned %v", name, result["code"])
	}
	return result, nil
}
