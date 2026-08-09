// Package overlap answers one question: are two agents doing the same work?
//
// Directory claims answer a narrower one — are two agents naming the same path
// — and that is the collision which is cheap to detect rather than the one that
// hurts. Two agents refactoring the same concept in different languages never
// name the same path and wreck each other anyway. See SPEC-CHANNELS.md §0.
//
// THE SHAPE, and the reason for it:
//
//	Scorer:  declaration ──► the files this work will touch
//	Overlap: two predictions ──► a score
//
// The tier-specific part is ONLY the prediction. Comparing two predictions is
// uniform, deterministic and shared, so swapping a lexical scorer for an
// embedding sidecar changes what is predicted and nothing about what a score
// means. It also makes the evaluation harness (eval.go) measure exactly the
// part that varies, against ground truth that already exists: git history.
//
// Nothing here may be called during ledger replay. Every value this package
// produces is impure — it reads the filesystem and, at higher tiers, a model —
// so scores are computed at the edge and RECORDED in the op, exactly as
// liveness probe verdicts are (SPEC §2, §7; SPEC-CHANNELS.md §4.3).
package overlap

import (
	"context"
	"sort"
)

// File is one predicted file and how strongly it was predicted.
//
// Weighted rather than a bare path because the tail of a prediction is mostly
// noise: a declaration mentioning "auth" pulls in the auth package strongly and
// half the test tree weakly, and treating those as equally-predicted work makes
// every pair of agents look related.
type File struct {
	Path   string  `json:"path"`
	Weight float64 `json:"weight"` // 0..1, best first
}

// Prediction is a scorer's answer, carrying enough provenance to explain itself
// later without re-running anything (SPEC-CHANNELS.md §10.3).
type Prediction struct {
	Files    []File   `json:"files"`
	ScorerID string   `json:"scorer_id"`
	Version  string   `json:"scorer_version"`
	Evidence []string `json:"evidence,omitempty"`
	// Degraded is set when this came from a lower tier than was configured,
	// because presenting a fallback as the real thing is a lie about how much
	// is known (SPEC-CHANNELS.md §10.5).
	Degraded bool `json:"degraded,omitempty"`
}

// Scorer turns a declaration of work into the files it is likely to touch.
//
// Implementations must be safe for concurrent use: the engine calls this from
// the request path, off the single writer loop.
type Scorer interface {
	ID() string
	Version() string
	// Predict is given the agent's own words ("refactoring the auth middleware")
	// and returns at most limit files, best first. An empty result is a valid
	// answer meaning "no idea", and callers must treat it as no evidence rather
	// than as evidence of no overlap (SPEC-CHANNELS.md §10.1).
	Predict(ctx context.Context, declaration string, limit int) (Prediction, error)
}

// Overlap scores two predictions against each other, in [0,1].
//
// This is a weighted Jaccard: the shared weight over the total weight. Plain
// set Jaccard was tried first and is wrong here for a specific reason — it
// counts a weak tail hit exactly as much as the one file both agents are about
// to rewrite, so two agents sharing nothing but `go.mod` and a test helper
// score the same as two agents sharing the package they are both editing.
//
// Symmetric, deterministic, and free of any I/O, so the same two predictions
// always yield the same number on any machine. That property is what lets the
// score be recorded in the ledger and trusted on replay.
func Overlap(a, b Prediction) float64 {
	if len(a.Files) == 0 || len(b.Files) == 0 {
		return 0
	}
	aw := make(map[string]float64, len(a.Files))
	for _, f := range a.Files {
		if f.Weight > aw[f.Path] {
			aw[f.Path] = f.Weight
		}
	}
	var shared, total float64
	seen := make(map[string]bool, len(b.Files))
	for _, f := range b.Files {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		if w, ok := aw[f.Path]; ok {
			shared += min(w, f.Weight)
			total += max(w, f.Weight)
			continue
		}
		total += f.Weight
	}
	for p, w := range aw {
		if !seen[p] {
			total += w
		}
	}
	if total == 0 {
		return 0
	}
	return shared / total
}

// Shared returns the files both predictions name, strongest first.
//
// This is the evidence half of a verdict. A score with no evidence is not
// explainable, and SPEC-CHANNELS.md §10.3 requires every auto-join to be
// explainable on demand — so the thing that produced the number has to hand
// back the reasons at the same time, not on request later when the index has
// moved on.
func Shared(a, b Prediction, limit int) []File {
	aw := make(map[string]float64, len(a.Files))
	for _, f := range a.Files {
		if f.Weight > aw[f.Path] {
			aw[f.Path] = f.Weight
		}
	}
	var out []File
	seen := make(map[string]bool, len(b.Files))
	for _, f := range b.Files {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		if w, ok := aw[f.Path]; ok {
			out = append(out, File{Path: f.Path, Weight: min(w, f.Weight)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// topN truncates to the n heaviest files, renormalising so the best is 1.0.
//
// Renormalisation keeps scores comparable across scorers whose raw magnitudes
// differ by orders of magnitude — a tier-0 token count and a tier-2 cosine have
// no common unit, and thresholds are configured once for all of them.
func topN(files []File, n int) []File {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Weight != files[j].Weight {
			return files[i].Weight > files[j].Weight
		}
		return files[i].Path < files[j].Path // stable across map iteration
	})
	if n > 0 && len(files) > n {
		files = files[:n]
	}
	if len(files) == 0 || files[0].Weight <= 0 {
		return files
	}
	// The top file is assigned exactly 1, not computed as w*(1/w): floating
	// point makes that 0.9999999999999999 for most inputs, and these weights are
	// compared against a user-calibrated threshold. Deterministic either way,
	// but "the strongest match scores 1" should be true rather than nearly true.
	scale := 1 / files[0].Weight
	files[0].Weight = 1
	for i := 1; i < len(files); i++ {
		files[i].Weight *= scale
	}
	return files
}
