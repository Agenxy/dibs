package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/testport"
)

// Enter run(), including its -dir wiring, not an advisory setter installed by
// the test. The same encrypted board is restarted, and the assertion reads its
// committed ordinary mail. Two replayed trees AND the separate pre-warm path
// must produce one message, even though their indexes finish independently.
// This file also compiles against the production source before the fix.
func TestScorerAdvisoryThroughDaemonBoot(t *testing.T) {
	if os.Getenv("DIBS_ADVISORY_TEST_CHILD") == "1" {
		os.Args = strings.Split(os.Getenv("DIBS_ADVISORY_TEST_ARGS"), "\n")
		flag.CommandLine = flag.NewFlagSet("dibd", flag.ContinueOnError)
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	repos := []string{advisoryBootRepo(t), advisoryBootRepo(t), advisoryBootRepo(t)}
	dir := t.TempDir()
	seedAdvisoryBoard(t, dir, repos[:2])
	var previous int
	for _, step := range []struct {
		name    string
		notify  string
		join    string
		history string
		added   int
	}{
		{"first boot batches all trees", "0.25", "0", "1", 1},
		{"unchanged restart is silent", "0.25", "0", "1", 0},
		{"notify threshold change resends once", "0.35", "0", "1", 1},
		{"unchanged notify restart is silent", "0.35", "0", "1", 0},
		{"join threshold change resends once", "0.35", "0.5", "1", 1},
		{"unchanged join restart is silent", "0.35", "0.5", "1", 0},
		{"scorer history config change resends once", "0.35", "0.5", "2", 1},
		{"unchanged scorer restart is silent", "0.35", "0.5", "2", 0},
	} {
		t.Run(step.name, func(t *testing.T) {
			advisoryDaemonBoot(t, dir, repos, step.notify, step.join, step.history)
			mail := committedScorerAdvice(t, dir)
			if added := len(mail) - previous; added != step.added {
				t.Errorf("actual boot added %d scorer advisories, want %d (before %d, after %d)",
					added, step.added, previous, len(mail))
			}
			for _, m := range mail[previous:] {
				for _, repo := range repos {
					if !strings.Contains(m.Body, repo) {
						t.Errorf("advisory %d did not batch repository %s", m.Serial, repo)
					}
				}
			}
			previous = len(mail)
		})
	}
	later := []string{advisoryBootRepo(t), advisoryBootRepo(t)}
	t.Run("later discoveries batch only newly relevant trees", func(t *testing.T) {
		logs := advisoryDaemonBoot(t, dir, repos, "0.35", "0.5", "2", func(addr string) {
			registerLaterAdviceTrees(t, dir, addr, later)
		})
		// Assert setup reached real publication for BOTH later repositories.
		for _, repo := range later {
			found := false
			for _, line := range strings.Split(logs, "\n") {
				found = found || (strings.Contains(line, "work-overlap matching ready") && strings.Contains(line, repo))
			}
			if !found {
				t.Fatalf("setup: later repository was never indexed: %s\n%s", repo, logs)
			}
		}
		mail := committedScorerAdvice(t, dir)
		if added := len(mail) - previous; added != 1 {
			t.Errorf("later registrations added %d advisories, want one combined new-repo message", added)
		}
		for _, m := range mail[previous:] {
			for _, repo := range later {
				if !strings.Contains(m.Body, repo) {
					t.Errorf("later advisory %d did not include new repository %s", m.Serial, repo)
				}
			}
			for _, repo := range repos {
				if strings.Contains(m.Body, repo) {
					t.Errorf("later advisory %d repeated already-covered repository %s", m.Serial, repo)
				}
			}
		}
		previous = len(mail)
	})
	t.Run("restart after later discovery is silent", func(t *testing.T) {
		all := append(append([]string{}, repos...), later...)
		advisoryDaemonBoot(t, dir, all, "0.35", "0.5", "2")
		if mail := committedScorerAdvice(t, dir); len(mail) != previous {
			t.Errorf("unchanged restart repeated later advice: before %d, after %d", previous, len(mail))
		}
	})
}

func advisoryBootRepo(t *testing.T) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("setup: real repository path:", err)
	}
	for n := range sidecarWorthIt {
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("file_%04d.go", n)), []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal("setup: tracked file:", err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "--no-gpg-sign", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup: git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

func seedAdvisoryBoard(t *testing.T, dir string, repos []string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("advisory-fixture"), 0o600); err != nil {
		t.Fatal("setup: node id:", err)
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup: key:", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "advisory-fixture", box)
	if err != nil {
		t.Fatal("setup: ledger:", err)
	}
	eng := engine.New(core.NewState("advisory-fixture", core.DefaultLimits()), led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
		if err := led.Close(); err != nil {
			t.Error("setup: close ledger:", err)
		}
	}()
	reg, err := eng.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "advice-reader", Nonce: "reader-nonce",
		AgentKind: core.KindPersistent, NoProcess: true,
	})
	if err != nil || reg["agent_id"] != "advice-reader" {
		t.Fatalf("setup: reader registration: %v %v", reg, err)
	}
	if _, err := eng.GrantRole(ctx, "advice-reader", core.RoleCoordinator); err != nil {
		t.Fatal("setup: coordinator grant:", err)
	}
	for n, repo := range repos {
		if _, err := eng.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: fmt.Sprintf("worker-%d", n),
			Nonce: fmt.Sprintf("worker-nonce-%d", n), AgentKind: core.KindPersistent, NoProcess: true,
			Agent: &core.AgentInfo{CWD: repo},
		}); err != nil {
			t.Fatal("setup: worker registration:", err)
		}
	}
	if got := eng.WorkingDirectories(ctx); len(got) != len(repos) {
		t.Fatalf("setup: replayed local trees = %v, want %v", got, repos)
	}
}

func advisoryDaemonBoot(t *testing.T, dir string, repos []string, notify, join, history string,
	afterStartup ...func(string),
) string {
	t.Helper()
	port := testport.Reserve(t, "tcp", "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestScorerAdvisoryThroughDaemonBoot$")
	args := []string{
		"dibd", "--dir", dir, "--addr", port.Addr, "--allow-parallel",
		"--match-repo", repos[2], "--match-notify", notify, "--match-join", join, "--match-history", history,
	}
	cmd.Env = append(nativeProbeEnv(t.TempDir(), "", ""), "DIBS_NOTIFY=off", "GORACE=atexit_sleep_ms=0",
		"DIBS_ADVISORY_TEST_CHILD=1", "DIBS_ADVISORY_TEST_ARGS="+strings.Join(args, "\n"))
	logFile, err := os.CreateTemp(t.TempDir(), "boot-log-")
	if err != nil {
		t.Fatal("setup: log:", err)
	}
	defer func() { _ = logFile.Close() }()
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("setup: child log pipe:", err)
	}
	cmd.Stderr = cmd.Stdout
	port.Release(t)
	if err := cmd.Start(); err != nil {
		t.Fatal("setup: real daemon start:", err)
	}
	ready := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		seen := map[string]bool{}
		up, signalled := false, false
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(logFile, line)
			up = up || strings.Contains(line, "dibd up")
			if strings.Contains(line, "work-overlap matching ready") {
				for _, repo := range repos {
					if strings.Contains(line, repo) {
						seen[repo] = true
					}
				}
			}
			if up && len(seen) == len(repos) && !signalled {
				close(ready)
				signalled = true
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case err := <-exited:
			if err != nil {
				t.Error("real daemon exit:", err)
			}
		case <-ctx.Done():
			cancel()
			<-exited
			t.Error("real daemon did not stop")
		}
		<-drained
	}
	defer stop()
	defer func() {
		if t.Failed() {
			stop()
			raw, _ := os.ReadFile(logFile.Name())
			t.Log(string(raw))
		}
	}()
	select {
	case <-ready:
	case err := <-exited:
		stopped = true
		<-drained
		raw, _ := os.ReadFile(logFile.Name())
		t.Fatalf("setup: daemon exited before all indexes were installed: %v\n%s", err, raw)
	case <-ctx.Done():
		t.Fatal("setup: actual daemon never installed all startup indexes")
	}
	// Three sweeps after actual index publication: both a pending batch and an
	// unwanted retry have time to enter the ledger. No advisory-specific setter
	// or test completion flag is used to make this observation succeed.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal("setup: daemon observation timed out")
	}
	for _, after := range afterStartup {
		after(port.Addr)
		timer.Reset(3 * time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal("setup: later discovery observation timed out")
		}
	}
	stop()
	raw, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal("setup: read real daemon log:", err)
	}
	return string(raw)
}

func registerLaterAdviceTrees(t *testing.T, dir, addr string, repos []string) {
	t.Helper()
	secret, err := os.ReadFile(filepath.Join(dir, "local.secret"))
	if err != nil {
		t.Fatal("setup: local credential:", err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for n, repo := range repos {
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": n + 1, "method": "tools/call",
			"params": map[string]any{"name": "register", "arguments": map[string]any{
				"name": fmt.Sprintf("later-worker-%d", n), "nonce": fmt.Sprintf("later-nonce-%d", n),
				"kind": "persistent", "cwd": repo, "pid": os.Getpid(),
			}},
		})
		if err != nil {
			t.Fatal("setup: registration envelope:", err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal("setup: registration request:", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("X-Dibs-Local", strings.TrimSpace(string(secret)))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("setup: actual later registration:", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		var rpc struct {
			Error  any `json:"error"`
			Result struct {
				IsError bool                    `json:"isError"`
				Content []struct{ Text string } `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &rpc); err != nil || readErr != nil || resp.StatusCode != 200 ||
			rpc.Error != nil || rpc.Result.IsError || len(rpc.Result.Content) == 0 {
			t.Fatalf("setup: actual later registration refused: %d %s (%v, %v)", resp.StatusCode, raw, err, readErr)
		}
		var registered map[string]any
		if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &registered); err != nil ||
			registered["agent_id"] != fmt.Sprintf("later-worker-%d", n) || registered["token"] == nil {
			t.Fatalf("setup: later identity was not registered: %v %v", registered, err)
		}
	}
}

func committedScorerAdvice(t *testing.T, dir string) []*core.Message {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("read committed key:", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "advisory-fixture", box)
	if err != nil {
		t.Fatal("read committed ledger:", err)
	}
	defer func() { _ = led.Close() }()
	st := core.NewState("advisory-fixture", core.DefaultLimits())
	if _, err := led.Replay(st); err != nil {
		t.Fatal("replay actual boot mail:", err)
	}
	var messages []*core.Message
	for _, m := range st.Messages {
		if m.From == "dibs" && strings.Contains(m.Body, "-match-embed-url") {
			if m.To != "advice-reader" || m.Type != core.MsgNotify {
				t.Fatalf("advice escaped ordinary coordinator notify: %+v", m)
			}
			messages = append(messages, m)
		}
	}
	// The returned order must be the ledger's, never map iteration order.
	slices.SortFunc(messages, func(a, b *core.Message) int { return cmp.Compare(a.Serial, b.Serial) })
	return messages
}
