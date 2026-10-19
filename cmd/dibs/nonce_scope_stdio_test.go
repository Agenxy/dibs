// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type processNonceProbe struct {
	Known bool
	First string
	Again string
}

func TestNonceScopeProcessHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_NONCE_SCOPE") != "1" {
		return
	}
	thread := os.Getenv("DIBS_TEST_NONCE_THREAD")
	params := map[string]any{"name": "register", "arguments": map[string]any{"name": "worker", "cwd": "/project"}}
	if thread != "" {
		params["_meta"] = map[string]any{"threadId": thread}
	}
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	if err != nil {
		t.Fatal(err)
	}
	nonce := func() string {
		var call struct {
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(enrichRegister(request), &call); err != nil {
			t.Fatal(err)
		}
		value, _ := call.Params.Arguments["nonce"].(string)
		return value
	}
	p := processNonceProbe{First: nonce(), Again: nonce(), Known: thread != "" || bridgeStarted() != ""}
	if err := json.NewEncoder(os.Stdout).Encode(p); err != nil {
		t.Fatal(err)
	}
}

// Both children share the same launcher, cache directory and role. Only their
// measured process incarnations differ; no test supplies a nonce or cache scope.
func TestAnonymousBridgeCacheBelongsOnlyToItsOwnProcess(t *testing.T) {
	dir := t.TempDir()
	a, b := runNonceScopeProcess(t, dir, ""), runNonceScopeProcess(t, dir, "")
	if a.Known && b.Known && a.First == b.First {
		t.Fatal("two bridge processes borrowed one anonymous credential")
	}
}

func runNonceScopeProcess(t *testing.T, dir, thread string) processNonceProbe {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNonceScopeProcessHelper$")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "USERPROFILE=" + dir, "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
		"DIBS_DIR=" + dir, "DIBS_HOST_ID=nonce-scope-fixture", "DIBS_NOTIFY=off", "DIBS_TEST_NONCE_SCOPE=1",
		"DIBS_TEST_NONCE_THREAD=" + thread,
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("setup: real bridge process", err)
	}
	var p processNonceProbe
	if err := json.NewDecoder(strings.NewReader(string(out))).Decode(&p); err != nil {
		t.Fatal("setup: bridge response", err)
	}
	if p.Known && (p.First == "" || p.First != p.Again) {
		t.Fatal("known process lost its own recovery credential")
	}
	if !p.Known && (p.First != "" || p.Again != "") {
		t.Fatal("unknown process injected a credential")
	}
	return p
}

func TestConcurrentBridgeProcessesClaimALegacyCredentialOnce(t *testing.T) {
	dir := t.TempDir()
	writeLegacyRecoveryCache(t, dir, map[string]string{"/project\x00worker": "legacy-original"})
	threads := []string{"01a10520-0000-7000-8000-000000000001", "01a10520-0000-7000-8000-000000000002"}
	var probes [2]processNonceProbe
	var wg sync.WaitGroup
	for i, thread := range threads {
		wg.Add(1)
		go func() { defer wg.Done(); probes[i] = runNonceScopeProcess(t, dir, thread) }()
	}
	wg.Wait()
	claims := 0
	for _, p := range probes {
		if p.First == "legacy-original" {
			claims++
		}
	}
	if claims != 1 || probes[0].First == probes[1].First {
		t.Fatal("concurrent bridge processes did not transfer the legacy credential to exactly one session")
	}
	if loadRecoveryCache(t, dir)["/project\x00worker"] != "" {
		t.Fatal("atomic migration retained an unscoped copy")
	}
}

func TestRemoteTunnelCannotInjectThisMachinesCachedCredential(t *testing.T) {
	wasThread := threadServed()
	t.Cleanup(func() { noteThread(wasThread) })
	t.Setenv("DIBS_DIR", t.TempDir())
	wasRemote := remoteSession
	t.Cleanup(func() { remoteSession = wasRemote })
	if err := parseBridgeArgs([]string{remoteSessionFlag}); err != nil {
		t.Fatal(err)
	}
	rememberNonce("/project", "worker", "private-local-credential")
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{"name":"worker","cwd":"/project"},"_meta":{"threadId":"a-remote-thread"}}}`)
	var call struct {
		Params struct {
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(enrichRegister(request), &call); err != nil {
		t.Fatal(err)
	}
	if call.Params.Arguments["nonce"] != nil || call.Params.Arguments["recovery_nonces"] != nil {
		t.Fatal("a remote thread received a credential from this machine's cache")
	}
}
