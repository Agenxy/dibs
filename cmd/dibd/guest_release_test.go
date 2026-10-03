package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This recording executable proves process/issuer wiring, NOT cryptography.
// The real historical signed bundle has a separate network-denied probe.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_GUEST_ADMISSION_COSIGN") == "1" && len(os.Args) > 1 {
		if os.Args[1] == "version" {
			os.Exit(0)
		}
		if os.Args[1] == "verify-blob" {
			os.Exit(guestAdmissionCosign())
		}
	}
	os.Exit(m.Run())
}

func guestAdmissionChecksums() string {
	s := ""
	for _, platform := range []string{"darwin_arm64", "linux_amd64", "linux_arm64"} {
		s += strings.Repeat("a", 64) + "  dibs_0.0.9_" + platform + ".tar.gz\n"
		s += strings.Repeat("b", 64) + "  members/" + platform + "/dibs\n"
	}
	return s
}

func guestAdmissionCosign() int {
	if len(os.Args) != 11 {
		return 10
	}
	flags := map[string]string{}
	for i := 3; i+1 < len(os.Args); i += 2 {
		flags[os.Args[i]] = os.Args[i+1]
	}
	b, err := os.ReadFile(os.Args[2])
	if err != nil || string(b) != guestAdmissionChecksums() {
		return 11
	}
	bundle, err := os.ReadFile(flags["--bundle"])
	if err != nil || string(bundle) != "fixture only, not a signature" {
		return 12
	}
	root, err := os.ReadFile(flags["--trusted-root"])
	hash := sha256.Sum256(root)
	if err != nil || hex.EncodeToString(hash[:]) != "6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66" ||
		flags["--certificate-identity"] != "https://github.com/Agenxy/dibs/.github/workflows/release.yml@refs/tags/v0.0.9" ||
		flags["--certificate-oidc-issuer"] != "https://token.actions.githubusercontent.com" {
		return 13
	}
	// One appended receipt per actual verifier subprocess.
	f, err := os.OpenFile(os.Getenv("DIBS_TEST_GUEST_ADMISSION_RECEIPT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 14
	}
	_, err = f.WriteString("verified fixture\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return 15
	}
	return 0
}

// Start the SHIPPED daemon, not a hand-configured Service or a called setter.
// The fixture floor is linker-only. Proxy export exercises the same preparation
// before mint, without opening a global listener or asserting native CA support.
func TestGuestReleaseMetadataThroughShippedDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix daemon lifecycle fixture; Windows has no published guest target")
	}
	bin := t.TempDir()
	daemon := filepath.Join(bin, "dibd")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X github.com/agenxy/dibs/internal/selfupdate.GuestSupportingMinimum=v0.0.9 -X github.com/agenxy/dibs/internal/build.Version=devel", "-o", daemon, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("daemon build setup: %v %s", err, out)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "cosign"), b, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	record := map[string]any{"tag": "v0.0.9", "checksums": []byte(guestAdmissionChecksums()), "bundle": []byte("fixture only, not a signature")}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "guest-release.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reserved.Addr().String()
	if err = reserved.Close(); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(t.TempDir(), "receipts")
	logFile, err := os.CreateTemp(t.TempDir(), "daemon-log-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.CommandContext(ctx, daemon, "--dir", dir, "--addr", addr, "--allow-parallel", "--public-url", "https://board.invalid", "--public-addr", "127.0.0.1:0")
	// The race-built verifier stand-in must not add Go's one-second exit
	// grace period to each subprocess. Race detection itself remains enabled.
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "DIBS_NOTIFY=off", "DIBS_TEST_GUEST_ADMISSION_COSIGN=1", "DIBS_TEST_GUEST_ADMISSION_RECEIPT="+receipt)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(os.Interrupt); _ = cmd.Wait() })
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	origin := "http://" + addr
	var secret []byte
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		secret, err = os.ReadFile(filepath.Join(dir, "local.secret"))
		if err == nil {
			resp, rerr := client.Get(origin + "/health")
			if rerr == nil {
				_ = resp.Body.Close()
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	type rpcResult struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		ServerInfo struct {
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	rpcCall := func(method string, params map[string]any) rpcResult {
		t.Helper()
		wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/mcp", bytes.NewReader(wire))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Dibs-Local", strings.TrimSpace(string(secret)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		resp, err := client.Do(req)
		if err != nil {
			logs, _ := os.ReadFile(logFile.Name())
			t.Fatalf("daemon door setup: %v %s", err, logs)
		}
		defer func() { _ = resp.Body.Close() }()
		var rpc struct {
			Result rpcResult `json:"result"`
			Error  any       `json:"error"`
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rpc); err != nil || resp.StatusCode != 200 || rpc.Error != nil || rpc.Result.IsError {
			t.Fatalf("RPC setup refused %s: %d %v", method, resp.StatusCode, err)
		}
		return rpc.Result
	}
	// A clean real.git checkout can resolve a Git-derived module version;
	// managed worktrees can report only devel. Neither is the selected release.
	// Compare against the independently reported LIVE MCP version, not a
	// guessed literal or a looser prefix, and never turn off VCS provenance.
	actualBuild := rpcCall("server/discover", map[string]any{}).ServerInfo.Version
	if actualBuild == "" || strings.TrimPrefix(actualBuild, "v") == "0.0.9" {
		t.Fatalf("daemon provenance setup is missing or claims the fixture release: %q", actualBuild)
	}
	t.Logf("shipped daemon reports actual build %q", actualBuild)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		rpc := rpcCall("tools/call", map[string]any{"name": name, "arguments": args})
		if len(rpc.Content) == 0 {
			t.Fatalf("tool setup returned no result: %s", name)
		}
		var out map[string]any
		if err = json.Unmarshal([]byte(rpc.Content[0].Text), &out); err != nil || out["ok"] == false {
			t.Fatalf("tool setup refused %s: %v %v", name, out, err)
		}
		return out
	}
	reg := call("register", map[string]any{"name": "issuer", "nonce": "long-retained-fixture-nonce", "kind": "persistent"})
	token, ok := reg["token"].(string)
	if !ok || token == "" {
		t.Fatal("registration setup returned no token")
	}
	call("check_in", map[string]any{"token": token})
	mint := func(n int) map[string]any {
		out := call("invite", map[string]any{"token": token, "name": fmt.Sprintf("issuer-cloud-%d", n), "export": true})
		if out["key"] == nil || out["recovery_nonce"] == nil {
			t.Fatal("mint setup omitted private recovery credentials")
		}
		config, ok := out["config"].(map[string]any)
		if !ok {
			t.Fatal("mint setup omitted configuration")
		}
		return config
	}
	for n := 1; n <= 2; n++ {
		config := mint(n)
		metadata, ok := config["bridge_release"].(map[string]any)
		if !ok || metadata["tag"] != "v0.0.9" || metadata["board_build"] != actualBuild {
			t.Fatalf("shipped mint provenance differs from live MCP: present=%t tag=%q board=%q want=%q", ok,
				metadata["tag"], metadata["board_build"], actualBuild)
		}
		assets, ok := metadata["assets"].([]any)
		if !ok || len(assets) != 3 || metadata["provisioning_status"] != "INCOMPLETE" || !strings.Contains(fmt.Sprint(config["bridge_release_status"]), "INCOMPLETE") {
			t.Fatal("artifact projection offered readiness or omitted published targets")
		}
	}
	r, err := os.ReadFile(receipt)
	if err != nil || string(r) != "verified fixture\n" {
		t.Fatalf("unchanged record was not cached: %q %v", r, err)
	}
	// A content change retaining the same filename/mtime cannot reuse authority.
	info, err := os.Stat(filepath.Join(dir, "guest-release.json"))
	if err != nil {
		t.Fatal(err)
	}
	record["checksums"] = []byte(strings.Replace(guestAdmissionChecksums(), strings.Repeat("b", 64), strings.Repeat("c", 64), 1))
	body, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "guest-release.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(filepath.Join(dir, "guest-release.json"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	config := mint(3)
	if config["bridge_release"] != nil || !strings.Contains(fmt.Sprint(config["bridge_release_status"]), "INCOMPLETE") {
		t.Fatal("changed evidence reused stale artifact metadata or blocked ordinary mint")
	}
	call("invite", map[string]any{"token": token, "action": "list"})
	call("invite", map[string]any{"token": token, "action": "revoke", "name": "issuer-cloud-3"})
}
