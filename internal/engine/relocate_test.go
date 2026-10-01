package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

const relocThread = "0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"

// relocationBoard is a running engine with a closed Codex agent to move, a
// coordinator, a plain member, and a stand-in for the command runner: a test
// that reached the real one would open the operator's ChatGPT app or start a
// real agent.
type relocationBoard struct {
	e       *Engine
	led     *memLedger
	ctx     context.Context
	lead    string
	member  string
	started chan []string
}

func newRelocationBoard(t *testing.T) *relocationBoard {
	t.Helper()
	led := &memLedger{}
	e := New(core.NewState("test", core.DefaultLimits()), led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopWakeTimersOnCleanup(t, e)
	go e.Run(ctx)
	b := &relocationBoard{e: e, led: led, ctx: ctx, started: make(chan []string, 4)}
	prev := relocationRunner
	relocationRunner = func(argv []string, _, _ string) bool { b.started <- argv; return true }
	t.Cleanup(func() { relocationRunner = prev })

	reg := func(name string, op *core.Op) string {
		t.Helper()
		op.Kind, op.Name = core.OpRegister, name
		res, err := e.Do(ctx, op)
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if tok == "" {
			t.Fatalf("setup: no token for %s", name)
		}
		return tok
	}
	worker := reg("worker", &core.Op{
		Nonce: "n-worker-0123456789abcdef0123456789abcdef", AgentKind: core.KindPersistent,
		SessionID: relocThread, Agent: &core.AgentInfo{Harness: "Codex", Surface: harnessenv.CodexOutsideApp},
	})
	_ = worker
	// Its process is gone: the sweep is how a harness that closed becomes a
	// dormant row on a real board.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"worker"}}); err != nil {
		t.Fatalf("setup: sweep: %v", err)
	}
	if st, _ := e.query(ctx, func() core.Result { return core.Result{"s": e.state.Agents["worker"].Status} }); st["s"] == core.StatusActive {
		t.Fatal("setup: the worker is still active, so nothing below tests a closed agent")
	}
	b.lead = reg("lead", &core.Op{})
	if _, err := e.GrantRoleByHuman(ctx, "lead", core.RoleCoordinator); err != nil {
		t.Fatalf("setup: grant: %v", err)
	}
	b.member = reg("member", &core.Op{})
	reg("busy", &core.Op{
		SessionID: "0199a0b1-0000-4e5f-8a9b-0c1d2e3f4a5b", Agent: &core.AgentInfo{Harness: "Codex"},
	})
	return b
}

// codeOf is the error's code, for asserting WHICH refusal fired: a test that
// only checks "refused" passes when an unrelated check refuses first.
func codeOf(err error) string {
	var ce *core.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func (b *relocationBoard) ran(t *testing.T) []string {
	t.Helper()
	select {
	case argv := <-b.started:
		return argv
	case <-time.After(5 * time.Second):
		t.Fatal("the relocation command never started")
		return nil
	}
}

func (b *relocationBoard) ranNothing(t *testing.T) {
	t.Helper()
	select {
	case argv := <-b.started:
		t.Fatalf("a refused relocation ran %q", argv)
	case <-time.After(100 * time.Millisecond):
	}
}

// The deliberate move, end to end: a coordinator moves a closed terminal
// Codex thread into the ChatGPT app, and the command that runs is the app's
// own route for that thread, after the decision is in the ledger.
func TestACoordinatorMovesAClosedAgentIntoTheApp(t *testing.T) {
	b := newRelocationBoard(t)
	ledgered := len(b.led.ops)
	res, err := b.e.Relocate(b.ctx, b.lead, "worker", "ChatGPT-App")
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if argv := b.ran(t); len(argv) != 2 || argv[1] != "codex://threads/"+relocThread {
		t.Fatalf("ran %q: the thread was not opened in the app", argv)
	}
	if res["by"] != "lead" || res["from"] != harnessenv.CodexOutsideApp || res["to"] != harnessenv.ChatGPTApp {
		t.Errorf("result %v: who moved it, from where and to where is the record", res)
	}
	if len(b.led.ops) != ledgered+1 || b.led.ops[ledgered].Kind != core.OpRelocate {
		t.Errorf("the relocation was not ledgered as one op: an agent turned up in an "+
			"environment it did not start in and nothing says who did it (%d ops)", len(b.led.ops)-ledgered)
	}
}

// The operator's own environments: their command, with the agent's thread.
// These are the hosting commands a wake refuses, and this is the one place
// that runs them.
func TestAnOperatorEnvironmentRunsItsCommandForTheThread(t *testing.T) {
	b := newRelocationBoard(t)
	b.e.SetRelocations(map[string]RelocateCommand{
		"Headless": {Argv: []string{"codex", "exec", "resume", "{thread}", "{message}"}},
	})
	if _, err := b.e.RelocateByHuman(b.ctx, "worker", "headless"); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if argv := b.ran(t); strings.Join(argv[:4], " ") != "codex exec resume "+relocThread {
		t.Fatalf("ran %q", argv)
	}
}

// And everything that must not move an agent does not, with nothing run and
// nothing ledgered.
func TestARelocationThatCannotHappenRunsAndRecordsNothing(t *testing.T) {
	b := newRelocationBoard(t)
	human, _, err := b.e.HumanAgent(b.ctx)
	if err != nil || human == "" {
		t.Fatalf("setup: no human row: %v", err)
	}
	for name, try := range map[string]func() (string, error){
		"a member without the permission": func() (string, error) {
			_, err := b.e.Relocate(b.ctx, b.member, "worker", "chatgpt-app")
			return "E_NOT_PERMITTED", err
		},
		"an environment nobody configured": func() (string, error) {
			_, err := b.e.Relocate(b.ctx, b.lead, "worker", "nowhere")
			return "E_NO_ENVIRONMENT", err
		},
		"an agent that is running": func() (string, error) {
			_, err := b.e.Relocate(b.ctx, b.lead, "busy", "chatgpt-app")
			return "E_AGENT_RUNNING", err
		},
		"the person's own row": func() (string, error) {
			_, err := b.e.Relocate(b.ctx, b.lead, human, "chatgpt-app")
			return "E_IS_HUMAN", err
		},
		"where it already runs": func() (string, error) {
			b.e.SetRelocations(map[string]RelocateCommand{"codex": {Argv: []string{"x"}}})
			_, err := b.e.Relocate(b.ctx, b.lead, "worker", "codex")
			return "E_SAME_ENVIRONMENT", err
		},
	} {
		ledgered := len(b.led.ops)
		want, err := try()
		if got := codeOf(err); got != want {
			t.Errorf("%s: refused with %q (%v), want %s", name, got, err, want)
		}
		b.ranNothing(t)
		if len(b.led.ops) != ledgered {
			t.Errorf("%s: a refused relocation was ledgered", name)
		}
	}
}

// A wake never runs a relocation command. With an environment configured that
// would run the thread headless, mail for the closed agent still goes only
// where a wake may go: here, nowhere, because the board has no wake command
// for it. The two tables never meet.
func TestAWakeNeverRunsARelocationCommand(t *testing.T) {
	e := &Engine{state: core.NewState("test", core.DefaultLimits())}
	e.SetRelocations(map[string]RelocateCommand{
		"codex": {Argv: []string{"codex", "exec", "resume", "{thread}"}},
	})
	l := bridgeAgent("worker", "Codex", relocThread)
	plan, ok := e.wakeFor(l, core.MsgQuestion, questionFor("worker"))
	if ok && len(plan.argv) > 0 {
		t.Fatalf("a wake planned %q: a relocation command ran as a wake", plan.argv)
	}
}
