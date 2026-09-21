package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A Windows hub does not rewrite a unix member's filenames.
//
// The daemon's own OS says what a separator means HERE, and nothing
// about the machine an op came from. Every op was folded by the hub's
// rule, so on a Windows hub a unix agent's `/work/a\b`, which is one
// file with a backslash in its name, became `/work/a/b`, which is
// another: two distinct resources merged before anything was ledgered,
// and a claim that protects neither of them. A remote bridge already
// sends its own machine's paths portably, so there is nothing here to
// fold for it. Round forty-six of the pre-release review.
func TestAWindowsHubDoesNotFoldARemoteAgentsFilename(t *testing.T) {
	st := core.NewState("hub-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetHostID("hub-node")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// This daemon is the Windows one, which is the only host where a
	// backslash is a separator at all.
	old := foldsSeparators
	foldsSeparators = true
	t.Cleanup(func() { foldsSeparators = old })

	reg := func(name, host, cwd string) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name,
			Agent: &core.AgentInfo{CWD: cwd, HostID: host},
		})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}

	// A unix member claims a file whose NAME contains a backslash.
	const odd = `/work/a\b`
	far := reg("far", "machine-b", "/work")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpClaim, Token: far, Path: odd, Mode: core.ClaimExclusive}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	var got string
	onLoop(t, ctx, e, func(s *core.State) {
		for _, c := range s.Claims {
			got = c.Path
		}
	})
	if got != odd {
		t.Fatalf("the hub recorded the remote claim as %q, want %q: it applied its own "+
			"platform's rule to another machine's filename, and %q is a different file",
			got, odd, got)
	}

	// And a caller on the hub's own machine is still folded, which is
	// what the rule is for.
	near := reg("near", "hub-node", `/work`)
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpClaim, Token: near, Path: `/work/c\d`, Mode: core.ClaimExclusive,
	}); err != nil {
		t.Fatalf("local claim: %v", err)
	}
	var local []string
	onLoop(t, ctx, e, func(s *core.State) {
		for _, c := range s.Claims {
			local = append(local, c.Path)
		}
	})
	found := false
	for _, p := range local {
		if p == "/work/c/d" {
			found = true
		}
	}
	if !found {
		t.Fatalf("this machine's own caller was not folded: %v, want /work/c/d among them: "+
			"a Windows agent spells its paths with backslashes and the fold knows one separator", local)
	}
}

// The write guard asks about the file the remote agent named, not a
// different one this hub's platform would spell that way.
//
// Round forty-six taught the op path to fold only for this machine's own
// callers and left the lifecycle hooks and the guard folding
// unconditionally, so on a Windows hub a unix agent's claim on
// `/work/a\b` stayed literal while a guard query about the same file
// became `/work/a/b`: a different path, no claim on it, and `allow`
// returned for a file somebody holds exclusively. Every entry point asks
// foldFor now. Round forty-seven of the pre-release review.
func TestTheGuardDoesNotFoldARemoteAgentsFilename(t *testing.T) {
	st := core.NewState("hub-node", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	e.SetHostID("hub-node")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	old := foldsSeparators
	foldsSeparators = true // this daemon is the Windows one
	t.Cleanup(func() { foldsSeparators = old })

	const odd = `/work/a\b`
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "holder", SessionID: "host-1",
		Agent: &core.AgentInfo{CWD: "/work", HostID: "machine-b"},
	})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatalf("setup: ack: %v", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpClaim, Token: tok, Path: odd, Mode: core.ClaimExclusive,
	}); err != nil {
		t.Fatalf("setup: claim: %v", err)
	}

	// A DIFFERENT agent on that same machine asks whether it may write
	// the file the first one holds.
	other, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "other", SessionID: "host-2",
		Agent: &core.AgentInfo{CWD: "/work", HostID: "machine-b"},
	})
	if err != nil {
		t.Fatalf("setup: register other: %v", err)
	}
	otok, _ := other["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: otok}); err != nil {
		t.Fatalf("setup: ack other: %v", err)
	}

	got, err := e.GuardPathFrom(ctx, "host-2", odd, "/work", "machine-b")
	if err != nil {
		t.Fatalf("guard: %v", err)
	}
	if got["decision"] == "allow" {
		t.Fatalf("the guard allowed a write to %q, which another agent holds exclusively: "+
			"the hub folded the query into a path no claim covers (%v)", odd, got)
	}
}
