package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
)

// blockingScorer holds its prediction until the test lets it go, which is
// the window everything here is about: prediction runs off the match lock,
// and the index it came from can be evicted or replaced while it does.
type blockingScorer struct {
	id, path string
	inside   chan struct{}
	release  chan struct{}
}

func (b blockingScorer) ID() string      { return b.id }
func (b blockingScorer) Version() string { return "test" }

func (b blockingScorer) Predict(context.Context, string, int) (overlap.Prediction, error) {
	close(b.inside)
	<-b.release
	return overlap.Prediction{ScorerID: b.id, Files: []overlap.File{{Path: b.path, Weight: 1}}}, nil
}

// A footprint predicted from a supplied index is recorded as supplied even
// if that index is gone by the time the prediction lands.
//
// Round thirty-two made open_space predict in the opener's own index and
// record whether it was one an agent shipped, because SECURITY.md promises
// that a supplied index "decides no membership" and auto-join reads that
// one bit. It read the bit afterwards, by repository path, out of the
// index map as it stood then: eviction runs while prediction is off the
// lock, so a footprint built from untrusted data was written with
// `supplied: false` and the next local agent whose work overlapped it was
// joined on the strength of it. The provenance travels with the scorer
// now. Round thirty-eight of the pre-release review.
func TestAFootprintKeepsItsSuppliedProvenanceWhenTheIndexIsEvicted(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{JoinThreshold: 0.3, AutoJoin: AutoJoinAlways, Deadline: 5 * time.Second}
	shipped := t.TempDir()
	scorer := blockingScorer{
		id: "shipped", path: "shared.go",
		inside: make(chan struct{}), release: make(chan struct{}),
	}
	e.SetIndex(shipped, scorer, cfg, IndexInfo{
		Fingerprint: "h-shipped", SuppliedBy: "far", SuppliedHost: "member",
	})

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "far",
		Agent: &core.AgentInfo{CWD: shipped, RepoRoot: shipped, HostID: "member"},
	})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatalf("setup: ack: %v", err)
	}

	op := &core.Op{Kind: core.OpSpaceOpen, Token: tok, Space: "refresh", Text: "fix refresh token expiry"}
	done := make(chan error, 1)
	go func() { _, err := e.OpenWithPrediction(ctx, op); done <- err }()

	<-scorer.inside                // the supplied index is answering
	e.RemoveScorerForRepo(shipped) // and the last agent in that tree leaves
	close(scorer.release)
	if err := <-done; err != nil {
		t.Fatalf("open_space: %v", err)
	}

	if len(op.Predicted) == 0 {
		t.Fatal("the prediction the supplied index returned was dropped")
	}
	if !op.IndexSupplied {
		t.Fatal("a footprint predicted from an index an agent shipped was recorded as the " +
			"daemon's own: auto-join reads that bit, so untrusted data decides membership")
	}
}

// An opener that says where it is, in a tree this daemon has no index
// for, is not predicted from some other project's index.
//
// A nil scorer for a location means two different things, and the fallback
// treated them as one: "this agent said nothing about where it is", which
// is ordinary for a space opened by hand and is what the daemon's own
// index is for, and "this agent's tree has no index the rules allow here".
// In the second case the fallback reached for whichever index the daemon
// happened to hold, including one an agent shipped for another machine,
// and seeded the space with another repository's files as overlap
// evidence. Round thirty-eight of the pre-release review.
func TestALocatedOpenerIsNotPredictedFromAnotherProjectsIndex(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	cfg := MatchConfig{JoinThreshold: 0.3, AutoJoin: AutoJoinAlways, Deadline: time.Second}
	indexed, elsewhere := t.TempDir(), t.TempDir()
	e.SetIndex(indexed, cloneScorer{"indexed", "other-project.go"}, cfg,
		IndexInfo{Fingerprint: "h-indexed"})

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

	// An agent in a tree of its own, which this daemon has never indexed.
	away := reg("away", core.AgentInfo{
		CWD: filepath.Join(elsewhere, "pkg"), RepoRoot: elsewhere,
	})
	op := &core.Op{Kind: core.OpSpaceOpen, Token: away, Space: "away-work", Text: "fix refresh token expiry"}
	if _, err := e.OpenWithPrediction(ctx, op); err != nil {
		t.Fatalf("open_space: %v", err)
	}
	if len(op.Predicted) > 0 {
		t.Fatalf("a space opened from an unindexed tree was seeded with %v: those files are "+
			"another project's, and every later match compares against them", op.Predicted)
	}
	if op.Index != "" {
		t.Fatalf("the space records index %q, which was mined from a tree its opener is not in", op.Index)
	}

	// And an opener that said nothing about where it is still gets the
	// daemon's own index, which is what the fallback is for.
	blind := reg("blind", core.AgentInfo{})
	byHand := &core.Op{Kind: core.OpSpaceOpen, Token: blind, Space: "by-hand", Text: "fix refresh token expiry"}
	if _, err := e.OpenWithPrediction(ctx, byHand); err != nil {
		t.Fatalf("open_space by hand: %v", err)
	}
	if len(byHand.Predicted) == 0 {
		t.Fatal("an opener with no location lost the daemon's own index: a space opened by hand " +
			"can never be matched against, which is what predicting at open time is for")
	}
}
