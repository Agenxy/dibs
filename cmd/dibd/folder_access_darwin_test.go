// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/folderaccess"
	"github.com/agenxy/dibs/internal/testport"
)

func TestFolderAccessDaemonProcess(t *testing.T) {
	mode := os.Getenv("DIBS_FOLDER_ACCESS_TEST_CHILD")
	if mode == "" {
		return
	}
	newFolderAccess = func() *folderaccess.Monitor {
		return folderaccess.New(os.Getenv("HOME"), func(string) error {
			switch mode {
			case "pending":
				select {}
			case "denied":
				return os.ErrPermission
			default:
				return nil
			}
		})
	}
	flag.CommandLine = flag.NewFlagSet("dibd", flag.ContinueOnError)
	os.Args = strings.Split(os.Getenv("DIBS_FOLDER_ACCESS_TEST_ARGS"), "\n")
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func TestFolderAccessRunsThroughDaemonRegistrationAndTheRealBoard(t *testing.T) {
	for _, state := range []string{"pending", "denied", "granted"} {
		t.Run(state, func(t *testing.T) {
			f := bootFolderAccessDaemon(t, state)
			cwd := filepath.Join(f.home, "Desktop", "project")
			registered := f.register(t, "local", cwd, "")
			if _, ok := registered["agent_id"].(string); !ok {
				t.Fatal("setup: registration did not succeed:", registered)
			}
			var board struct {
				FolderAccess []folderaccess.Observation `json:"folder_access"`
				Advice       string                     `json:"folder_access_advice"`
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				body := f.request(t, http.MethodGet, "/api/board", nil)
				if err := json.Unmarshal(body, &board); err != nil {
					t.Fatal(err)
				}
				if len(board.FolderAccess) == 1 && board.FolderAccess[0].State == state {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("actual daemon did not publish %s: %s", state, body)
				}
				time.Sleep(time.Millisecond)
			}
			if board.FolderAccess[0].Folder != "Desktop" || board.FolderAccess[0].Requested {
				t.Fatalf("registration probed unrelated roots or claimed acquisition: %+v", board)
			}
			if (board.Advice != "") != (state != "granted") {
				t.Fatalf("folder advice disagrees with real probe: %+v", board)
			}
			page := string(f.request(t, http.MethodGet, "/", nil))
			aside := regexp.MustCompile(`<aside id="folder-access-advice"[^>]*>([^<]*)</aside>`).FindStringSubmatch(page)
			if len(aside) != 2 || html.UnescapeString(aside[1]) != board.Advice {
				t.Fatalf("actual rendered board omitted the access diagnosis: %q", aside)
			}
			remote := f.register(t, "remote", filepath.Join(f.home, "Documents", "elsewhere"), "a-different-machine")
			if _, ok := remote["agent_id"].(string); !ok {
				t.Fatal("setup: foreign registration failed:", remote)
			}
			body := f.request(t, http.MethodGet, "/api/board", nil)
			if err := json.Unmarshal(body, &board); err != nil || len(board.FolderAccess) != 1 {
				t.Fatalf("foreign path triggered a local folder read: %v %s", err, body)
			}
			var acquisition struct {
				Requested bool                       `json:"requested"`
				Rows      []folderaccess.Observation `json:"folder_access"`
			}
			if err := json.Unmarshal(f.request(t, http.MethodPost, "/api/folder-access/request", nil), &acquisition); err != nil {
				t.Fatal(err)
			}
			if !acquisition.Requested || len(acquisition.Rows) != 3 {
				t.Fatalf("explicit acquisition did not request the three roots: %+v", acquisition)
			}
		})
	}
}

type folderDaemon struct {
	url, secret, home string
	client            *http.Client
}

func bootFolderAccessDaemon(t *testing.T, mode string) *folderDaemon {
	t.Helper()
	home, dir := t.TempDir(), t.TempDir()
	hash, err := adminpw.Hash("folder-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.hash"), []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Desktop", "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFolderAccessDaemonProcess$")
	args := []string{"dibd", "-dir", dir, "-addr", port.Addr, "-allow-parallel"}
	cmd.Env = append(nativeProbeEnv(home, "", ""), "DIBS_NOTIFY=off", "GORACE=atexit_sleep_ms=0", "DIBS_FOLDER_ACCESS_TEST_CHILD="+mode, "DIBS_FOLDER_ACCESS_TEST_ARGS="+strings.Join(args, "\n"))
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	port.Release(t)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	ready, drained := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(pipe)
		up := false
		for scanner.Scan() {
			if !up && strings.Contains(scanner.Text(), "dibd up") {
				up = true
				close(ready)
			}
		}
	}()
	t.Cleanup(func() { cancel(); _ = cmd.Wait(); <-drained })
	select {
	case <-ready:
	case <-drained:
		t.Fatal("setup: daemon exited before readiness")
	case <-time.After(10 * time.Second):
		t.Fatal("setup: daemon did not become ready")
	}
	secret, err := os.ReadFile(filepath.Join(dir, "local.secret"))
	if err != nil {
		t.Fatal(err)
	}
	return &folderDaemon{url: "http://" + port.Addr, secret: strings.TrimSpace(string(secret)), home: home, client: &http.Client{Timeout: 4 * time.Second}}
}

func (f *folderDaemon) request(t *testing.T, method, path string, body []byte) []byte {
	t.Helper()
	req, err := http.NewRequest(method, f.url+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Dibs-Local", f.secret)
	req.Header.Set("Content-Type", "application/json")
	if path == "/" {
		req.Header.Set("X-Dibs-Admin", "folder-fixture-password")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: %s %s: status%d error%v %s", method, path, resp.StatusCode, err, out)
	}
	return out
}

func (f *folderDaemon) register(t *testing.T, name, cwd, host string) map[string]any {
	t.Helper()
	args := map[string]any{"name": name, "nonce": "folder-probe-" + name, "kind": "persistent", "cwd": cwd}
	params := map[string]any{"name": "register", "arguments": args}
	if host != "" {
		params["_meta"] = map[string]any{"com.dibs/host": host}
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	out := f.request(t, http.MethodPost, "/mcp", body)
	if err := json.Unmarshal(out, &reply); err != nil || len(reply.Result.Content) == 0 {
		t.Fatalf("setup: unreadable register reply: %v %s", err, out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
