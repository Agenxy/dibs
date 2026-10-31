// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
	notifyapp "github.com/agenxy/dibs/internal/notify/app"
	"github.com/agenxy/dibs/internal/testport"
)

// An actual daemon, actual signed notifier, kernel audit evidence and shipped
// reopen handlers. Compile-time fixtures replace only OS posting/user gestures.
// No injected verifier, waiter, HumanAgent call or profile setter in the test.
func TestNativeHumanColdReopenUsesCurrentDaemonAndAttestedPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("", "dibs-native-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	contents := filepath.Join(root, "Dibs.app", "Contents")
	macos := filepath.Join(contents, "MacOS")
	res := filepath.Join(contents, "Resources")
	for _, dir := range []string{macos, res, filepath.Join(res, "Fonts"), filepath.Join(root, "board"), filepath.Join(root, "posted"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, body []byte) {
		t.Helper()
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(contents, "Info.plist"), []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.native-reopen-fixture</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`))
	source, err := notifyapp.ReadSource("../../internal/notify/app")
	if err != nil {
		t.Fatal(err)
	}
	swift := filepath.Join(root, "notify.swift")
	write(swift, source)
	for _, name := range []string{"AtkinsonHyperlegibleNext[wght].ttf", "AtkinsonHyperlegibleMono[wght].ttf"} {
		body, err := os.ReadFile(filepath.Join("../../internal/notify/app/fonts", name))
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(res, "Fonts", name), body)
	}
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
	helper := filepath.Join(macos, "dibs-notify")
	daemon := filepath.Join(macos, "dibd")
	run("swiftc", "-D", "DIBS_BROKER_FIXTURE", "-D", "DIBS_READING_FIXTURE", "-o", helper, swift)
	icon := filepath.Join(root, "icon")
	run("swiftc", "-o", icon, "../../internal/notify/app/icon_darwin.swift")
	run(icon, filepath.Join(res, "Dibs.png"), "84")
	run("go", "build", "-o", daemon, ".")
	run("codesign", "--force", "--sign", "-", filepath.Join(root, "Dibs.app"))
	profile, posted := filepath.Join(root, "board"), filepath.Join(root, "posted")
	env := append(os.Environ(), "HOME="+filepath.Join(root, "home"), "DIBS_DIR="+profile, "DIBS_NOTIFY=", "DIBS_BROKER_FIXTURE_DIR="+root, "DIBS_BROKER_POSTED_DIR="+posted, "DIBS_BROKER_DISMISS_POSTED=1", "DIBS_BROKER_PID_FILE="+filepath.Join(root, "broker.pid"))
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	port.Release(t)
	log, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	child := exec.CommandContext(ctx, daemon, "-dir", profile, "-addr", port.Addr, "-allow-parallel")
	child.Env = env
	child.Dir = root
	child.Stdout = log
	child.Stderr = log
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	defer func() {
		_ = child.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			if err != nil && !t.Failed() {
				t.Errorf("daemon exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop after SIGTERM")
			_ = child.Process.Kill()
			if err := <-exited; err == nil {
				t.Error("killed daemon reported success")
			}
		}
		if t.Failed() {
			raw, _ := os.ReadFile(log.Name())
			t.Logf("daemon log: %s", raw)
		}
	}()
	client := &http.Client{Transport: localtransport.RoundTripper(profile, localtransport.Local, nil), Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(profile, "dibd.sock")); err == nil {
			break
		}
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("daemon startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not bind")
		}
		time.Sleep(20 * time.Millisecond)
	}
	secretBytes, err := os.ReadFile(filepath.Join(profile, "local.secret"))
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(string(secretBytes))
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		return nativeToolCall(t, client, secret, name, args)
	}
	reg := call("register", map[string]any{"name": "native-requester", "nonce": "native-reopen-fixture"})
	token, _ := reg["token"].(string)
	if token == "" {
		t.Fatalf("register: %v", reg)
	}
	// The sending process exits before any response. Its token remains only as a
	// read-side test observer; no original pipe/process owns the ability to answer.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	send := func(kind string) (uint64, string) {
		t.Helper()
		out := filepath.Join(root, kind+"-sent.json")
		sender := exec.CommandContext(ctx, self, "-test.run=^TestNativeHumanAskerProcess$")
		sender.Env = append(append([]string(nil), env...), "DIBS_NATIVE_ASKER=1", "DIBS_NATIVE_TOKEN="+token, "DIBS_NATIVE_KIND="+kind, "DIBS_NATIVE_RESULT="+out)
		if raw, err := sender.CombinedOutput(); err != nil {
			t.Fatalf("asker: %v %s", err, raw)
		}
		var sent map[string]any
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &sent); err != nil {
			t.Fatal(err)
		}
		serial := uint64(sent["msg_serial"].(float64))
		if sent["human_route"] != "desktop" {
			t.Fatalf("desktop setup: %v", sent)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			files, _ := filepath.Glob(filepath.Join(posted, fmt.Sprintf("dibs.msg.*.%d.plist", serial)))
			if len(files) == 1 {
				return serial, files[0]
			}
			if time.Now().After(deadline) {
				t.Fatal("actual main/presenter never persisted notification")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	reopen := func(id, path, action, text string, choice int, extra ...string) map[string]any {
		t.Helper()
		view := filepath.Join(root, fmt.Sprintf("view-%d.json", time.Now().UnixNano()))
		cmd := exec.CommandContext(ctx, helper)
		cmd.Env = append(append([]string(nil), env...), "DIBS_READING_FIXTURE=1", "DIBS_READING_POSTED="+path, "DIBS_READING_NOTIFICATION_ID="+id, "DIBS_READING_WINDOW="+view, "DIBS_NATIVE_REOPEN_VIEW="+view, "DIBS_NATIVE_REOPEN_ACTION="+action, "DIBS_NATIVE_REOPEN_TEXT="+text, "DIBS_NATIVE_REOPEN_CHOICE="+strconv.Itoa(choice))
		if shot := os.Getenv("DIBS_NATIVE_REOPEN_ARTIFACTS"); shot != "" {
			cmd.Env = append(cmd.Env, "DIBS_NATIVE_REOPEN_SCREENSHOT="+filepath.Join(shot, action+"-"+strconv.Itoa(choice)+".png"))
		}
		cmd.Env = append(cmd.Env, extra...)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			debug, _ := os.ReadFile(view)
			t.Fatalf("cold reopen: %v %s; view %s", err, raw, debug)
		}
		body, err := os.ReadFile(view)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	serial, path := send("question")
	id := strings.TrimSuffix(filepath.Base(path), ".plist")
	// Broker restart: kill only our recorded helper child, then launch the same
	// installed bundle with no arguments, as Notification Center does.
	pidRaw, err := os.ReadFile(filepath.Join(root, "broker.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidRaw))
	if err != nil || pid <= 1 {
		t.Fatalf("broker pid: %q %v", pidRaw, err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	lightExtra := []string{"DIBS_NATIVE_REOPEN_APPEARANCE=light"}
	if shots := os.Getenv("DIBS_NATIVE_REOPEN_ARTIFACTS"); shots != "" {
		lightExtra = append(lightExtra, "DIBS_NATIVE_REOPEN_SCREENSHOT="+filepath.Join(shots, "pending-light.png"))
	}
	light := reopen(id, path, "", "", -1, lightExtra...)
	if light["reply"] != true || light["confirmed"] != true {
		t.Fatalf("light pending reopen not answerable: %v", light)
	}
	pending := reopen(id, path, "Send", "typed after helper restart", 1)
	if pending["confirmed"] != true || pending["reply"] != true {
		t.Fatalf("pending reopen is not answerable: %v", pending)
	}
	msg := call("read_mail", map[string]any{"token": token, "msg_serial": serial, "include_body": true})
	raw, _ := json.Marshal(msg)
	if !bytes.Contains(raw, []byte("typed after helper restart")) || !bytes.Contains(raw, []byte("Second option")) {
		t.Fatalf("answer not recorded: %s", raw)
	}
	answered := reopen(id, path, "", "", -1, "DIBS_READING_LEGACY_PROFILE=1", "DIBS_NATIVE_PROFILE_DISCOVERY=1")
	if answered["state"] != "answered" || answered["reply"] != false || strings.Contains(fmt.Sprint(answered["hint"]), "send") {
		t.Fatalf("answered reopen not read-only: %v", answered)
	}
	liveSerial, livePath := send("question")
	liveID := strings.TrimSuffix(filepath.Base(livePath), ".plist")
	nativeLiveBrokerReopen(t, ctx, helper, env, profile, root, liveID)
	liveMail := call("read_mail", map[string]any{"token": token, "msg_serial": liveSerial, "include_body": true})
	liveBytes, _ := json.Marshal(liveMail)
	if !bytes.Contains(liveBytes, []byte("via live broker")) || !bytes.Contains(liveBytes, []byte("Third option")) {
		t.Fatalf("live body action failed: %s", liveBytes)
	}
	// A general local-secret holder is not a native human peer. Header-selected
	// PIDs, a declared bundle id and the correct agent/board secret cannot help.
	for _, method := range []string{"message", "answer"} {
		body, _ := json.Marshal(map[string]any{"node": strings.Split(id, ".")[2], "serial": serial, "action": "answer", "text": "forged"})
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://localhost/api/human/native/"+method, bytes.NewReader(body))
		req.Header.Set("X-Dibs-Local", secret)
		req.Header.Set("X-Dibs-PID", strconv.Itoa(pid))
		req.Header.Set("X-Dibs-Bundle", "org.agenxy.dibs.native-reopen-fixture")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("same UID non-notifier %s accepted: %d %s", method, resp.StatusCode, data)
		}
	}
	_, requestPath := send("request")
	requestID := strings.TrimSuffix(filepath.Base(requestPath), ".plist")
	reqView := reopen(requestID, requestPath, "Approve", "reviewed current request", -1)
	if reqView["reply"] != true {
		t.Fatalf("request not answerable: %v", reqView)
	}
	approved := reopen(requestID, requestPath, "", "", -1)
	if approved["state"] != "approved" || approved["reply"] != false {
		t.Fatalf("approved request is still actionable: %v", approved)
	}
	withdrawn, withdrawnPath := send("question")
	call("respond", map[string]any{"token": token, "msg_serial": withdrawn, "disposition": "withdraw"})
	withdrawnView := reopen(strings.TrimSuffix(filepath.Base(withdrawnPath), ".plist"), withdrawnPath, "", "", -1)
	if withdrawnView["state"] != "withdrawn" || withdrawnView["reply"] != false {
		t.Fatalf("withdrawn question still actionable: %v", withdrawnView)
	}
	// Kernel audit tokens bind the process incarnation, not only its PID. Keep
	// the real notifier waiting on this fixture server while checking both the
	// actual token and a token with only pidversion changed.
	auditProfile := filepath.Join(root, "audit")
	if err := os.Mkdir(auditProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(map[string]any{"title": "Dibs · audit asks", "sender": "audit", "kind": "question", "body": "Audit identity fixture", "profile": auditProfile})
	auditPath := filepath.Join(root, "audit.plist")
	write(auditPath, []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>title</key><string>Audit</string><key>subtitle</key><string>audit</string><key>body</key><string>Audit</string><key>userInfo</key><dict><key>org.agenxy.dibs.reading.v2</key><data>`+base64.StdEncoding.EncodeToString(detail)+`</data></dict></dict></plist>`))
	nativeServerMustReceiveNoRequest(t, ctx, helper, env, auditProfile, auditPath, root)
	// The server below is this test process. Give its isolated fixture bundle
	// that exact code identity as the expected daemon, never an auth bypass.
	auditBase := filepath.Join(root, "audit-app")
	if err := os.Mkdir(auditBase, 0o700); err != nil {
		t.Fatal(err)
	}
	auditApp := filepath.Join(auditBase, "Dibs.app")
	run("/bin/cp", "-R", filepath.Join(root, "Dibs.app"), auditApp)
	expectedDaemon := filepath.Join(auditApp, "Contents", "MacOS", "dibd")
	fixtureCode, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expectedDaemon+".next", fixtureCode, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(expectedDaemon+".next", expectedDaemon); err != nil {
		t.Fatal(err)
	}
	run("codesign", "--force", "--sign", "-", auditApp)
	helper = filepath.Join(auditApp, "Contents", "MacOS", "dibs-notify")
	listener, err := localtransport.Listen(auditProfile)
	if err != nil {
		t.Fatal(err)
	}
	proof := make(chan error, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second, ConnContext: localtransport.ConnContext,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			audit := localtransport.PeerAuditToken(r.Context())
			verify := func(token []byte) error {
				cmd := exec.CommandContext(ctx, helper, "--verify-notification-peer")
				cmd.Stdin = bytes.NewReader(token)
				return cmd.Run()
			}
			var fault error
			if len(audit) != 32 {
				fault = fmt.Errorf("kernel audit token length %d", len(audit))
			} else if err := verify(audit); err != nil {
				fault = fmt.Errorf("real notifier refused: %w", err)
			} else {
				audit[28] ^= 0x80
				if verify(audit) == nil {
					fault = fmt.Errorf("same PID with wrong pidversion accepted")
				}
			}
			proof <- fault
			nativeHumanJSON(w, http.StatusOK, map[string]any{"ok": true, "title": "Dibs · audit asks", "view": map[string]any{"pending": false, "state": "withdrawn", "notice": map[string]any{"type": "question", "who": "audit", "body": "Audit identity fixture"}}})
		}),
	}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	reopen("dibs.msg.audit.1", auditPath, "", "", -1)
	select {
	case err := <-proof:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("missing audit-token proof")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	// Upgrade window: the trusted server process still runs its old ad-hoc
	// image, while the complete bundle now carries a different signed daemon.
	newCode, err := os.ReadFile(daemon)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expectedDaemon+".next", newCode, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(expectedDaemon+".next", expectedDaemon); err != nil {
		t.Fatal(err)
	}
	run("codesign", "--force", "--sign", "-", auditApp)
	nativeServerMustReceiveNoRequest(t, ctx, helper, env, auditProfile, auditPath, root)
}

func nativeServerMustReceiveNoRequest(t *testing.T, ctx context.Context, helper string, env []string, profile, posted, root string) {
	t.Helper()
	listener, err := localtransport.Listen(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	seen := make(chan int, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			seen <- -1
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var first [1]byte
		n, _ := conn.Read(first[:])
		seen <- n
		if n > 0 {
			body := `{"ok":true,"title":"Dibs · forged asks","view":{"pending":false,"state":"withdrawn","notice":{"type":"question","who":"forged","body":"Forged current text"}}}`
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		}
	}()
	view := filepath.Join(root, "refused-server.json")
	cmd := exec.CommandContext(ctx, helper)
	cmd.Env = append(append([]string(nil), env...), "DIBS_READING_FIXTURE=1", "DIBS_READING_POSTED="+posted, "DIBS_READING_NOTIFICATION_ID=dibs.msg.audit.1", "DIBS_NATIVE_REOPEN_VIEW="+view)
	raw, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("untrusted/replaced server accepted: %s", raw)
	}
	select {
	case n := <-seen:
		if n != 0 {
			t.Fatalf("untrusted server received %d request bytes", n)
		}
	case <-ctx.Done():
		t.Fatal("missing server refusal")
	}
	data, err := os.ReadFile(view)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["confirmed"] != false || !strings.Contains(fmt.Sprint(result["body"]), "The board is restarting or changed") {
		t.Fatalf("server refusal lacks honest route guidance: %s", data)
	}
}

func nativeLiveBrokerReopen(t *testing.T, ctx context.Context, helper string, env []string, profile, root, id string) {
	t.Helper()
	lockDir := filepath.Join(root, "live-lock")
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, helper, "--broker")
	cmd.Env = append(append([]string(nil), env...), "DIBS_BROKER_FIXTURE_DIR="+lockDir, "DIBS_BROKER_DISMISS_POSTED=0")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil && !t.Failed() {
			t.Errorf("live broker exit: %v", err)
		}
	}()
	frames := make(chan map[string]any, 16)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var f map[string]any
			if json.Unmarshal(scanner.Bytes(), &f) == nil {
				frames <- f
			}
		}
	}()
	read := func(event string) map[string]any {
		t.Helper()
		select {
		case f := <-frames:
			if f["event"] != event {
				t.Fatalf("want live %s, got %v", event, f)
			}
			return f
		case <-time.After(10 * time.Second):
			t.Fatalf("missing live %s", event)
		}
		return nil
	}
	write := func(f map[string]any) {
		t.Helper()
		f["v"] = 1
		f["id"] = id
		if err := json.NewEncoder(input).Encode(f); err != nil {
			t.Fatal(err)
		}
	}
	read("ready")
	write(map[string]any{"type": "post", "title": "Dibs · cached asks", "excerpt": "cached", "detail": map[string]string{"sender": "cached", "body": "Stale notification body", "kind": "question"}, "replyKind": "question", "choices": []string{"Stale choice"}, "buttons": []string{"Later", "Reply…"}, "until": float64(time.Now().Add(time.Minute).Unix()), "profile": profile})
	read("posted")
	write(map[string]any{"type": "fixture_response", "action": "com.apple.UNNotificationDefaultActionIdentifier"})
	view := read("native_reading")["view"].(map[string]any)
	if view["reply"] != true || strings.Contains(fmt.Sprint(view["body"]), "Stale") || len(view["choices"].([]any)) != 3 {
		t.Fatalf("live body did not fetch current message: %v", view)
	}
	write(map[string]any{"type": "fixture_edit", "text": "via live broker"})
	read("editing")
	write(map[string]any{"type": "fixture_select", "index": 2})
	read("selected")
	write(map[string]any{"type": "fixture_window", "action": "Send"})
	read("native_sent")
	// Lose the broker's entry and open its actual persisted notification. This
	// takes the broker delegate's other reopen branch, independently of Handler.
	write(map[string]any{"type": "cancel"})
	write(map[string]any{"type": "fixture_reopen"})
	final := read("native_reading")["view"].(map[string]any)
	if final["reply"] != false || !strings.Contains(fmt.Sprint(final["body"]), "answered") || strings.Contains(fmt.Sprint(final["hint"]), "send") {
		t.Fatalf("cold broker remained actionable: %v", final)
	}
}

func nativeToolCall(t *testing.T, client *http.Client, secret, name string, args map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req, _ := http.NewRequestWithContext(t.Context(), "POST", "http://localhost/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil || len(rpc.Result.Content) == 0 {
		t.Fatalf("%s rpc: %v %+v", name, err, rpc)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	if result["error"] != nil {
		t.Fatalf("%s: %v", name, result)
	}
	return result
}

func TestNativeHumanAskerProcess(t *testing.T) {
	if os.Getenv("DIBS_NATIVE_ASKER") != "1" {
		return
	}
	profile := os.Getenv("DIBS_DIR")
	secret, err := os.ReadFile(filepath.Join(profile, "local.secret"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: localtransport.RoundTripper(profile, localtransport.Local, nil), Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	args := map[string]any{"token": os.Getenv("DIBS_NATIVE_TOKEN"), "to": "human", "type": os.Getenv("DIBS_NATIVE_KIND"), "body": "Full current question, preserved after the original asker exits."}
	if args["type"] == "question" {
		args["choices"] = []string{"First option", "Second option", "Third option"}
	}
	sent := nativeToolCall(t, client, strings.TrimSpace(string(secret)), "send", args)
	data, _ := json.Marshal(sent)
	if err := os.WriteFile(os.Getenv("DIBS_NATIVE_RESULT"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
