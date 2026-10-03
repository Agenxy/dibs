package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestOnlyCodexQueueCountsAsAQueuingCommand(t *testing.T) {
	for argv, want := range map[string]bool{
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex queue --thread x": true,
		"codex queue --thread x --message m":                                                true,
		"codex exec resume x":                                                               false,
		"/usr/local/bin/notify-me x":                                                        false,
	} {
		if got := isQueuingArgv(splitArgs(argv)); got != want {
			t.Errorf("%q: queuing = %v, want %v", argv, got, want)
		}
	}
}

func splitArgs(s string) []string {
	var out []string
	cur := ""
	for _, r := range s + " " {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	return out
}

// A queued wake that has not been picked up is not followed by another. The
// ChatGPT app keeps every `codex queue` message and releases one per turn
// end, so wakes queued while a thread was not loaded drained later as a run of
// empty turns: codex-k7-0 got "a new request is waiting." three times in
// thirty seconds with nothing in its inbox (k7-dev, Dibs #5052). Only a new
// prompt/start hook is a fallback receipt; tool and MCP traffic are not.
func TestASecondWakeIsNotQueuedBehindOneNotYetDelivered(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	touch, err := filepath.Abs("/usr/bin/touch")
	if _, serr := os.Stat(touch); err != nil || serr != nil {
		t.Skip("no /usr/bin/touch on this platform")
	}
	bin := t.TempDir()
	codex := filepath.Join(bin, "codex") // a queuing command by name, touch by nature
	if err := os.Symlink(touch, codex); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "queue") // touch makes a file named for its first argument

	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{"codex": {
		Argv: []string{codex, "queue", "{thread}"}, Cooldown: time.Millisecond,
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWakeTimersOnCleanup(t, e)
	go e.Run(ctx)
	worker, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "worker", Nonce: "n-worker-0123456789abcdef",
		AgentKind: core.KindPersistent, SessionID: contThread,
		Agent: &core.AgentInfo{Harness: "Codex", CWD: cwd},
	})
	if err != nil {
		t.Fatal(err)
	}
	asker, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "asker", Nonce: "n-asker-0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	atok := asker["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: atok}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	send := func(id string) {
		t.Helper()
		<-time.After(20 * time.Millisecond) // past the 1ms cooldown and the recency window
		if _, err := e.Do(ctx, &core.Op{
			Kind: core.OpSendMessage, Token: atok, To: "worker",
			MsgType: core.MsgQuestion, Body: "are you there? " + id, OpID: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	woke := func() bool {
		t.Helper()
		for range 100 {
			if _, err := os.Stat(marker); err == nil {
				_ = os.Remove(marker)
				return true
			}
			<-time.After(10 * time.Millisecond)
		}
		return false
	}

	send("q1")
	if !woke() {
		t.Fatal("setup: the first question woke nobody, so nothing below tests a second")
	}
	send("q2")
	if woke() {
		t.Error("a second wake was queued behind one the agent has not picked up: each " +
			"later becomes an empty turn")
	}
	// An ordinary MCP call during a running turn is not a queue receipt.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: worker["token"].(string)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	send("q3")
	if woke() {
		t.Error("an MCP call re-armed an undelivered queued wake")
	}
	if _, err := e.HookPoll(ctx, contThread, "UserPromptSubmit", cwd, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	send("q4")
	if !woke() {
		t.Error("after the agent picked up its wake, new mail woke nobody")
	}
}
