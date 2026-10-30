// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	cmd, err := r.newCommand(r.ctx, env, filepath.Join(bin, "dibd"), "-dir", dir, "-addr", addr)
	if err != nil {
		return err
	}
	cmd.Dir, cmd.Stdout, cmd.Stderr = home, log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := r.awaitInstalledBoard(bin, env); err != nil {
		return fmt.Errorf("private daemon readiness (see %s): %w", log.Name(), err)
	}
	return r.coordinateInstalled(bin, home, env, 0, false)
}

func (r *runner) coordinateInstalled(bin, home string, env []string, base int, service bool) error {
	for i, protocol := range []string{"2026-07-28", "2025-11-25"} {
		if err := r.installedBridge(bin, home, env, protocol, base+i); err != nil {
			return fmt.Errorf("%s: %w", protocol, err)
		}
		r.report.MCPRoundTrips++
		if service {
			r.report.ServiceMCPRoundTrips++
		}
	}
	return nil
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
