package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A wake that fails is told to the agent that was waiting on it.
//
// K7-DEV SENT TWO REQUESTS AND WAS TOLD NOTHING. Its two Codex workers were
// woken four times each and every wake failed: Codex refused to run in a
// directory that was not a git repository. The daemon knew exactly what had
// happened and wrote it down, at WARN, in dibd.log, which no agent can read.
// So the sender's await_events showed no wake and no failure, both requests sat
// pending, doctor went on claiming "a wake either runs or reports why", and
// after the third failure the board quietly stopped trying. The only way anyone
// found out was a person reading the daemon's log.
//
// the maintainer's rule for this: an agent that is not archived is live and is woken when
// needed, and when the board cannot wake it, the agent waiting on it must be
// told, not left to infer silence. The channel is the one that already exists
// for "something happened that you could not have inferred": a notice, which
// reaches the sender through its own digest and wakes it if the operator asked
// for that.
//
// Driven through the real path: a real request, the real waker, a real command
// that exits nonzero. A test that pushed the notice by hand would pass with the
// wiring missing, which is precisely how #245 shipped a feature called from
// nowhere.
func TestAFailedWakeIsToldToTheAgentWaitingOnIt(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	worker := bridgeAgent("worker", "Codex", "019ffe52-0eaf-7f60-81cc-6ab1298d76ec")
	worker.Token = "worker-tok"
	st.Agents = map[string]*core.Agent{"worker": worker}

	e := New(st, &memLedger{}, deadProber{})
	e.SetWakeCommands(map[string]WakeCommand{
		// Exits nonzero and says nothing about an open thread, like Codex
		// refusing an untrusted directory did.
		"codex": {Argv: []string{"/usr/bin/false"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "orchestrator"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	orch, _ := res["token"].(string)
	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: orch, To: "worker",
		MsgType: core.MsgRequest, Body: "take the next task",
	})
	if err != nil {
		t.Fatal("setup: send:", err)
	}
	serial, _ := sent["msg_serial"].(uint64)
	awaitWakeDone(t, e, "worker")

	_, _ = e.query(ctx, func() core.Result {
		var told string
		for _, n := range e.notices["orchestrator"] {
			if strings.Contains(n.Text, "worker") && n.Msg == serial {
				told = n.Text
			}
		}
		if told == "" {
			t.Errorf("the wake for the orchestrator's request failed and the orchestrator " +
				"was told nothing. This is the report: a failure the daemon knew about, " +
				"written only to its own log, while the sender waited on a pending request")
			return core.Result{}
		}
		for _, want := range []string{"wake", "failed"} {
			if !strings.Contains(strings.ToLower(told), want) {
				t.Errorf("the notice does not say the wake %s: %q", want, told)
			}
		}
		// What it must NOT carry: the recipient's wake command. It names the
		// recipient's thread, and the fix belongs to whoever runs the board.
		if strings.Contains(told, "019ffe52") || strings.Contains(told, "/usr/bin") {
			t.Errorf("the sender's notice carries the recipient's wake command: %q", told)
		}
		// And the row says so, which is where the person running the board
		// looks: the workers read `active` while every wake for them failed.
		agents, _ := e.decoratedBoard()["agents"].([]map[string]any)
		for _, row := range agents {
			if row["id"] != "worker" {
				continue
			}
			if w, _ := row["wake"].(string); !strings.Contains(w, "fail") {
				t.Errorf("the worker's row says nothing about its failing wake (wake=%q)", w)
			}
		}
		return core.Result{}
	})
}

// Told once when the wake fails, and once more if the board gives up; never on
// every retry. A sender woken by the same failure on each attempt would learn to
// ignore the notice, which is how a real warning gets trained away.
//
// The decision is tested directly here because the wiring is already driven
// end to end above; what this pins is the counting, which depends on the
// failure history and not on a process.
func TestAWakeFailureIsToldOnceAndThenOnceMoreWhenTheBoardGivesUp(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	r, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "sender"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"}); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: r["token"].(string), To: "worker",
		MsgType: core.MsgQuestion, Body: "ready?",
	}); err != nil {
		t.Fatal("setup:", err)
	}
	count := func() (n int, last string) {
		for _, x := range e.notices["sender"] {
			if strings.Contains(x.Text, "worker") {
				n, last = n+1, x.Text
			}
		}
		return n, last
	}
	_, _ = e.query(ctx, func() core.Result {
		e.wakers.mu.Lock()
		e.wakers.fails = map[string]int{"worker": 1}
		e.wakers.mu.Unlock()
		e.reportWakeFailureDecision("worker", time.Now())
		e.reportWakeFailureDecision("worker", time.Now()) // the retry fails too
		if n, _ := count(); n != 1 {
			t.Errorf("the sender was told %d times about one failing wake; want once", n)
		}
		e.wakers.mu.Lock()
		e.wakers.fails["worker"] = spawnFailureCap
		e.wakers.mu.Unlock()
		e.reportWakeFailureDecision("worker", time.Now())
		e.reportWakeFailureDecision("worker", time.Now())
		n, last := count()
		if n != 2 || !strings.Contains(last, "stopped") {
			t.Errorf("after the board gave up the sender has %d notice(s), last %q; want "+
				"exactly one more, saying the board stopped trying", n, last)
		}
		return core.Result{}
	})
}
