package core

import (
	"testing"
)

// What a whole fleet experiences, at the base rate a fleet actually has.
//
// # Why the golden set is not enough
//
// It holds eleven hand-picked pairs, several of them positive. A real board is
// almost entirely negative: twelve agents produce sixty-six pairs, and on a normal
// day one or two of those are genuine overlaps. Precision measured on a balanced
// set says very little about that, because the quantity an agent actually
// experiences is not "of the warnings shown, how many were right" but "how often
// am I interrupted for nothing" — and that scales with the number of PEERS, which
// a pairwise metric hides entirely.
//
// An agent with one false-alert-per-pair rate of 2% meets eleven peers and is
// wrong-footed 20% of the time. That is the number that decides whether a fleet
// keeps Lanes switched on, and nothing in the repository measured it.
//
// # What this asserts
//
// Two things, both per-AGENT rather than per-pair:
//
//	no agent is auto-joined to work it is not doing — zero tolerance, because
//	that is the failure that makes an agent stand down real work;
//	and the share of agents seeing at least one spurious WARNING stays low
//	enough that warnings keep meaning something.
//
// The fleet below is synthetic but its shape is not invented: disjoint
// subsystems, shared build files, shared vocabulary, a couple of genuine
// overlaps, and agents who declare unevenly — which is what the live fleet looked
// like.
func fleet() []struct {
	name string
	slot Slot
	cwd  string
} {
	mk := func(name, text string, dirs, refs []string, act string, holds ...string) struct {
		name string
		slot Slot
		cwd  string
	} {
		return struct {
			name string
			slot Slot
			cwd  string
		}{name, Slot{
			Text: text, Dirs: dirs, Refs: refs, Activity: act, Holds: holds,
			Predicted: fp(dirs...),
		}, "/repo"}
	}
	return []struct {
		name string
		slot Slot
		cwd  string
	}{
		// Ten agents on genuinely separate work, the way a partitioned fleet looks.
		mk("cli", "CLI flags and command help", []string{"cli"}, []string{"pr:100"}, "implement"),
		mk("docs", "documentation site and guides", []string{"docs"}, []string{"goal:green-main"}, "document"),
		mk("runtime", "runtime scheduler internals", []string{"runtime/sched"}, []string{"pr:101"}, "implement"),
		mk("ci", "CI throughput and caching", []string{".github"}, []string{"goal:fast-ci"}, "implement"),
		mk("web", "web dashboard components", []string{"web/ui"}, []string{"pr:102"}, "implement"),
		mk("auth", "auth token refresh", []string{"internal/auth"}, []string{"pr:103"}, "implement"),
		mk("store", "blob store retention", []string{"internal/store"}, []string{"pr:104"}, "implement"),
		mk("sdk", "JS SDK dependency bumps", []string{"sdks/js"}, []string{"goal:green-main"}, "implement"),
		mk("proto", "wire protocol version negotiation", []string{"internal/proto"}, []string{"pr:105"}, "implement"),
		mk("release", "release tooling and signing", []string{"tools/release"}, nil, "release"),
		// Two genuine overlaps, which the fleet must still catch.
		mk("auth-2", "the token refresh path", []string{"internal/auth"}, []string{"pr:103"}, "implement"),
		mk("ci-2", "CI cache keys", []string{".github"}, nil, "test", "port:8080"),
	}
}

// truePairs are the overlaps a perfect system would report. Everything else is a
// pair that must stay quiet, or at most whisper.
var truePairs = map[string]bool{
	"auth|auth-2": true, // same PR, same directory, same role
	"ci|ci-2":     true, // same directory
}

func TestFalseAlertsAcrossAWholeFleet(t *testing.T) {
	agents := fleet()
	const repo = "/repo"

	joinedWrongly := map[string]bool{}
	warnedWrongly := map[string]bool{}
	caught := map[string]bool{}
	pairs := 0

	for i := range agents {
		for j := i + 1; j < len(agents); j++ {
			pairs++
			a, b := agents[i], agents[j]
			ev := EvidenceBetween(a.slot, b.slot, a.cwd, b.cwd, repo, nil, nil)
			rel := ev.Classify()
			key := a.name + "|" + b.name
			real := truePairs[key] || truePairs[b.name+"|"+a.name]

			if real {
				if rel != RelationNone {
					caught[key] = true
				}
				continue
			}
			// An auto-join on work these two are not sharing is the expensive
			// failure: it is what makes an agent stand down something real.
			if rel == RelationSameItem && !ev.Complementary {
				joinedWrongly[a.name], joinedWrongly[b.name] = true, true
				t.Errorf("FALSE AUTO-JOIN %s ↔ %s: %+v", a.name, b.name, ev)
			}
			// A warning loud enough to interrupt. A broad-territory note is
			// deliberately not counted: it is explicitly framed as awareness.
			if rel == RelationSameSurface && !ev.SurfaceBroad {
				warnedWrongly[a.name], warnedWrongly[b.name] = true, true
				t.Logf("spurious warning %s ↔ %s on %v", a.name, b.name, ev.SurfaceDeclared)
			}
		}
	}

	t.Logf("")
	t.Logf("FLEET OF %d — %d pairs, %d genuine overlaps (%.1f%% base rate)",
		len(agents), pairs, len(truePairs), 100*float64(len(truePairs))/float64(pairs))
	t.Logf("  agents auto-joined to work they are not doing: %d", len(joinedWrongly))
	t.Logf("  agents seeing at least one spurious warning:   %d of %d", len(warnedWrongly), len(agents))
	t.Logf("  genuine overlaps caught:                       %d of %d", len(caught), len(truePairs))

	if len(joinedWrongly) > 0 {
		t.Errorf("%d agents auto-joined wrongly; the tolerance for this is zero", len(joinedWrongly))
	}
	// Per AGENT, not per pair. A 2% per-pair error rate means one agent in five is
	// wrong-footed once it has eleven peers, and that is what decides whether a
	// fleet leaves Lanes switched on.
	if share := float64(len(warnedWrongly)) / float64(len(agents)); share > 0.25 {
		t.Errorf("%.0f%% of agents see a spurious warning; warnings stop meaning anything "+
			"long before that", 100*share)
	}
	// And it must still catch what it is for.
	if len(caught) != len(truePairs) {
		for k := range truePairs {
			if !caught[k] {
				t.Errorf("MISSED a genuine overlap: %s", k)
			}
		}
	}
}

// The same fleet, reported rather than asserted: how many peers must an agent
// meet before it is interrupted for nothing? Pairwise precision hides this
// entirely, and it is the number an operator feels.
func TestNoiseGrowsWithFleetSize(t *testing.T) {
	agents := fleet()
	const repo = "/repo"
	for _, n := range []int{2, 4, 8, len(agents)} {
		if n > len(agents) {
			continue
		}
		noisy := map[string]bool{}
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				a, b := agents[i], agents[j]
				if truePairs[a.name+"|"+b.name] || truePairs[b.name+"|"+a.name] {
					continue
				}
				ev := EvidenceBetween(a.slot, b.slot, a.cwd, b.cwd, repo, nil, nil)
				if rel := ev.Classify(); rel == RelationSameSurface && !ev.SurfaceBroad {
					noisy[a.name], noisy[b.name] = true, true
				}
			}
		}
		t.Logf("fleet of %2d: %d agent(s) interrupted for nothing", n, len(noisy))
	}
	t.Logf("(reported, not asserted — the assertion lives in %s)",
		"TestFalseAlertsAcrossAWholeFleet")
}
