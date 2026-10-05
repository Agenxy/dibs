package wakeexec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestNativeQueueVerdictCoalescesThroughCommandDoor(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build %v: %s", err, b)
	}
	for _, kind := range []string{
		"", core.MsgNotify, core.MsgRequest, core.MsgHandoff, "notice", KindContinuation, KindRecheck,
		core.MsgQuestion, core.MsgStateAnswered, core.MsgStateApproved, core.MsgStateDenied,
		core.MsgStateDeclined, core.MsgStateDone, core.MsgStateWithdrawn, "adopted",
	} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			t.Setenv("DIBS_DIR", t.TempDir())
			argv := []string{binary, "queue", "--thread", "verdict-thread", "--message", Compose(kind)}
			started := time.Now().UTC()
			if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
				t.Fatal("setup: first verdict wake failed")
			}
			finished := time.Now().UTC()
			readQueue := func() []struct {
				Input []struct{ Text string }
			} {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(home, "pending.json"))
				if err != nil {
					t.Fatal(err)
				}
				var rows []struct {
					Input []struct{ Text string }
				}
				if err = json.Unmarshal(b, &rows); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			rows := readQueue()
			if len(rows) != 1 || len(rows[0].Input) != 1 {
				t.Fatalf("setup: fixture didn't retain the actual command notice: %#v", rows)
			}
			assertIssuedQueueNotice(t, rows[0].Input[0].Text, kind, started, finished)
			// A current prompt and an old receipt must not override the actual
			// pending verdict. This is also the unloaded/away queue: no fixture
			// consumer runs until the queue is explicitly cleared below.
			NoteQueuePrompt("verdict-thread", time.Now())
			if err := writeReceipt("verdict-thread", queueReceipt{QueuedAt: time.Now().Add(-3 * time.Hour)}); err != nil {
				t.Fatal(err)
			}
			argv[5] = Compose(core.MsgHandoff)
			if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
				t.Fatal("second command door failed")
			}
			if rows = readQueue(); len(rows) != 1 {
				t.Fatalf("pending %s verdict was duplicated by a handoff: %d entries, want 1", kind, len(rows))
			}
			if err := os.WriteFile(filepath.Join(home, "pending.json"), []byte("[]"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) || len(readQueue()) != 1 {
				t.Fatal("actual consumption did not re-arm the next handoff")
			}
		})
	}
}
