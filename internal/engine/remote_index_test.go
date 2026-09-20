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

// A remote agent's tree is reported as one this daemon cannot read, so the
// bridge on that machine ships its index.
//
// The bridge ships on the daemon's verdict, and the verdict for a remote
// path was never recorded: the daemon does not go looking for another
// machine's tree (correctly), so it never found it unreadable, and the
// bridge exhausted its schedule without shipping. `/api/index` accepting
// remote shipments did nothing for a client that never sent one. Found by
// the pre-release review, round four.
func TestARemoteAgentsTreeIsReportedForItsBridgeToShip(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "member",
		Agent: &core.AgentInfo{CWD: "/srv/checkout", HostID: "member-host"},
	}); err != nil {
		t.Fatal("setup:", err)
	}
	ms := e.MatchStatus()
	if len(ms.Remote) != 1 || ms.Remote[0] != "/srv/checkout" {
		t.Errorf("match status lists remote trees %v, want the member's checkout: without it "+
			"the member's bridge never ships an index for a tree only it can read", ms.Remote)
	}
	if len(ms.Unreadable) != 0 {
		t.Errorf("a remote tree was listed as UNREADABLE %v: that list carries a permissions "+
			"hint about this machine's disk, which is not where the tree is", ms.Unreadable)
	}
}

// And the other direction: an index shipped from another machine scores
// nothing on this one. A member's index for `/workspace/repo` arrived first;
// a local agent then registered from an unrelated checkout at that path and
// was scored by the member's history. Round four of the pre-release review.
func TestALocalAgentIsNotScoredByAnotherMachinesShippedIndex(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	const path = "/workspace/repo"
	e.SetIndex(path, cloneScorer{"member", "src/member.go"}, MatchConfig{Deadline: time.Second},
		IndexInfo{Fingerprint: "hist-member", SuppliedBy: "member", SuppliedHost: "member-host"})

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "local", Agent: &core.AgentInfo{
		CWD: path, RepoDir: path + "/.git", RepoRoot: path, RepoRemote: "github.com/acme/unrelated",
	}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if s, _ := e.scorerForLocation(e.locationForToken(ctx, res["token"].(string))); s != nil {
		t.Error("a local agent was scored by an index shipped from another machine for a tree at the same path")
	}
	// The member itself still is.
	res, err = e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "member", Agent: &core.AgentInfo{
		CWD: path, HostID: "member-host", RepoRoot: path,
	}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if s, _ := e.scorerForLocation(e.locationForToken(ctx, res["token"].(string))); s == nil {
		t.Error("the shipper was refused the index it shipped")
	}
}

// And after a restart, when the daemon walks the board for trees to index,
// a remote agent's tree is relisted for its bridge: the daemon's memory of
// what was supplied is gone with the restart, and the bridge ships on the
// list. Round five of the pre-release review.
func TestARestartedDaemonRelistsRemoteTreesForTheirBridges(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "member", NewToken: "t",
		Agent: &core.AgentInfo{CWD: "/srv/checkout", HostID: "member-host"},
	}, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	// A cold engine over the replayed state: the restart.
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if dirs := e.WorkingDirectories(ctx); len(dirs) != 0 {
		t.Errorf("a remote tree was offered for local indexing: %v", dirs)
	}
	if ms := e.MatchStatus(); len(ms.Remote) != 1 || ms.Remote[0] != "/srv/checkout" {
		t.Errorf("after the boot walk the remote list is %v, want the member's checkout", ms.Remote)
	}
}

// Two clones of one project on two machines, declaring the same identifying
// reference, are matched as one repository.
//
// The repository lens asked Git here about both agents' directories, which
// for a directory on another machine answers nothing, and the fall-through
// then read two different checkout roots as two repositories: the two got
// "different repositories" and the exact-reference join that pr:42 on both
// sides earns was withheld. The board records each agent's repository
// identity at registration; the lens now answers from that, host-aware, and
// asks Git only for a local directory that recorded nothing. Round thirteen
// of the pre-release review.
func TestTwoClonesOnTwoMachinesMatchOnTheRecordedRepository(t *testing.T) {
	st := core.NewState("hub", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	reg := func(name string, info core.AgentInfo) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Agent: &info})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	// The hub has a tree of its own, indexed, and one of the clones sits at
	// the SAME PATH on its machine: the path repeats across machines, and
	// the fall-through read "one is inside the indexed tree and the other is
	// not" as two repositories. Git here can say nothing about either.
	hubTree := t.TempDir()
	e.SetIndex(hubTree, cloneScorer{"hub", "x.go"}, MatchConfig{Deadline: time.Second}, IndexInfo{Fingerprint: "h-hub"})
	one := reg("one", core.AgentInfo{
		CWD: hubTree, RepoRoot: hubTree, RepoDir: hubTree + "/.git",
		RepoRemote: "github.com/acme/api", RepoRoots: "r1", HostID: "machine-a",
	})
	two := reg("two", core.AgentInfo{
		CWD: "/machine-b/checkout", RepoRoot: "/machine-b/checkout", RepoDir: "/machine-b/checkout/.git",
		RepoRemote: "github.com/acme/api", RepoRoots: "r1", HostID: "machine-b",
	})

	resOne, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: one, Text: "pr 42", Refs: []string{"pr:42"}})
	if err != nil {
		t.Fatal(err)
	}
	opened := suggestions(t, resOne)
	if len(opened) != 1 || opened[0].Action != "opened" {
		t.Fatalf("one's declaration: %+v, want it to open a space", opened)
	}
	resTwo, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: two, Text: "pr 42", Refs: []string{"pr:42"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range suggestions(t, resTwo) {
		if s.Space != opened[0].Space {
			continue
		}
		if !s.Evidence.SameRepo {
			t.Fatalf("two clones of one project on two machines were read as different repositories: %+v", s)
		}
		if len(s.SharedRefs) == 0 {
			t.Fatalf("the shared pr:42 was not counted: %+v", s)
		}
		return
	}
	t.Fatalf("two's declaration did not surface one's space at all: %+v", suggestions(t, resTwo))
}

// And the other way round: two different projects on two machines, one at a
// path nested under the other's, are NOT one repository, whatever the shape
// of the paths says. The shape fall-through read a nested path as a
// worktree of the outer one; the recorded identities say otherwise, and a
// shared pr:42 across two projects is two different PRs. Round thirteen.
func TestNestedPathsAcrossMachinesAreNotOneRepository(t *testing.T) {
	st := core.NewState("hub", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	reg := func(name string, info core.AgentInfo) string {
		t.Helper()
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Agent: &info})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	outer := reg("outer", core.AgentInfo{
		CWD: "/srv/work", RepoRoot: "/srv/work", RepoDir: "/srv/work/.git",
		RepoRemote: "github.com/acme/api", RepoRoots: "r-api", HostID: "machine-a",
	})
	inner := reg("inner", core.AgentInfo{
		CWD: "/srv/work/vendor/web", RepoRoot: "/srv/work/vendor/web", RepoDir: "/srv/work/vendor/web/.git",
		RepoRemote: "github.com/other/web", RepoRoots: "r-web", HostID: "machine-b",
	})
	resOuter, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: outer, Text: "pr 42", Refs: []string{"pr:42"}})
	if err != nil {
		t.Fatal(err)
	}
	opened := suggestions(t, resOuter)
	if len(opened) != 1 || opened[0].Action != "opened" {
		t.Fatalf("outer's declaration: %+v, want it to open a space", opened)
	}
	resInner, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: inner, Text: "pr 42", Refs: []string{"pr:42"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range suggestions(t, resInner) {
		if s.Space != opened[0].Space {
			continue
		}
		if s.Action == "joined" || s.Action == "queued" || (s.Evidence.RepoKnown && s.Evidence.SameRepo) {
			t.Fatalf("two different projects on two machines were read as one repository from the shape "+
				"of their paths: %+v", s)
		}
	}
}
