package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
	"github.com/agenxy/dibs/internal/paths"
)

// cloneScorer answers every declaration with one fixed path: these tests are
// about WHICH indexes a declaration is scored in, not what they predict.
type cloneScorer struct{ id, path string }

func (c cloneScorer) ID() string      { return c.id }
func (c cloneScorer) Version() string { return "test" }
func (c cloneScorer) Predict(context.Context, string, int) (overlap.Prediction, error) {
	return overlap.Prediction{ScorerID: c.id, Files: []overlap.File{{Path: c.path, Weight: 1}}}, nil
}

// A declaration is scored in every OTHER index of the same project, and the
// result is recorded on the op beside the home prediction: the same sentence
// in each coordinate system the project has, so the fold can compare two
// clones inside one they share. Issue #39.
//
// Not in an index of a different project, whose history says nothing about
// this sentence; and not in a peer whose history is identical, which would
// record the home prediction twice under another name.
func TestADeclarationIsScoredInEveryPeerIndexOfItsProject(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	home, peer, twin, other := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := MatchConfig{Deadline: time.Second}
	project := core.AgentInfo{RepoRemote: "github.com/acme/api", RepoRoots: "r1"}
	e.SetIndex(home, cloneScorer{"home", "internal/session/token.go"}, cfg,
		IndexInfo{Fingerprint: "hist-after", Identity: project})
	e.SetIndex(peer, cloneScorer{"peer", "pkg/auth/refresh.go"}, cfg,
		IndexInfo{Fingerprint: "hist-before", Identity: project})
	e.SetIndex(twin, cloneScorer{"twin", "internal/session/token.go"}, cfg,
		IndexInfo{Fingerprint: "hist-after", Identity: project}) // same history as home
	e.SetIndex(other, cloneScorer{"other", "src/main.rs"}, cfg,
		IndexInfo{Fingerprint: "hist-other", Identity: core.AgentInfo{RepoRemote: "github.com/acme/web"}})

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "a", Agent: &core.AgentInfo{CWD: home}})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatalf("setup: ack: %v", err)
	}
	if _, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: tok, Text: "refresh token expiry"}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	var slot core.Slot
	_, _ = e.query(ctx, func() core.Result {
		for _, sl := range e.state.AgentByToken(tok).Slots {
			slot = sl
		}
		return core.Result{}
	})
	if slot.Index != "hist-after" {
		t.Errorf("slot.Index = %q, want the home index's fingerprint: without it nothing "+
			"can say which coordinate system Predicted is in", slot.Index)
	}
	if len(slot.Footprints) != 1 {
		t.Fatalf("footprints = %+v, want exactly one, in the peer clone: the other "+
			"project's index says nothing about this sentence, and the twin with an "+
			"identical history would only record the home prediction twice", slot.Footprints)
	}
	fp := slot.Footprints[0]
	if fp.Index != "hist-before" || fp.Root != peer || len(fp.Files) != 1 || fp.Files[0].Path != "pkg/auth/refresh.go" {
		t.Errorf("footprint = %+v, want the peer's fingerprint, its root, and its prediction", fp)
	}
}

// gitIn runs git in dir with a hermetic identity, so the fixture repositories
// need no global config and sign nothing.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitFile(t *testing.T, dir, rel, body, subject string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "--no-gpg-sign", "-m", subject)
}

// The concrete failure from issue #39, end to end through the engine with
// real git history and the real scorer.
//
// Clone A is on main after an auth refactor: its recent history maps
// "refresh token expiry" to internal/session/token.go. Clone B is a clone
// taken before the refactor, where the same concern is pkg/auth/refresh.go.
// Both agents declare the same work. Each clone's index predicts a path the
// other clone does not have, so the two predictions are disjoint, and the
// comparison used to score zero: the warning was lost, and the duplicate work
// collided when one fix was ported.
//
// The property, not a score: two clones with divergent histories now warn
// where they used to be silent. A's index window is bounded so that it covers
// only the post-refactor commits, which is the "-match-history window can make
// two clones sample very different distributions" case the issue names; a
// shallow or stale clone produces the same shape.
func TestTwoClonesWithDivergentHistoriesAreWarned(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "-b", "main")
	commitFile(t, origin, "README.md", "auth service\n", "initial layout")
	commitFile(t, origin, "pkg/auth/refresh.go", "package auth // v1\n", "refresh token expiry check")
	commitFile(t, origin, "pkg/auth/refresh.go", "package auth // v2\n", "refresh token expiry off by one")
	commitFile(t, origin, "pkg/auth/refresh.go", "package auth // v3\n", "refresh token expiry uses server clock")
	commitFile(t, origin, "cmd/server/main.go", "package main\n", "server entrypoint")

	// B: the maintenance clone, before the refactor.
	before := filepath.Join(t.TempDir(), "before")
	gitIn(t, filepath.Dir(before), "clone", "-q", origin, before)

	// A: main after the refactor moved the concern into internal/session.
	after := filepath.Join(t.TempDir(), "after")
	gitIn(t, filepath.Dir(after), "clone", "-q", origin, after)
	if err := os.MkdirAll(filepath.Join(after, "internal", "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, after, "mv", "pkg/auth/refresh.go", "internal/session/token.go")
	gitIn(t, after, "commit", "-q", "--no-gpg-sign", "-m", "move session handling")
	commitFile(t, after, "internal/session/token.go", "package session // v4\n", "refresh token expiry after move")
	commitFile(t, after, "internal/session/token.go", "package session // v5\n", "refresh token expiry grace window")
	commitFile(t, after, "internal/session/token.go", "package session // v6\n", "refresh token expiry logging")
	commitFile(t, after, "internal/session/store.go", "package session\n", "session store")

	index := func(dir string, window int) (overlap.Scorer, IndexInfo) {
		cc, err := overlap.MineCoChange(ctx, dir, overlap.CoChangeOptions{MaxCommits: window})
		if err != nil {
			t.Fatalf("mine %s: %v", dir, err)
		}
		lex, err := overlap.NewLexical(ctx, dir, cc)
		if err != nil {
			t.Fatalf("index %s: %v", dir, err)
		}
		repoDir, remote, roots, _ := paths.Identify(dir).Identity()
		return lex, IndexInfo{
			Fingerprint: cc.Fingerprint(),
			Identity:    core.AgentInfo{RepoDir: repoDir, RepoRemote: remote, RepoRoots: roots},
		}
	}
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	go e.Run(ctx)
	cfg := MatchConfig{Deadline: 5 * time.Second}
	afterScorer, afterInfo := index(after, 4)
	beforeScorer, beforeInfo := index(before, 0)
	if afterInfo.Fingerprint == "" || afterInfo.Fingerprint == beforeInfo.Fingerprint {
		t.Fatalf("fingerprints after=%q before=%q: the fixture must present two histories", afterInfo.Fingerprint, beforeInfo.Fingerprint)
	}
	if !core.SameProject(&afterInfo.Identity, &beforeInfo.Identity) {
		t.Fatalf("the two clones do not identify as one project: after=%+v before=%+v", afterInfo.Identity, beforeInfo.Identity)
	}
	e.SetIndex(after, afterScorer, cfg, afterInfo)

	reg := func(name, cwd string) string {
		res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Agent: &core.AgentInfo{CWD: cwd}})
		if err != nil {
			t.Fatalf("setup: register %s: %v", name, err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatalf("setup: ack %s: %v", name, err)
		}
		return tok
	}
	a := reg("after", after)

	const work = "fix refresh token expiry"
	resA, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: a, Text: work})
	if err != nil {
		t.Fatalf("declare in A: %v", err)
	}
	opened := suggestions(t, resA)
	if len(opened) != 1 || opened[0].Action != "opened" {
		t.Fatalf("A's declaration: %+v, want it to open the first space for this work", opened)
	}

	// B arrives later, from the older clone, and its index comes up with it:
	// A's declaration was never scored in B's system, so the ONLY coordinate
	// system the two share is A's, which is foreign to B.
	e.SetIndex(before, beforeScorer, cfg, beforeInfo)
	b := reg("before", before)

	// The two predictions really are disjoint: that is the precondition the
	// fix exists for, and a fixture where they overlap tests nothing.
	predA, _ := e.predictIn(ctx, location{cwd: after}, work)
	predB, _ := e.predictIn(ctx, location{cwd: before}, work)
	if shared := predPaths(sharedFiles(predA, predB)); len(shared) != 0 {
		t.Fatalf("the two clones' own predictions share %v: the fixture does not "+
			"reproduce divergent histories", shared)
	}

	resB, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: b, Text: work})
	if err != nil {
		t.Fatalf("declare in B: %v", err)
	}
	var warned *Suggestion
	for i := range suggestions(t, resB) {
		if s := &suggestions(t, resB)[i]; s.Space == opened[0].Space && s.Action != "opened" {
			warned = s
		}
	}
	if warned == nil {
		t.Fatalf("B's declaration: %+v, want a suggestion naming the space A opened: "+
			"identical work in two clones with divergent histories scored zero and "+
			"the warning was lost, which is issue #39 exactly", suggestions(t, resB))
	}
	if warned.Evidence.Semantic <= 0 {
		t.Errorf("semantic = %v, want > 0", warned.Evidence.Semantic)
	}
	// B is shown the score, told where it came from, and never a path its own
	// checkout does not have.
	if warned.Evidence.ScoredIn != after {
		t.Errorf("scored_in = %q, want A's tree %q", warned.Evidence.ScoredIn, after)
	}
	if len(warned.Evidence.SurfaceInferred) != 0 {
		t.Errorf("B was shown %v: the shared file is internal/session/token.go, which "+
			"exists only in A's checkout", warned.Evidence.SurfaceInferred)
	}
	if !strings.Contains(warned.Hint, after) {
		t.Errorf("hint = %q, want it to name the peer's checkout: a score with no "+
			"files has to say where the files are", warned.Hint)
	}
}

func suggestions(t *testing.T, res core.Result) []Suggestion {
	t.Helper()
	out, _ := res["spaces"].([]Suggestion)
	return out
}

func sharedFiles(a, b []core.PredFile) []core.PredFile {
	in := map[string]bool{}
	for _, f := range a {
		in[f.Path] = true
	}
	var out []core.PredFile
	for _, f := range b {
		if in[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// An index an agent SHIPPED (issue #19) is that agent's word about a tree the
// daemon could not read. It scores the agents the daemon has no better
// knowledge about, and nothing else: not an agent the daemon placed in a
// repository of its own, even one beneath the claimed root, and never as a
// peer index of a project whose remote the payload happens to name. The
// Codex review of #102 found both holes: a payload rooted at a parent
// directory scored every checkout under it, and a payload naming a real
// project's remote joined that project's peer set.
func TestAShippedIndexScoresOnlyAgentsTheDaemonCannotPlace(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{Deadline: time.Second}
	project := core.AgentInfo{RepoRemote: "github.com/acme/api", RepoRoots: "r1"}
	parent := t.TempDir()
	placed := filepath.Join(parent, "api") // a checkout the daemon resolved itself
	unplaced := filepath.Join(parent, "dark", "pkg")
	e.SetIndex(placed, cloneScorer{"mine", "internal/session/token.go"}, cfg,
		IndexInfo{Fingerprint: "hist-mine", Identity: project})
	// The shipment claims the PARENT, with the project's own identity.
	e.SetIndex(parent, cloneScorer{"shipped", "pkg/auth/refresh.go"}, cfg,
		IndexInfo{Fingerprint: "hist-shipped", Identity: project, SuppliedBy: "stranger"})

	s, _, _ := e.scorerForLocation(location{cwd: unplaced})
	if s == nil || s.ID() != "shipped" {
		t.Errorf("an agent the daemon could not place is scored by %v, want the shipped index: "+
			"that is the whole point of shipping one", s)
	}
	s, _, _ = e.scorerForLocation(location{cwd: filepath.Join(placed, "cmd"), repoRoot: placed})
	if s == nil || s.ID() != "mine" {
		t.Errorf("the placed agent is scored by %v, want the daemon's own index", s)
	}
	// The SHIPPER: its recorded root is the shipped root, and it is scored
	// by the index it shipped. Refusing every agent with a recorded root
	// would refuse the one agent the index exists for; the rule is "never
	// another repository", not "never a placed agent".
	s, _, _ = e.scorerForLocation(location{cwd: filepath.Join(parent, "pkg"), repoRoot: parent})
	if s == nil || s.ID() != "shipped" {
		t.Errorf("the shipper is scored by %v, want the index it shipped", s)
	}
	nested := filepath.Join(parent, "other")
	e.SetIndex(nested, cloneScorer{"theirs", "src/main.rs"}, cfg,
		IndexInfo{Fingerprint: "hist-theirs", Identity: core.AgentInfo{RepoRemote: "github.com/acme/web"}})
	s, _, _ = e.scorerForLocation(location{cwd: filepath.Join(parent, "other", "cmd"), repoRoot: nested})
	if s == nil || s.ID() != "theirs" {
		t.Errorf("a neighbour under the claimed root is scored by %v, want its own index", s)
	}
	// Longest root ordinarily wins; a shipped root LONGER than the placed one
	// still loses to the daemon's placement.
	e.SetIndex(filepath.Join(placed, "cmd"), cloneScorer{"deep", "x"}, cfg,
		IndexInfo{Fingerprint: "hist-deep", SuppliedBy: "stranger"})
	s, _, _ = e.scorerForLocation(location{cwd: filepath.Join(placed, "cmd", "dibd"), repoRoot: placed})
	if s == nil || s.ID() != "mine" {
		t.Errorf("a shipped index nested inside the placed checkout scores it: %v", s)
	}

	for _, home := range []string{placed, parent} {
		for _, p := range e.peerIndexesFor(home) {
			if p.info.SuppliedBy != "" || e.indexes[home].SuppliedBy != "" {
				t.Errorf("peers of %s include %s (shipped by %q): a shipped index is never a peer, "+
					"its identity is the shipper's word", home, p.root, p.info.SuppliedBy)
			}
		}
	}
	if by := e.IndexSuppliedBy(parent); by != "stranger" {
		t.Errorf("IndexSuppliedBy = %q", by)
	}
	if !e.TreeIsUnreadable(parent) {
		t.Error("a tree with a shipped index is still one the daemon cannot read: a newer shipment must be taken")
	}
}

// A supplied index never joins anyone, whatever the operator configured.
//
// SECURITY.md promises a shipped index "decides no claim, no role, no
// membership", and the operator's join threshold and auto-join policy used
// to apply to its scores exactly as to the daemon's own: with
// `auto_join = "always"` an agent's own index data could put other agents
// into a space. Its scores are suggestions. Found by the pre-release review.
func TestASuppliedIndexNeverJoinsAnyone(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{JoinThreshold: 0.3, AutoJoin: AutoJoinAlways, Deadline: time.Second}
	parent := t.TempDir()
	mine := filepath.Join(parent, "api")
	dark := filepath.Join(parent, "dark")
	e.SetIndex(mine, cloneScorer{"mine", "a.go"}, cfg, IndexInfo{Fingerprint: "h1"})
	e.SetIndex(dark, cloneScorer{"shipped", "b.go"}, cfg, IndexInfo{Fingerprint: "h2", SuppliedBy: "stranger"})

	_, got, _ := e.scorerForLocation(location{cwd: filepath.Join(dark, "pkg")})
	if got.JoinThreshold != 0 || got.AutoJoin != AutoJoinNever {
		t.Fatalf("a supplied index carries join threshold %v and auto-join %q: the operator's "+
			"membership policy applied to an agent's own index data", got.JoinThreshold, got.AutoJoin)
	}
	_, own, _ := e.scorerForLocation(location{cwd: filepath.Join(mine, "pkg"), repoRoot: mine})
	if own.JoinThreshold != 0.3 || own.AutoJoin != AutoJoinAlways {
		t.Errorf("the daemon's own index lost the operator's policy: %+v", own)
	}
}

// AND NOT FROM THE OTHER SIDE EITHER. The rule above downgrades the
// DECLARING agent's supplied index to suggestions; the peer's footprint can
// come from a shipped index too, and a local agent scored by this daemon's
// own index was joined to a remote peer's space under `auto_join = "always"`
// on the strength of the peer's shipped data, because only the declaring
// side's index was checked. Round thirteen of the pre-release review
// reproduced it with a local root and a shipped root nested inside it.
func TestAPeerScoredByASuppliedIndexIsNeverJoinedAutomatically(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{JoinThreshold: 0.3, AutoJoin: AutoJoinAlways, Deadline: time.Second}
	local := t.TempDir()
	shipped := filepath.Join(local, "remote")
	// Both indexes predict the same file for this work, so the footprints
	// overlap and the score clears the bar.
	e.SetIndex(local, cloneScorer{"local", "shared.go"}, cfg, IndexInfo{Fingerprint: "h-local"})
	e.SetIndex(shipped, cloneScorer{"shipped", "shared.go"}, cfg,
		IndexInfo{Fingerprint: "h-shipped", SuppliedBy: "far", SuppliedHost: "member"})

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
	// BOTH IN THE SHIPPED TREE, which is where a shared coordinate system
	// and a supplied index can both exist. Round thirteen reproduced this
	// with one agent in each tree, and their scores met only through the
	// home-to-home floor: round forty-two removed that comparison, because
	// SPEC-CHANNELS.md §4 compares declarations only inside a system both
	// carry, so the old topology now scores nothing for a reason that has
	// nothing to do with provenance. Two agents on the shipper's own
	// machine share h-shipped, and the rule still has to hold for them.
	far := reg("far", core.AgentInfo{CWD: shipped, RepoRoot: shipped, HostID: "member"})
	near := reg("near", core.AgentInfo{CWD: shipped, RepoRoot: shipped, HostID: "member"})

	const work = "fix refresh token expiry"
	resFar, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: far, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	opened := suggestions(t, resFar)
	if len(opened) != 1 || opened[0].Action != "opened" {
		t.Fatalf("far's declaration: %+v, want it to open a space", opened)
	}
	resNear, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: near, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	surfaced := false
	for _, s := range suggestions(t, resNear) {
		if s.Space != opened[0].Space {
			continue
		}
		surfaced = true
		if s.Action == "joined" || s.Action == "queued" {
			t.Fatalf("near was %s to far's space on the strength of a SHIPPED index: %+v", s.Action, s)
		}
	}
	if !surfaced {
		t.Fatal("near's declaration did not even surface far's space: the fixture does not " +
			"overlap, so nothing above was measured")
	}

	// THE CONTROL. The same two declarations in a tree this daemon mined
	// itself are joined, so the refusal above is the provenance and not a
	// fixture that never scored.
	mined := t.TempDir()
	e.SetIndex(mined, cloneScorer{"mined", "shared.go"}, cfg, IndexInfo{Fingerprint: "h-mined"})
	a := reg("mined-a", core.AgentInfo{CWD: mined, RepoRoot: mined})
	b := reg("mined-b", core.AgentInfo{CWD: mined, RepoRoot: mined})
	resA, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: a, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	theirs := suggestions(t, resA)
	if len(theirs) != 1 || theirs[0].Action != "opened" {
		t.Fatalf("the control's first declaration: %+v, want it to open a space", theirs)
	}
	resB, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: b, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	joined := false
	for _, s := range suggestions(t, resB) {
		if s.Space == theirs[0].Space && s.Action == "joined" {
			joined = true
		}
	}
	if !joined {
		t.Fatal("the control was not joined either: this test proves nothing about supplied " +
			"indexes if an ordinary one does not join")
	}
}

// AND THE PROVENANCE OUTLIVES THE CACHE. The check above read "was the
// peer's fingerprint shipped" from the index cache, which forgets a shipped
// index when it is replaced by a newer shipment or evicted, while the
// declarations scored in it stay on the board. After the replacement, the
// old footprint was no longer recognised and the join went through. The
// fold now records the provenance on the declaration itself. Round
// fourteen of the pre-release review.
func TestASuppliedFootprintStaysSuppliedAfterItsIndexIsReplaced(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{JoinThreshold: 0.3, AutoJoin: AutoJoinAlways, Deadline: time.Second}
	local := t.TempDir()
	shipped := filepath.Join(local, "remote")
	e.SetIndex(local, cloneScorer{"local", "shared.go"}, cfg, IndexInfo{Fingerprint: "h-local"})
	e.SetIndex(shipped, cloneScorer{"shipped", "shared.go"}, cfg,
		IndexInfo{Fingerprint: "h-shipped-1", SuppliedBy: "far", SuppliedHost: "member"})

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
	// Both on the shipper's machine, for the reason given in the test
	// above: after round forty-two a comparison needs a coordinate system
	// both declarations carry, and one agent per tree no longer has one.
	far := reg("far", core.AgentInfo{CWD: shipped, RepoRoot: shipped, HostID: "member"})
	near := reg("near", core.AgentInfo{CWD: shipped, RepoRoot: shipped, HostID: "member"})

	const work = "fix refresh token expiry"
	resFar, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: far, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	opened := suggestions(t, resFar)
	if len(opened) != 1 || opened[0].Action != "opened" {
		t.Fatalf("far's declaration: %+v, want it to open a space", opened)
	}
	resNear, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: near, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	surfaced := false
	for _, s := range suggestions(t, resNear) {
		if s.Space != opened[0].Space {
			continue
		}
		surfaced = true
		if s.Action == "joined" || s.Action == "queued" {
			t.Fatalf("near was %s to far's space on shipped data: %+v", s.Action, s)
		}
	}
	if !surfaced {
		t.Fatal("the two declarations did not meet at all: nothing above was measured")
	}

	// A newer shipment replaces the index; the cache forgets h-shipped-1
	// entirely, and the declarations scored in it stay on the board.
	e.SetIndex(shipped, cloneScorer{"shipped", "shared.go"}, cfg,
		IndexInfo{Fingerprint: "h-shipped-2", SuppliedBy: "far", SuppliedHost: "member"})
	e.RemoveScorerForRepo(shipped)

	// THE PROVENANCE IS ON THE DECLARATION, not in the cache that just
	// forgot it. Read off the board, because that is where a later judge
	// reads it from: the space it opened, and the slot that opened it.
	// The SPACE is where an opener's provenance is kept (its membership
	// carries a score and a footprint only when it joined on one), and the
	// space is what a later judge reads.
	var spaceSupplied, present bool
	onLoop(t, ctx, e, func(st *core.State) {
		ch := st.Spaces[opened[0].Space]
		present = ch != nil
		if present {
			spaceSupplied = ch.Supplied
		}
	})
	if !present {
		t.Fatal("the space far opened is gone")
	}
	if !spaceSupplied {
		t.Fatal("after the shipment was replaced the board no longer says the space was opened " +
			"on a supplied footprint: the provenance was being read back from an index cache " +
			"that had forgotten it, which is how a local agent got joined on shipped data")
	}
}

// AND IT OUTLIVES THE DECLARATION. Provenance on the slot decides only while
// the slot exists: undeclare removes it, and the space keeps the footprint
// that was merged in when the slot opened it, without the provenance. With
// no live declaration left to compare against, judgedScore falls back to
// that retained footprint, and the local agent was joined to the remote
// peer's space on the strength of the shipped data after all. The space now
// records that some of its footprint was supplied, and the fallback carries
// it. Round fifteen of the pre-release review.
func TestASuppliedFootprintStaysSuppliedAfterItsDeclarationIsRemoved(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{JoinThreshold: 0.3, AutoJoin: AutoJoinAlways, Deadline: time.Second}
	local := t.TempDir()
	shipped := filepath.Join(local, "remote")
	e.SetIndex(local, cloneScorer{"local", "shared.go"}, cfg, IndexInfo{Fingerprint: "h-local"})
	e.SetIndex(shipped, cloneScorer{"shipped", "shared.go"}, cfg,
		IndexInfo{Fingerprint: "h-shipped", SuppliedBy: "far", SuppliedHost: "member"})

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
	far := reg("far", core.AgentInfo{CWD: shipped, RepoRoot: shipped, HostID: "member"})
	near := reg("near", core.AgentInfo{CWD: local, RepoRoot: local})

	const work = "fix refresh token expiry"
	resFar, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: far, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	opened := suggestions(t, resFar)
	if len(opened) != 1 || opened[0].Action != "opened" {
		t.Fatalf("far's declaration: %+v, want it to open a space", opened)
	}
	slot, _ := resFar["slot_id"].(string)
	if slot == "" {
		t.Fatalf("setup: far's declaration returned no slot id: %v", resFar)
	}
	// The declaration goes; far stays a member, and the space keeps the
	// footprint the declaration gave it.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpClearSlot, Token: far, SlotID: slot}); err != nil {
		t.Fatalf("setup: undeclare: %v", err)
	}

	resNear, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: near, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range suggestions(t, resNear) {
		if s.Space != opened[0].Space {
			continue
		}
		if s.Action == "joined" || s.Action == "queued" {
			t.Fatalf("near was %s to far's space on a shipped footprint whose declaration had "+
				"been removed: %+v", s.Action, s)
		}
		return
	}
	t.Fatal("near's declaration did not even surface far's space: the fixture does not overlap")
}

// The fingerprint comes back with the scorer that answers in it.
//
// It was read separately, after the prediction, so an index replaced in
// between recorded one index's files under another's fingerprint: two
// coordinate systems then compared as one, which is the comparison issue
// #39 exists to prevent. One lock, one answer. Round twenty-eight of the
// pre-release review.
func TestTheScorerAndItsFingerprintComeTogether(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	cfg := MatchConfig{Deadline: time.Second}
	root := t.TempDir()
	e.SetIndex(root, cloneScorer{"first", "a.go"}, cfg, IndexInfo{Fingerprint: "hist-1"})

	s, _, fp := e.scorerForLocation(location{cwd: filepath.Join(root, "pkg"), repoRoot: root})
	if s == nil || s.ID() != "first" || fp != "hist-1" {
		t.Fatalf("scorer %v with fingerprint %q, want first/hist-1", s, fp)
	}
	// A shipment replaces it; the next lookup answers with both halves of
	// the NEW index, never one of each.
	e.SetIndex(root, cloneScorer{"second", "b.go"}, cfg, IndexInfo{Fingerprint: "hist-2"})
	s, _, fp = e.scorerForLocation(location{cwd: filepath.Join(root, "pkg"), repoRoot: root})
	if s == nil || s.ID() != "second" || fp != "hist-2" {
		t.Fatalf("after the replacement: scorer %v with fingerprint %q, want second/hist-2: a "+
			"prediction would be recorded in a coordinate system it was not made in", s, fp)
	}
	// And a location with no index of its own gets the fallback pair, whose
	// fingerprint describes whatever that pair is.
	if _, _, got := e.scorerForLocation(location{cwd: "/elsewhere"}); got != "" && got != "hist-2" {
		t.Fatalf("the fallback answered with fingerprint %q, which belongs to no index it returned", got)
	}
}

// A space opened by hand carries the provenance of the index that
// predicted its footprint.
//
// open_space predicted with the GLOBAL scorer, whichever tree was indexed
// first, and recorded nothing about where the footprint came from. When
// that index was one an agent had SHIPPED, the space held a footprint
// built from untrusted data with Supplied false, and the next local agent
// whose work overlapped it was joined automatically on the strength of it:
// SECURITY.md's "a supplied index decides no membership", through the one
// door that did not ask. It predicts in the opener's own index now, and
// records whether that index was supplied. Round thirty-two of the
// pre-release review.
func TestASpaceOpenedByHandCarriesItsIndexsProvenance(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{JoinThreshold: 0.3, AutoJoin: AutoJoinAlways, Deadline: time.Second}
	local := t.TempDir()
	shipped := filepath.Join(local, "remote")
	e.SetIndex(local, cloneScorer{"local", "shared.go"}, cfg, IndexInfo{Fingerprint: "h-local"})
	e.SetIndex(shipped, cloneScorer{"shipped", "shared.go"}, cfg,
		IndexInfo{Fingerprint: "h-shipped", SuppliedBy: "far", SuppliedHost: "member"})

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
	far := reg("far", core.AgentInfo{CWD: shipped, RepoRoot: shipped, HostID: "member"})
	near := reg("near", core.AgentInfo{CWD: local, RepoRoot: local})

	const work = "fix refresh token expiry"
	// The remote agent opens a space BY HAND, which is the door declare's
	// own checks do not cover.
	if _, err := e.OpenWithPrediction(ctx, &core.Op{
		Kind: core.OpSpaceOpen, Token: far, Space: "refresh", Text: work,
	}); err != nil {
		t.Fatalf("setup: open_space: %v", err)
	}
	res, err := e.DoMatched(ctx, &core.Op{Kind: core.OpSetSlot, Token: near, Text: work})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range suggestions(t, res) {
		if s.Space != "refresh" {
			continue
		}
		found = true
		if s.Action == "joined" || s.Action == "queued" {
			t.Fatalf("near was %s to a space whose footprint came from an index an agent shipped: "+
				"%+v", s.Action, s)
		}
	}
	if !found {
		t.Fatal("near's declaration did not surface the space at all: the fixture does not overlap")
	}
}
