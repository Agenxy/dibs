package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/engine"
)

// A real operator command, running only the test executable. No installed
// harness, user notification, shell or eligibility setter participates.
func TestWakePhaseCommandHelper(t *testing.T) {
	path := os.Getenv("DIBS_TEST_PHASE_COMMAND_FILE")
	if path == "" {
		return
	}
	body, err := json.Marshal(os.Args)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// The reader treats publication as completion. Creating the final file
	// before Write let it observe an empty receipt between those syscalls.
	// Keep earlier command rows, then publish the complete next version by a
	// same-directory rename: the reader sees the old file or the whole new one.
	f, err := os.CreateTemp(filepath.Dir(path), ".command-receipt-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(previous, append(body, '\n')...)); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		t.Fatal(err)
	}
}

func TestWakePhaseCommandThroughMCP(t *testing.T) {
	const session = "e2c03160-6000-4000-8000-000000000002"
	for _, phase := range []string{"all", "urgent", "none"} {
		for _, kind := range []string{"notify", "question", "ordinary-approval"} {
			t.Run(phase+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("HOME", dir)
				t.Setenv("XDG_RUNTIME_DIR", dir)
				path := filepath.Join(dir, "command-receipt")
				t.Setenv("DIBS_TEST_PHASE_COMMAND_FILE", path)
				self, err := os.Executable()
				if err != nil {
					t.Fatal("setup: test command:", err)
				}
				srv, eng, _ := restartableQueueServer(t, dir)
				eng.ApplySetting("wake.extend_turn_for", phase)
				eng.SetWakeCommands(map[string]engine.WakeCommand{
					"policy-fixture": {Argv: []string{self, "-test.run=^TestWakePhaseCommandHelper$", "--", "{message}"}, Cooldown: time.Second},
				})
				call := func(name string, args map[string]any) map[string]any {
					t.Helper()
					return sendNoteCall(t, srv, name, args)
				}
				worker := call("register", map[string]any{
					"name": "worker", "session_id": session, "harness": "policy-fixture",
				})
				sender := call("register", map[string]any{"name": "sender"})
				call("check_in", map[string]any{"token": sender["token"]})
				call("check_in", map[string]any{"token": worker["token"]})
				if kind == "ordinary-approval" {
					n := call("send", map[string]any{"token": worker["token"], "to": "sender", "type": "request", "body": "work"})["msg_serial"]
					call("respond", map[string]any{"token": sender["token"], "msg_serial": n, "disposition": "approve"})
				} else {
					call("send", map[string]any{"token": sender["token"], "to": "worker", "type": kind, "body": "private body must stay off argv"})
				}
				// Recent authenticated contact must defer rather than discard an
				// enabled notify. Its actual command runs after the configured
				// one-second window, entering the real retry path too.
				wakes := phase != "none" && kind != "ordinary-approval" && (phase == "all" || kind == "question")
				until := time.Now().Add(1600 * time.Millisecond)
				if wakes {
					until = time.Now().Add(5 * time.Second)
				}
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					body, readErr := os.ReadFile(path)
					if readErr == nil {
						if !wakes {
							t.Fatalf("suppressed %s/%s ran the actual operator command: %s", phase, kind, body)
						}
						if !strings.Contains(string(body), "Dibs:") || !strings.Contains(string(body), kind) ||
							strings.Contains(string(body), "private body") {
							t.Fatalf("command lost its event or exposed message text: %s", body)
						}
						break
					}
					if !os.IsNotExist(readErr) {
						t.Fatal("read command receipt:", readErr)
					}
					if time.Now().After(until) {
						if wakes {
							t.Fatal("enabled authored mail was lost on the real command/retry route")
						}
						break
					}
					<-tick.C
				}
				if phase == "all" && kind == "notify" {
					presented := call("check_in", map[string]any{"token": worker["token"]})
					if !mentions(presented, "private body must stay off argv") {
						t.Fatal("setup: command-woken FYI was not presented")
					}
					for range 2 {
						call("hook_poll", map[string]any{"session_id": session, "event": "PreToolUse"})
						got := call("hook_poll", map[string]any{
							"session_id": session, "event": "Stop", "strict_output": true,
						})
						if got["decision"] == "block" || got["reason"] != nil || got["hookSpecificOutput"] != nil {
							t.Fatalf("presented command FYI blocked a later Stop: %v", got)
						}
					}
					until = time.Now().Add(1600 * time.Millisecond)
					for {
						body, er := os.ReadFile(path)
						if er != nil || len(strings.Split(strings.TrimSpace(string(body)), "\n")) != 1 {
							t.Fatalf("presented FYI ran additional commands: %q %v", body, er)
						}
						if time.Now().After(until) {
							break
						}
						<-tick.C
					}
					// A genuinely new question still wakes after contact deferral;
					// oldestBlocking must not label it with the already-read FYI.
					call("check_in", map[string]any{"token": worker["token"]})
					call("send", map[string]any{"token": sender["token"], "to": "worker", "type": "question", "body": "new work"})
					until = time.Now().Add(5 * time.Second)
					for {
						body, er := os.ReadFile(path)
						if er != nil {
							t.Fatal(er)
						}
						rows := strings.Split(strings.TrimSpace(string(body)), "\n")
						if len(rows) >= 2 {
							if len(rows) != 2 || !strings.Contains(rows[1], "question") || strings.Contains(rows[1], "notify") {
								t.Fatalf("retry used an already-presented FYI instead of the new question: %q", body)
							}
							break
						}
						if time.Now().After(until) {
							t.Fatal("a new blocking question was lost after FYI presentation")
						}
						<-tick.C
					}
				}
			})
		}
	}
}
