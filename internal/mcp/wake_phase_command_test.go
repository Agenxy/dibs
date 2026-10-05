package mcp

import (
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
	if err := os.WriteFile(path, []byte(strings.Join(os.Args, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWakePhaseCommandThroughMCP(t *testing.T) {
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
					"name": "worker", "session_id": "e2c03160-6000-4000-8000-000000000002", "harness": "policy-fixture",
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
			})
		}
	}
}
