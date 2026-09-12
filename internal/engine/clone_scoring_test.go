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
	predA, _, _ := e.predictIn(ctx, after, work)
	predB, _, _ := e.predictIn(ctx, before, work)
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
