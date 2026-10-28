// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/testexec"
	"github.com/agenxy/dibs/internal/testport"
)

// Both executables enter through their real command dispatch. The guest has
// only an invitation and address (plus trust for this fixture's HTTPS origin),
// never the board secret or the recipient's token. The daemon's --public-url
// loopback listener sits behind the TLS proxy it is designed to serve.
func TestProcessCLIThroughPublicDaemon(t *testing.T) {
	if os.Getenv("DIBS_PROCESS_CLI_DAEMON") == "1" {
		os.Args = []string{
			"dibd", "--dir", os.Getenv("DIBS_PROCESS_CLI_DIR"),
			"--addr", os.Getenv("DIBS_PROCESS_CLI_PRIVATE"), "--allow-parallel",
			"--public-url", os.Getenv("DIBS_PROCESS_CLI_URL"),
			"--public-addr", os.Getenv("DIBS_PROCESS_CLI_PUBLIC"),
		}
		flag.CommandLine = cloudFlagSet()
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(bin, "--version"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("process-cli-local-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := invites.Store{Dir: dir}
	key, err := store.Mint("labs-broker", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := store.Mint("other-broker", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	private := testport.Reserve(t, "tcp", "127.0.0.1:0")
	public := testport.Reserve(t, "tcp", "127.0.0.1:0")
	proxy := httptest.NewUnstartedServer(nil)
	target, err := url.Parse("http://" + public.Addr)
	if err != nil {
		t.Fatal(err)
	}
	forward := httputil.NewSingleHostReverseProxy(target)
	var loseRegistrationReply atomic.Bool
	forward.ModifyResponse = func(*http.Response) error {
		if loseRegistrationReply.Swap(false) {
			return errors.New("fixture discards a committed registration reply")
		}
		return nil
	}
	proxy.Config.Handler = forward
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	daemon := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessCLIThroughPublicDaemon$")
	daemon.Env = append(nativeProbeEnv(t.TempDir(), "", ""),
		"DIBS_PROCESS_CLI_DAEMON=1", "DIBS_PROCESS_CLI_DIR="+dir,
		"DIBS_PROCESS_CLI_PRIVATE="+private.Addr, "DIBS_PROCESS_CLI_PUBLIC="+public.Addr,
		"DIBS_PROCESS_CLI_URL="+proxy.URL)
	var logs bytes.Buffer
	daemon.Stdout, daemon.Stderr = &logs, &logs
	private.Release(t)
	public.Release(t)
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	for _, addr := range []string{private.Addr, public.Addr} {
		ready := false
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				ready = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !ready {
			t.Fatal("actual daemon did not start its listeners")
		}
	}
	guestDir := t.TempDir()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(guestDir, "trusted-certs.pem"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	recoveryNonce := ""
	runCLI := func(address, dataDir, invite, token, stdin string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_DIR="+dataDir,
			"DIBS_ADDR="+address, "DIBS_INVITE="+invite, "DIBS_TOKEN="+token,
			"DIBS_NONCE="+recoveryNonce)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cli := func(invite, token, stdin string, args ...string) (string, error) {
		return runCLI(proxy.URL, guestDir, invite, token, stdin, args...)
	}
	decode := func(out string, err error) map[string]any {
		t.Helper()
		var value map[string]any
		if err != nil || json.Unmarshal([]byte(out), &value) != nil {
			t.Fatalf("CLI did not succeed with JSON: %v %s", err, out)
		}
		return value
	}
	registered := decode(cli(key, "", "", "register", "--name", "labs-broker", "--json"))
	token, _ := registered["token"].(string)
	nonce, _ := registered["nonce"].(string)
	if registered["agent_id"] != "labs-broker" || token == "" || len(nonce) < 32 {
		t.Fatal("register did not return the sender's identity and retained credentials")
	}
	t.Run("recover-own-registration", func(t *testing.T) {
		recoveryNonce = nonce
		defer func() { recoveryNonce = "" }()
		recovered := decode(cli(key, "", "", "register", "--name", "labs-broker", "--json"))
		if recovered["agent_id"] != registered["agent_id"] || recovered["token"] != token || recovered["nonce"] != nonce {
			t.Fatal("retained nonce did not recover the sender's own identity")
		}
	})
	t.Run("failed-registration-retains-attempted-nonce", func(t *testing.T) {
		retryKey, err := store.Mint("retry-broker", time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		loseRegistrationReply.Store(true)
		out, err := cli(retryKey, "", "", "register", "--name", "retry-broker", "--json")
		var refused struct{ Code, Hint, Nonce string }
		if err == nil || json.Unmarshal([]byte(out), &refused) != nil || refused.Code != "E_CLI" || refused.Hint == "" || len(refused.Nonce) < 32 || loseRegistrationReply.Load() {
			t.Fatalf("failed registration lost its retry credential: %v %s", err, out)
		}
		entries, err := store.List()
		if err != nil {
			t.Fatal(err)
		}
		boundID := ""
		for _, entry := range entries {
			if entry.Name == "retry-broker" {
				boundID = entry.AgentID
			}
		}
		if boundID == "" {
			t.Fatal("lost-reply setup never committed and bound its registration")
		}
		recoveryNonce = refused.Nonce
		defer func() { recoveryNonce = "" }()
		retried := decode(cli(retryKey, "", "", "register", "--name", "retry-broker", "--json"))
		if retried["agent_id"] != boundID || retried["nonce"] != refused.Nonce || retried["token"] == nil {
			t.Fatal("registration could not retry with the retained attempted nonce")
		}
	})
	// The private RPC is fixture setup and observation only, never the sender.
	tool := func(name string, args map[string]any) map[string]any {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args},
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+private.Addr+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dibs-Local", "process-cli-local-secret")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var envelope struct {
			Result struct {
				IsError bool
				Content []struct{ Text string }
			}
		}
		if json.NewDecoder(resp.Body).Decode(&envelope) != nil || resp.StatusCode != 200 ||
			envelope.Result.IsError || len(envelope.Result.Content) != 1 {
			t.Fatal("private setup/observation RPC refused")
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	recipient := tool("register", map[string]any{"name": "labs-owner", "nonce": "process-cli-recipient"})
	recipientToken, _ := recipient["token"].(string)
	if recipientToken == "" {
		t.Fatal("recipient setup returned no token")
	}
	t.Run("local-secret-and-plain-receipts", func(t *testing.T) {
		out, err := runCLI("http://"+private.Addr, dir, "", "", "", "register", "--name", "local-process")
		if err != nil || !strings.HasPrefix(out, "agent_id: local-process\ntoken: ") || !strings.Contains(out, "\nnonce: ") {
			t.Fatalf("local registration lost plain credential output: %v %s", err, out)
		}
		var localToken string
		for line := range strings.SplitSeq(out, "\n") {
			if strings.HasPrefix(line, "token: ") {
				localToken = strings.TrimPrefix(line, "token: ")
			}
		}
		out, err = runCLI("http://"+private.Addr, dir, "", localToken, "", "send", "--to", "labs-owner", "local job finished")
		if err != nil || !strings.HasPrefix(out, "sent message ") {
			t.Fatalf("local send failed: %v %s", err, out)
		}
		inbox := tool("inbox", map[string]any{"token": recipientToken})
		found := false
		for _, item := range inbox["messages"].([]any) {
			mail := item.(map[string]any)
			found = found || (mail["from"] == "local-process" && mail["body"] == "local job finished")
		}
		if !found {
			t.Fatal("local send did not enter the recipient's inbox")
		}
	})
	for _, kind := range []string{"notify", "question", "request"} {
		t.Run(kind, func(t *testing.T) {
			body := "job 42: rc=0 state=TERMINAL\nsecond line\n"
			args := []string{"send", "--to", "labs-owner", "--json"}
			input := "-"
			if kind != "notify" {
				args = append(args, "--type", kind)
				input = body
			}
			result := decode(cli(key, token, body, append(args, input)...))
			if result["msg_serial"] == nil {
				t.Fatal("send returned no message serial")
			}
			inbox := tool("inbox", map[string]any{"token": recipientToken})
			messages, _ := inbox["messages"].([]any)
			found := 0
			for _, item := range messages {
				mail, _ := item.(map[string]any)
				if mail["serial"] == result["msg_serial"] {
					full := tool("read_mail", map[string]any{"token": recipientToken, "msg_serial": mail["serial"], "include_body": true})
					envelope, _ := full["message"].(map[string]any)
					if envelope["from"] != "labs-broker" || envelope["type"] != kind || envelope["body"] != body {
						t.Fatalf("wrong sender, type or exact body: %v", envelope)
					}
					found++
				}
			}
			if found != 1 {
				t.Fatalf("recipient inbox has %d matching messages", found)
			}
		})
	}
	other := decode(cli(otherKey, "", "", "register", "--name", "other-broker", "--json"))
	if other["token"] == nil || other["agent_id"] != "other-broker" {
		t.Fatal("other invitation did not bind its own identity")
	}
	for _, bad := range []struct{ name, invite, token, to, code, hint string }{
		{"bad-token", key, "not-a-token", "labs-owner", "E_INVITE_SCOPE", "use the invitation's own name, token and nonce"},
		{"unknown-recipient", key, token, "no-such-agent", "E_NO_AGENT", ""},
		{"other-agent-token", otherKey, token, "labs-owner", "E_INVITE_SCOPE", "use the invitation's own name, token and nonce"},
		{"bad-invite", "dibs_inv_invalid", token, "labs-owner", "E_INVITE_AUTH", "ask the operator for a live invitation"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			for _, asJSON := range []bool{false, true} {
				args := []string{"send", "--to", bad.to}
				if asJSON {
					args = append(args, "--json")
				}
				out, err := cli(bad.invite, bad.token, "", append(args, "must not arrive")...)
				if err == nil || !strings.Contains(out, "hint") || !strings.Contains(out, bad.code) || !strings.Contains(out, bad.hint) {
					t.Fatalf("refusal lost nonzero exit or hint (json=%v): %v %s", asJSON, err, out)
				}
				if asJSON {
					var refused struct{ Code, Hint string }
					if json.Unmarshal([]byte(out), &refused) != nil || refused.Code != bad.code || refused.Hint == "" {
						t.Fatalf("refusal is not a structured JSON error: %s", out)
					}
				}
				if strings.Contains(out, bad.invite) || strings.Contains(out, token) || strings.Contains(out, nonce) {
					t.Fatal("refusal disclosed a credential")
				}
			}
		})
	}
	finalInbox := tool("inbox", map[string]any{"token": recipientToken})
	for _, item := range finalInbox["messages"].([]any) {
		mail := item.(map[string]any)
		if mail["body"] == "must not arrive" {
			t.Fatal("a refused send reached the recipient")
		}
	}
	// A process must not need or silently create a local board secret.
	if _, err := os.Stat(filepath.Join(guestDir, "local.secret")); !os.IsNotExist(err) {
		t.Fatal("guest CLI created or required a board secret")
	}
}
