package core

import (
	"strings"
	"testing"
)

// Two clones of one project are two co-change indexes, and when their
// histories differ the same concern lives at different paths in each. The
// judge used to compare one clone's prediction against the other's as though
// both were in one coordinate system: disjoint sets, a zero, and zero read as
// "no overlap". Issue #39.
//
// With each declaration also scored in the peer's index, the two are compared
// INSIDE the system they share, and the recipient is shown only the paths that
// system names as its own checkout names them.
func TestTwoClonesAreComparedInsideACoordinateSystemTheyShare(t *testing.T) {
	// a is on main after the refactor: its history maps the concern to
	// internal/session/token.go. b is on the maintenance branch before it,
	// where the same concern is pkg/auth/refresh.go. b's declaration was
	// also scored in a's index, so it carries a footprint there.
	a := Slot{
		Index:     "hist-after",
		Predicted: []PredFile{{Path: "internal/session/token.go", Weight: 1}},
	}
	b := Slot{
		Index:     "hist-before",
		Predicted: []PredFile{{Path: "pkg/auth/refresh.go", Weight: 1}},
		Footprints: []Footprint{{
			Index: "hist-after", Root: "/clones/after",
			Files: []PredFile{{Path: "internal/session/token.go", Weight: 1}},
		}},
	}

	// a is the recipient: the shared system is a's own, so the paths are valid
	// for it and shown.
	ev := EvidenceBetween(a, b, "", "", "", nil, nil)
	if ev.Semantic <= 0 {
		t.Fatalf("semantic = %v, want > 0: the two clones were compared across two "+
			"coordinate systems instead of inside the one they share, and the "+
			"duplicate work scored as no overlap", ev.Semantic)
	}
	if ev.Classify() != RelationPossible {
		t.Errorf("relation = %q, want %q", ev.Classify(), RelationPossible)
	}
	if len(ev.SurfaceInferred) != 1 || ev.SurfaceInferred[0] != "internal/session/token.go" {
		t.Errorf("surface_inferred = %v, want the shared file named as a's checkout "+
			"names it", ev.SurfaceInferred)
	}
	if ev.ScoredIn != "" {
		t.Errorf("scored_in = %q, want empty: the comparison happened in the "+
			"recipient's own index", ev.ScoredIn)
	}

	// b is the recipient: the same score, but the shared path is one b's
	// checkout does not have, so it is withheld and the score is explained.
	ev = EvidenceBetween(b, a, "", "", "", nil, nil)
	if ev.Semantic <= 0 {
		t.Fatalf("mirrored semantic = %v, want > 0: the comparison must not depend "+
			"on which clone declared second", ev.Semantic)
	}
	if len(ev.SurfaceInferred) != 0 {
		t.Errorf("surface_inferred = %v, want none: %q is a path in the PEER's "+
			"checkout, and an agent is never shown a path that may not exist in its "+
			"own", ev.SurfaceInferred, ev.SurfaceInferred[0])
	}
	if ev.ScoredIn != "/clones/after" {
		t.Errorf("scored_in = %q, want /clones/after: a score with no files needs "+
			"to say where the files are", ev.ScoredIn)
	}
	if why := ev.Strongest(); !strings.Contains(why, "/clones/after") {
		t.Errorf("Strongest() = %q, want it to name the peer's checkout", why)
	}
}

// Slots declared before any of this existed carry no index and no footprints,
// and compare exactly as they always did.
func TestSlotsWithoutFootprintsCompareAsBefore(t *testing.T) {
	a := Slot{Predicted: []PredFile{{Path: "x.go", Weight: 1}, {Path: "y.go", Weight: 1}}}
	b := Slot{Predicted: []PredFile{{Path: "x.go", Weight: 1}}}
	want, _ := jaccard(a.Predicted, b.Predicted, nil)
	ev := EvidenceBetween(a, b, "", "", "", nil, nil)
	if ev.Semantic != want || ev.ScoredIn != "" {
		t.Errorf("semantic = %v scored_in = %q, want %v and empty: an old slot's "+
			"comparison must be unchanged", ev.Semantic, ev.ScoredIn, want)
	}
	if len(ev.SurfaceInferred) != 1 || ev.SurfaceInferred[0] != "x.go" {
		t.Errorf("surface_inferred = %v, want [x.go]", ev.SurfaceInferred)
	}
}

// When two slots share more than one coordinate system, the best decides:
// "warn if either shared space clears the bar", not "warn if they agree".
func TestTheBestSharedCoordinateSystemDecides(t *testing.T) {
	a := Slot{
		Index:     "one",
		Predicted: []PredFile{{Path: "a.go", Weight: 1}, {Path: "b.go", Weight: 1}, {Path: "c.go", Weight: 1}},
		Footprints: []Footprint{{
			Index: "two", Root: "/two",
			Files: []PredFile{{Path: "shared.go", Weight: 1}},
		}},
	}
	b := Slot{
		Index:     "two",
		Predicted: []PredFile{{Path: "shared.go", Weight: 1}},
		Footprints: []Footprint{{
			Index: "one", Root: "/one",
			Files: []PredFile{{Path: "a.go", Weight: 1}},
		}},
	}
	weakInOne, _ := jaccard(a.Predicted, []PredFile{{Path: "a.go", Weight: 1}}, nil)
	ev := EvidenceBetween(a, b, "", "", "", nil, nil)
	if ev.Semantic <= weakInOne {
		t.Errorf("semantic = %v, want above %v: system two scores these identical "+
			"and system one scores them a third alike; the best shared system decides",
			ev.Semantic, weakInOne)
	}
	// The winning system is b's home, foreign to a, so a is told where the
	// score came from and shown only what its own system shares.
	if ev.ScoredIn != "/two" {
		t.Errorf("scored_in = %q, want /two", ev.ScoredIn)
	}
	if len(ev.SurfaceInferred) != 1 || ev.SurfaceInferred[0] != "a.go" {
		t.Errorf("surface_inferred = %v, want [a.go], the share in a's own system", ev.SurfaceInferred)
	}
}

// The op fields land on the slot, so replay reconstructs the same comparisons
// without a repository present, exactly as Predicted already does.
func TestDeclareRecordsItsCoordinateSystems(t *testing.T) {
	s := NewState("test", DefaultLimits())
	mustApply(t, s, &Op{Kind: OpRegister, Name: "a", NewToken: "tokA"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tokA"}, t0)
	fp := []Footprint{{Index: "peer", Root: "/peer", Files: []PredFile{{Path: "p.go", Weight: 0.5}}}}
	mustApply(t, s, &Op{Kind: OpSetSlot, Token: "tokA", Text: "w", Index: "home", Footprints: fp}, t0)

	sl := s.AgentByToken("tokA").Slots["s1"]
	if sl.Index != "home" || len(sl.Footprints) != 1 || sl.Footprints[0].Index != "peer" ||
		sl.Footprints[0].Files[0].Path != "p.go" {
		t.Errorf("slot = %+v, want the op's index and footprints recorded on it", sl)
	}
	// And stripped from the board, as Predicted is: it is the same
	// intermediate, and a 32-agent board paid 38 KB for the last one.
	for _, row := range s.Board()["agents"].([]map[string]any) {
		for _, bs := range row["slots"].([]Slot) {
			if bs.Index != "" || bs.Footprints != nil {
				t.Errorf("board slot carries index %q footprints %v: the scorer's "+
					"intermediates are not board payload", bs.Index, bs.Footprints)
			}
		}
	}
}
