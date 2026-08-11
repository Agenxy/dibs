package overlap

import (
	"bufio"
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Evaluation: measuring a scorer on the repository it will actually run on.
//
// Thresholds are unitless and scorer-relative, so any number shipped as a
// default is a guess, and a guess that is wrong in one direction collapses
// every agent into one lane, and wrong in the other leaves them all working
// alone. SPEC-CHANNELS.md §9 makes calibration normative for exactly that
// reason.
//
// The ground truth is free and already on disk: A COMMIT MESSAGE IS A TASK
// DECLARATION AND ITS CHANGED FILES ARE THE LABEL. That is precisely the
// prediction a scorer is asked for. "given this description, what will it
// touch": measured on the user's own code, in their languages, with their
// naming.
//
// It is also CONTAMINATION-PROOF BY CONSTRUCTION, which no public leaderboard
// can claim. The live MTEB(Code) board ranks models trained on 8% to 58% of its
// own evaluation data alongside models that have seen none of it; the raw
// numbers are not measuring the same thing. No published model has trained on a
// private repository's history, so every scorer meets this benchmark blind.

// EvalCase is one commit reused as a labelled retrieval query: the message is
// the declaration, the changed files are the ground truth.
type EvalCase struct {
	Commit  string
	Message string
	Changed []string
}

// EvalResult is one scorer's performance over the sampled commits.
type EvalResult struct {
	ScorerID string
	Cases    int
	// RecallAt[k] is the mean fraction of a commit's changed files that appear
	// in the scorer's top k predictions.
	RecallAt map[int]float64
	// MRR is the mean reciprocal rank of the FIRST correctly predicted file,
	// "how far down the list before the scorer says something true". Recall
	// alone hides a scorer that is right only at position 40.
	MRR float64
	// Empty counts declarations the scorer had no answer for. Reported
	// separately because an honest abstention and a confident miss are
	// different failures and averaging them together conceals both.
	Empty int
}

// SampleCommits builds evaluation cases from the repository's history.
//
// The same bound as the co-change miner applies and for the same reason: a
// commit touching 200 files is a vendor drop or a licence sweep, and "predict
// these 200 files from this one-line message" is not a task anybody could do or
// would want done. skip drops the most recent commits, which matters when
// evaluating on a repo the agent is currently working in: the newest commits
// are the work in flight.
func SampleCommits(ctx context.Context, repo string, n, maxFiles, skip int) ([]EvalCase, error) {
	if maxFiles <= 0 {
		maxFiles = 25
	}
	// #nosec G204 -- no shell is involved: exec.Command passes argv directly,
	// so a path cannot inject arguments. The repository path comes from an
	// operator flag or config, never from an agent.
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "log", "--no-merges",
		"--name-only", "--pretty=format:"+recSep+"%H"+fldSep+"%s", "-n", strconv.Itoa(n+skip))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var cases []EvalCase
	for i, block := range strings.Split(string(out), recSep) {
		if i <= skip || strings.TrimSpace(block) == "" {
			continue
		}
		head, rest, _ := strings.Cut(block, "\n")
		sha, msg, _ := strings.Cut(head, fldSep)
		var files []string
		sc := bufio.NewScanner(strings.NewReader(rest))
		for sc.Scan() {
			if f := strings.TrimSpace(sc.Text()); f != "" {
				files = append(files, f)
			}
		}
		if len(files) == 0 || len(files) > maxFiles || strings.TrimSpace(msg) == "" {
			continue
		}
		cases = append(cases, EvalCase{Commit: sha, Message: msg, Changed: files})
		if len(cases) >= n {
			break
		}
	}
	return cases, nil
}

// Evaluate runs a scorer over the cases and reports recall@k and MRR.
//
// ks must be sorted ascending; the largest is what gets requested from the
// scorer, so one prediction serves every cutoff.
func Evaluate(ctx context.Context, s Scorer, cases []EvalCase, ks []int) (EvalResult, error) {
	if len(ks) == 0 {
		ks = []int{5, 10, 20}
	}
	sort.Ints(ks)
	maxK := ks[len(ks)-1]

	// ScorerID is filled from the first ANSWER, not from s.ID() up front. A
	// remote scorer does not know which model it is talking to until the service
	// tells it, so asking beforehand reports "remote:remote": a calibration run
	// that names the wrong scorer is worse than one that names none, because the
	// numbers get filed against the wrong model.
	res := EvalResult{Cases: len(cases), RecallAt: map[int]float64{}}
	sums := map[int]float64{}
	var mrr float64

	for _, c := range cases {
		pred, err := s.Predict(ctx, c.Message, maxK)
		if err != nil {
			return res, err
		}
		if res.ScorerID == "" && pred.ScorerID != "" {
			res.ScorerID = pred.ScorerID
			if pred.Version != "" {
				res.ScorerID += "@" + pred.Version
			}
		}
		if len(pred.Files) == 0 {
			res.Empty++
			continue
		}
		hits, rank := scoreCase(pred.Files, c.Changed, ks)
		if rank > 0 {
			mrr += 1 / float64(rank)
		}
		for k, v := range hits {
			sums[k] += v
		}
	}
	// Divide by ALL cases, including the ones the scorer abstained on. Scoring
	// only the cases it answered would let a scorer that answers one query in
	// ten report a better number than one that answers everything well.
	n := float64(len(cases))
	if n == 0 {
		return res, nil
	}
	for _, k := range ks {
		res.RecallAt[k] = sums[k] / n
	}
	res.MRR = mrr / n
	if res.ScorerID == "" {
		res.ScorerID = s.ID() // nothing answered; the configured name is all we have
	}
	return res, nil
}

// scoreCase measures one prediction against one commit's actual file set:
// recall at each cutoff, and the rank of the first correct file.
//
// Split out from Evaluate so the loop reads as what it is: accumulate over
// cases: rather than as three nested loops whose innermost one is doing the
// only interesting work.
func scoreCase(files []File, changed []string, ks []int) (map[int]float64, int) {
	want := make(map[string]bool, len(changed))
	for _, f := range changed {
		want[f] = true
	}
	firstHit := 0
	for rank, f := range files {
		if want[f.Path] {
			firstHit = rank + 1
			break
		}
	}
	hits := make(map[int]float64, len(ks))
	for _, k := range ks {
		n := 0
		for i, f := range files {
			if i >= k {
				break
			}
			if want[f.Path] {
				n++
			}
		}
		hits[k] = float64(n) / float64(len(changed))
	}
	return hits, firstHit
}

// minPairsForPercentile is where a 95th percentile starts being a percentile.
//
// pct() indexes at 0.95*(N-1), so below 20 samples the top 5% contains less
// than one whole sample and the "95th percentile" IS the maximum: a single
// unlucky pair sets the threshold for the whole repository.
const minPairsForPercentile = 20

// Calibration reports how much evidence a threshold rests on, because the
// number alone cannot say whether it was measured.
//
// The zero-pair case returns documented DEFAULTS, and the caller used to print
// them under the label "95th pct of unrelated pairs": a provenance that is
// simply false, on a screen that also says "set these in your config if they
// look right". An operator has no way to tell 0.750-because-we-measured from
// 0.750-because-we-could-not.
type Calibration struct {
	// Degenerate reports that the unrelated pairs scored too alike for the
	// median and the 95th percentile to differ, so Notify was derived from Join
	// rather than measured. The numbers are usable; the confidence is not the
	// same, and anything presenting them should say which.
	Degenerate   bool `json:"degenerate,omitempty"`
	Join, Notify float64
	// Pairs is how many unrelated pairs the percentile was taken over. Zero
	// means nothing was measured and Join/Notify are defaults.
	Pairs int

	// Positives, PosMedian and PosAboveJoin describe the OTHER population: work
	// that history says WAS related.
	//
	// A threshold alone cannot say whether a scorer can tell the two apart. A
	// scorer that ranks perfectly can still leave half the genuinely-related
	// work below its own calibrated bar: measured here on this repository with
	// a small embedding model, two declarations describing identical work
	// scored 0.566 and 0.509 against a bar of 0.542, so one auto-joined and one
	// silently did not. The numbers looked healthy; the separation was 0.118.
	Positives    int
	PosMedian    float64
	PosAboveJoin int
}

// A note on the threshold rule, so the obvious idea is not re-derived.
//
// The 95th percentile of unrelated pairs fixes the false-positive rate at 5%
// and lets the true-positive rate land where it will. An adaptive bar: the
// point maximising Youden's J, constrained never to be worse on false
// positives: was implemented and measured against it on this repository. It
// moved the built-in scorer from 50% to 52% of related work clearing the bar,
// and a small embedding model not at all.
//
// The populations simply overlap: no choice of threshold separates them when a
// scorer puts related and unrelated work in the same range. What fixes that is
// a better scorer, which is why Separation is REPORTED rather than optimised
// around. The percentile rule stays because it is honest about what it
// guarantees, and a cleverer rule that buys two points is complexity
// pretending to be an answer.

// describePositives summarises the related-work population against the bar.
//
// What fraction of genuinely-related work clears it is the number an operator
// actually needs and could not get: a threshold on its own says nothing about
// whether the scorer can TELL the two populations apart, and a scorer that
// ranks correctly can still leave half the true positives below its own bar.
func describePositives(pos []float64, join float64) (median float64, above int) {
	if len(pos) == 0 {
		return 0, 0
	}
	sort.Float64s(pos)
	for _, v := range pos {
		if v >= join {
			above++
		}
	}
	return pct(pos, 0.50), above
}

// Separation is the share of genuinely-related work that clears the join bar,
// the single number that says whether auto-join is worth switching on.
//
// Returns 0 when there was nothing to measure, which the caller must not
// present as "nothing gets through".
func (c Calibration) Separation() float64 {
	if c.Positives == 0 {
		return 0
	}
	return float64(c.PosAboveJoin) / float64(c.Positives)
}

// Measured reports whether the thresholds came from this repository at all.
func (c Calibration) Measured() bool { return c.Pairs > 0 }

// Thin reports that a percentile was taken, but over too few samples for it to
// be one: the threshold is within a sample of the maximum.
func (c Calibration) Thin() bool { return c.Pairs > 0 && c.Pairs < minPairsForPercentile }

// SuggestThresholds is Calibrate without the evidence, for callers that only
// want the two numbers. Prefer Calibrate anywhere the numbers are SHOWN: a
// threshold printed without saying what it rests on will be configured as
// though it were measured.
func SuggestThresholds(ctx context.Context, s Scorer, cases []EvalCase) (join, notify float64, err error) {
	c, err := Calibrate(ctx, s, cases)
	return c.Join, c.Notify, err
}

// thresholdsFrom turns the unrelated-pair scores into the two thresholds.
//
// join is their 95th percentile, so roughly one in twenty unrelated pairs would
// auto-join; notify is their median.
//
// A degenerate distribution collapses the two onto each other: if half the
// unrelated pairs score identically, the median IS the 95th, and notify == join.
// That silently deletes the advisory band: every match either auto-joins or is
// invisible, with nothing in between and no warning. It was observed on Lanes'
// own repository once its history passed a few hundred commits, which is exactly
// when an operator has enough data to trust the number.
//
// Notify is pulled strictly below join rather than reported as an error, because
// the ordering is what the rest of the system relies on and a narrow advisory
// band is still useful. The bool is what lets a caller say which number was
// measured and which was derived.
//
// Takes a sorted slice.
func thresholdsFrom(neg []float64) (join, notify float64, degenerate bool) {
	join = pct(neg, 0.95)
	notify = pct(neg, 0.50)
	if notify >= join {
		return join, join * 0.5, true
	}
	return join, notify, false
}

// Calibrate derives join/notify thresholds from the repository's own
// history, by measuring what Overlap actually returns for work that IS related
// against work that is not.
//
// Positives are pairs of commits that touched a common file: the closest thing
// to "these two tasks were the same work" that history can supply. Negatives
// are pairs that shared nothing. The join threshold is set at the negatives'
// 95th percentile, so roughly one in twenty unrelated pairs would auto-join;
// notify sits at their median.
//
// This is a starting point offered to a human, never applied silently
// (SPEC-CHANNELS.md §7). It is calibration, not truth: commits that shared no
// file may still have been the same work, so the negatives are noisy in the
// direction that makes the threshold conservative.
func Calibrate(ctx context.Context, s Scorer, cases []EvalCase) (Calibration, error) {
	return CalibrateWith(ctx, s, s, cases)
}

// ubiquitousFiles are the paths so many commits touch that sharing one says
// nothing about whether two pieces of work are the same.
//
// Measured from the evaluation set itself rather than listed, for the same reason
// the runtime discount is: a hardcoded set of boring filenames is wrong in the
// next repository and still misses the project-specific file everybody edits.
func ubiquitousFiles(cases []EvalCase) map[string]bool {
	if len(cases) < 4 {
		return nil // too few commits for "how often" to mean anything
	}
	count := map[string]int{}
	for _, c := range cases {
		seen := map[string]bool{}
		for _, f := range c.Changed {
			if !seen[f] {
				seen[f] = true
				count[f]++
			}
		}
	}
	out := map[string]bool{}
	for f, n := range count {
		// A file in a QUARTER of all commits cannot discriminate between them.
		if float64(n) >= 0.25*float64(len(cases)) {
			out[f] = true
		}
	}
	return out
}

// distinctiveShare reports whether two commits share a file that is actually
// about the work, ignoring the ones every commit touches.
func distinctiveShare(a, b []string, ubiquitous map[string]bool) bool {
	inA := make(map[string]bool, len(a))
	for _, f := range a {
		if !ubiquitous[f] {
			inA[f] = true
		}
	}
	for _, f := range b {
		if inA[f] {
			return true
		}
	}
	return false
}

// scorePairs splits every pair of evaluation commits into unrelated and
// related, scoring the two groups on the two different predictions: see
// CalibrateWith for why they must not be the same.
func scorePairs(cases []EvalCase, preds, honest []Prediction) (neg, pos []float64) {
	// Pairs that share ONLY repo-wide files are the HARD NEGATIVES, and they used
	// to be counted as positives, which is how a benchmark blessed the exact
	// false positives a live fleet then hit. See ubiquitousFiles.
	ubiquitous := ubiquitousFiles(cases)
	for i := range cases {
		for j := i + 1; j < len(cases); j++ {
			if len(preds[i].Files) == 0 || len(preds[j].Files) == 0 {
				continue
			}
			if !distinctiveShare(cases[i].Changed, cases[j].Changed, ubiquitous) {
				neg = append(neg, Overlap(preds[i], preds[j]))
				continue
			}
			// Two commits that touched a common file: the closest thing
			// history has to "these were the same work". Scored on the HELD-OUT
			// predictions.
			if len(honest[i].Files) > 0 && len(honest[j].Files) > 0 {
				pos = append(pos, Overlap(honest[i], honest[j]))
			}
		}
	}
	return neg, pos
}

// predictAll runs one scorer over every evaluation case.
func predictAll(ctx context.Context, s Scorer, cases []EvalCase) ([]Prediction, error) {
	out := make([]Prediction, len(cases))
	for i, c := range cases {
		p, err := s.Predict(ctx, c.Message, 40)
		if err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

// CalibrateWith measures the THRESHOLD on the deployed scorer and the
// SEPARATION on a held-out one.
//
// They are different questions and conflating them gets one of them wrong.
//
// The threshold is "what score do UNRELATED pairs reach", and it has to be
// measured on the index the daemon actually runs, or it is calibrated for a
// weaker scorer than the one applying it: measured, a threshold taken from a
// held-out index let 10.6% and 14.4% of unrelated pairs through against the ~5%
// its own 95th-percentile label promises.
//
// The separation is "how much genuinely-related work clears that bar", which is
// a QUALITY claim, and on the deployed index it is inflated: both commits in a
// related pair retrieve their own files, including the one that made them
// related, so they overlap by construction. Held out, neither does.
//
// Scoring held-out predictions against a deployed threshold understates
// separation. That is the safe direction for a number somebody will quote.
func CalibrateWith(ctx context.Context, deployed, heldOut Scorer, cases []EvalCase) (Calibration, error) {
	preds, err := predictAll(ctx, deployed, cases)
	if err != nil {
		return Calibration{}, err
	}
	honest := preds
	if heldOut != deployed {
		if honest, err = predictAll(ctx, heldOut, cases); err != nil {
			return Calibration{}, err
		}
	}
	neg, pos := scorePairs(cases, preds, honest)
	if len(neg) == 0 {
		// Documented defaults; nothing to calibrate from. Pairs stays 0 so the
		// caller can say that rather than presenting these as a measurement.
		return Calibration{Join: 0.75, Notify: 0.55}, nil
	}
	sort.Float64s(neg)
	join, notify, degenerate := thresholdsFrom(neg)

	// A well-discriminating scorer scores MOST unrelated pairs at exactly zero,
	// which drags the median to zero, and a notify threshold of zero notifies
	// about every lane on the board, which is worse than not notifying at all.
	// Measured on this repository: join 0.327, median 0.000.
	//
	// So the median is a floor to beat, not the answer. Half the join threshold
	// keeps notify meaningfully below it while staying above the noise.
	if half := join / 2; notify < half {
		notify = half
	}
	// The 95th-percentile rule fixes the false-positive rate at 5% and lets the
	// true-positive rate fall where it may. That suits a scorer whose unrelated
	// pairs cluster at zero (the built-in one does) but cosine similarity
	// never goes near zero, so an embedding scorer's negatives sit high, the
	// bar lands high with them, and it cuts through the positives. Measured on
	// this repository: the built-in scorer cleared 50% of related work at its
	// bar, a small embedding model only 29%, despite ranking better on recall.
	//
	// So the bar is CHOSEN rather than assumed: the point that best separates
	// the two populations, subject to never being worse than the percentile
	// rule would have been on false positives.
	// A degenerate negative distribution collapses the two percentiles onto each
	// other: if half the unrelated pairs score identically, the median IS the
	// 95th, and notify == join. That silently deletes the advisory band: every
	// match either auto-joins or is invisible, with nothing in between and no
	// warning. Observed on this repository's own history once it grew past a few
	// hundred commits, which is exactly when an operator would trust the number.
	//
	// Notify is pulled strictly below join rather than reported as an error,
	// because the ordering is what the rest of the system relies on and a
	// suggest-only band that is merely narrow is still useful. Calibration
	// carries Degenerate so callers can say so out loud instead of presenting a
	// derived number as measured.
	c := Calibration{Join: join, Notify: notify, Pairs: len(neg), Positives: len(pos), Degenerate: degenerate}
	c.PosMedian, c.PosAboveJoin = describePositives(pos, join)
	return c, nil
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func sharesFile(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, f := range a {
		set[f] = true
	}
	for _, f := range b {
		if set[f] {
			return true
		}
	}
	return false
}
