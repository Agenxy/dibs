package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// An index the daemon mined from its own disk scores the agents of THAT
// tree, and an agent on another machine at the same path is not in it.
//
// Indexes are keyed by directory, and a directory is only evidence on one
// computer: `/workspace/repo` on the hub and `/workspace/repo` on a member
// can be two projects. A remote agent declaring from that path was scored by
// the hub's index, so its predicted footprint came from a history it has
// nothing to do with, and the hub's registration hook scheduled a local
// discovery of the remote path into the bargain. A remote clone of the SAME
// project (same remote or root commits) is still scored: a clone's history
// is the right coordinate system, and that is what peer scoring compares.
// Found by the pre-release review, round three.
func TestARemoteAgentIsNotScoredByTheHubsIndexOfAnotherProjectAtThatPath(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	seen := make(chan string, 4)
	e.OnRepoSeen(func(cwd string) { seen <- cwd })

	const path = "/workspace/repo"
	hub := core.AgentInfo{RepoDir: path + "/.git", RepoRemote: "github.com/acme/hub-project", RepoRoots: "r-hub"}
	e.SetIndex(path, cloneScorer{"hub", "src/hub.go"}, MatchConfig{Deadline: time.Second},
		IndexInfo{Fingerprint: "hist-hub", Identity: hub})

	register := func(name string, info core.AgentInfo) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Agent: &info})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		return tok
	}
	// A stranger's project on another machine, at the same path.
	stranger := register("stranger", core.AgentInfo{
		CWD: path, HostID: "member-host", RepoDir: path + "/.git",
		RepoRemote: "github.com/acme/other-project", RepoRoots: "r-other",
	})
	// A clone of the hub's project on another machine, at the same path.
	clone := register("clone", core.AgentInfo{
		CWD: path, HostID: "member-host", RepoDir: path + "/.git",
		RepoRemote: "github.com/acme/hub-project", RepoRoots: "r-hub",
	})
	// The hub's own agent, for contrast.
	local := register("local", core.AgentInfo{
		CWD: path, RepoDir: path + "/.git",
		RepoRemote: "github.com/acme/hub-project", RepoRoots: "r-hub",
	})

	if s, _ := e.scorerForLocation(e.locationForToken(ctx, stranger)); s != nil {
		t.Errorf("a remote agent in another project at the hub's path was scored by the hub's index")
	}
	if s, _ := e.scorerForLocation(e.locationForToken(ctx, clone)); s == nil {
		t.Errorf("a remote clone of the hub's project was refused the hub's index")
	}
	if s, _ := e.scorerForLocation(e.locationForToken(ctx, local)); s == nil {
		t.Errorf("the hub's own agent was refused the hub's index")
	}

	// And the hub never went looking for the remote path on its own disk:
	// only the local registration is a tree to discover.
	var discovered []string
	deadline := time.After(2 * time.Second)
	for len(discovered) < 1 {
		select {
		case d := <-seen:
			discovered = append(discovered, d)
		case <-deadline:
			t.Fatal("the local registration was never offered for discovery")
		}
	}
	select {
	case d := <-seen:
		t.Errorf("a second discovery was scheduled for %q: a remote agent's path is not a tree this daemon can read", d)
	case <-time.After(200 * time.Millisecond):
	}
}

// A resumed agent's tree is offered for discovery, as a registered one's is.
//
// Eviction reads archived agents as gone, so an archived agent's index goes
// with the next pass at the repository ceiling. That agent can resume (its
// row and mailbox are kept for exactly that), and the resume path scheduled
// no discovery, so it came back to a board with matching unavailable for its
// tree until some other registration happened to rebuild the index. Found by
// the pre-release review, round three.
func TestAResumedAgentsTreeIsOfferedForDiscovery(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	seen := make(chan string, 4)
	e.OnRepoSeen(func(cwd string) { seen <- cwd })

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "back", Nonce: "n-back", Agent: &core.AgentInfo{CWD: "/work/tree"},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("setup: the registration was never offered for discovery")
	}
	// Archived by the timer, as retention does; the index under it evicted.
	_, _ = e.query(ctx, func() core.Result {
		e.state.Agents[res["agent_id"].(string)].Status = core.StatusArchived
		return core.Result{}
	})

	if _, err := e.Do(ctx, &core.Op{Kind: core.OpResume, Nonce: "n-back", ResumeID: "r1"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	select {
	case d := <-seen:
		if d != "/work/tree" {
			t.Errorf("discovery offered %q, want the resumed agent's tree", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the resumed agent's tree was never offered for discovery: matching stays " +
			"unavailable for it until somebody else registers there")
	}
}
