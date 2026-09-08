package engine

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A hook that reaches nobody must say so in the daemon's log.
//
// This is the failure mode the wake path has by construction: hook_poll
// answers, the session it answered for is not the agent asking, and nothing
// anywhere records it. A night went into diagnosing exactly that on this
// board, from three agents, producing three different and largely wrong
// accounts, while the daemon knew the answer on every call and wrote none of
// them down. The observable was only ever "my mail never arrives".
//
// Asserted on the ARRIVING ID being present, because that is the fact that
// discriminates: a Claude Code session carries a session UUID, a host session
// id, and the host-<ppid> the bridge derives; register binds one and the hook
// sends another, and the log is where those two become comparable.
func TestAHookThatResolvesToNobodyIsLogged(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// An agent in this directory, so the case is "agents exist and this session
	// is none of them" rather than "nobody works here".
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "resident", Nonce: "n-resident",
		AgentKind: core.KindPersistent,
		Agent:     &core.AgentInfo{CWD: "/work/here"},
	}); err != nil {
		t.Fatal("setup:", err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	const stranger = "local_439aba14-b1a5-4014-921a-3b8eccbef924"
	if _, err := e.HookPoll(ctx, stranger, "Stop", "/work/here", false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "NOBODY") {
		t.Errorf("a hook arrived for a directory that has agents, matched none of "+
			"them, and the daemon logged nothing at INFO. That is the wake path "+
			"failing silently, which is the whole defect\n  log: %q", got)
	}
	if !strings.Contains(got, stranger) {
		t.Errorf("the log does not carry the arriving session id, which is the one "+
			"fact that shows WHY it did not resolve\n  log: %q", got)
	}
}

// And the ordinary case stays quiet, or the signal drowns.
//
// Most sessions on a machine are in directories nobody coordinates from.
// Logging those at INFO would put the real fault in a stream nobody reads,
// which is the habituation failure this release has already fixed twice.
func TestAHookInAnUncoordinatedDirectoryStaysQuiet(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	if _, err := e.HookPoll(ctx, "some-stranger", "Stop", "/nobody/works/here", false, false); err != nil {
		t.Fatalf("hook_poll: %v", err)
	}
	if got := buf.String(); strings.Contains(got, "NOBODY") {
		t.Errorf("a session in a directory with no agents was logged as a fault at "+
			"INFO. Every idle session on the machine would produce one\n  log: %q", got)
	}
}
