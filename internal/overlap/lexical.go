package overlap

import (
	"context"
	"math"
	"os/exec"
	"strings"
	"sync"
	"unicode"
)

// Lexical is the tier-0 scorer: match the declaration's words against the
// repository's own file paths, then expand through co-change history.
//
// The two halves cover each other's blind spot, which is the point of pairing
// them. Token matching finds what the agent named and is blind to everything it
// did not; co-change finds what the named files drag along and is blind to work
// on files with no history. Together they answer "what will this touch" without
// a model, a download or a network: the floor that has to be useful because it
// is what runs on the day the sidecar is not configured.
//
// Nothing here is a substitute for tier 2. It relates work that shares words or
// history, and cannot relate work that shares neither. SPEC-CHANNELS.md §10.1
// is the governing rule: a low score is not proof that two agents will not
// collide.
type Lexical struct {
	cc *CoChange

	mu    sync.RWMutex
	files []string
	// terms[t] is the set of file indices whose path contains term t.
	terms map[string][]int
	idf   map[string]float64
	// history[t] is the set of file indices that commits MENTIONING term t
	// touched: the project describing its own work, which is the only evidence
	// tier 0 has for a declaration that names no file.
	history map[string][]int
	histIDF map[string]float64
}

// scoreHistory adds the declaration's match against how the project describes
// its work, and reports which terms carried it.
//
// Additive rather than a fallback: a declaration that names a file AND uses the
// project's vocabulary should score on both. Discounted because the evidence is
// weaker: a path match says "this work is about this file", a history match
// says "work described like this has touched this file before", and conflating
// them would let a chatty commit log outvote the filename an agent typed.
func (l *Lexical) scoreHistory(declaration string, scores map[int]float64) []string {
	type hit struct {
		term    string
		posting []int
	}
	var hits []hit
	for t := range tokenize(declaration) {
		if actionWords[t] {
			continue
		}
		posting, ok := l.history[t]
		if !ok {
			continue
		}
		// The same repository-describing guard as paths: a word in most commits
		// ("agents", "fix") describes the project, not the work.
		if len(posting) > minCommonTerm && float64(len(posting)) > 0.25*float64(len(l.files)) {
			continue
		}
		hits = append(hits, hit{t, posting})
	}

	// A two-distinct-terms bar was tried here and removed, which is worth
	// recording so nobody adds it back on the same reasoning.
	//
	// It was meant to stop a single low-information word ("wip", "update")
	// dragging a whole commit's file set into a prediction. Measured on four
	// repositories it cost recall (pi-mono 0.336 -> 0.300, opencode 0.246 ->
	// 0.238) and fixed nothing: the pathology it targeted: unrelated pairs
	// reaching an overlap of 1.0 on opencode: is present in the path-only
	// scorer too, so it was never history's to cause or to cure.
	//
	// The real exposure is small repositories with low-information messages,
	// where any evidence source collapses. `dibs calibrate` detects exactly
	// that and says so: a 95th-percentile threshold of 1.0 is it reporting that
	// this repository cannot discriminate, and refusing to recommend auto-join.
	matched := make([]string, 0, len(hits))
	for _, h := range hits {
		matched = append(matched, h.term)
		w := l.histIDF[h.term] * historyWeight
		for _, idx := range h.posting {
			scores[idx] += w
		}
	}
	return matched
}

// buildHistory indexes commit subjects onto the files those commits touched.
func buildHistory(l *Lexical, cc *CoChange, held map[string]bool) {
	if cc == nil {
		return
	}
	byFile := make(map[string]int, len(l.files))
	for i, f := range l.files {
		byFile[f] = i
	}
	for _, c := range cc.Messages {
		if held[c.Subject] {
			continue // held out for evaluation; see NewLexicalHolding
		}
		for t := range tokenize(c.Subject) {
			if actionWords[t] {
				continue
			}
			for _, f := range c.Files {
				// Only files that still exist: history describes a layout that
				// may be years gone, and predicting a deleted path is noise a
				// reader cannot act on.
				if idx, ok := byFile[f]; ok {
					l.history[t] = appendUnique(l.history[t], idx)
				}
			}
		}
	}
	n := float64(len(l.files))
	for t, posting := range l.history {
		// Weighted like paths, from the same file count, so the two indexes are
		// commensurable when their scores are added.
		l.histIDF[t] = math.Log(1 + n/float64(len(posting)))
	}
}

// appendUnique keeps a posting list free of duplicates without sorting it: one
// term appears in many commits and would otherwise carry the same file dozens
// of times, weighting a file by how often it is COMMITTED rather than how well
// it matches.
func appendUnique(list []int, idx int) []int {
	for _, v := range list {
		if v == idx {
			return list
		}
	}
	return append(list, idx)
}

// minCommonTerm is the floor below which a term is never treated as
// repository-describing, however large a fraction of the files it covers.
const minCommonTerm = 4

// historyWeight discounts commit-message evidence against filename evidence.
//
// A path match says "this work is about this file". A history match says "work
// described like this has touched this file before": real, and weaker. At 1.0
// a chatty commit log outvotes the filename an agent actually typed; at 0 the
// index may as well not exist. Set by measurement on this repository with
// `dibs calibrate`, not by taste.
const historyWeight = 0.6

// NewLexical builds the index over the repository's tracked files.
// NewLexical builds the index over the whole repository and its whole history.
func NewLexical(ctx context.Context, repo string, cc *CoChange) (*Lexical, error) {
	return NewLexicalHolding(ctx, repo, cc, nil)
}

// NewLexicalHolding is NewLexical with commits held OUT of the history index.
//
// It exists because measuring this scorer without it is measuring memorisation.
// `dibs calibrate` evaluates by using a commit message as the query and that
// commit's files as the answer, and the history index is built from exactly
// that pairing, so a query retrieves the commit it came from. Adding the index
// took this repository from recall@5 0.288 to 0.815 and MRR 0.476 to a perfect
// 1.000, which is not a result, it is a leak wearing one.
//
// Holding the evaluation commits out restores the question anybody actually
// cares about: does history help on work the index has never seen? That is also
// what production does: index the past, predict the present.
func NewLexicalHolding(ctx context.Context, repo string, cc *CoChange, holdOut []string) (*Lexical, error) {
	held := make(map[string]bool, len(holdOut))
	for _, h := range holdOut {
		held[h] = true
	}
	return newLexical(ctx, repo, cc, held)
}

func newLexical(ctx context.Context, repo string, cc *CoChange, held map[string]bool) (*Lexical, error) {
	// #nosec G204 -- no shell is involved: exec.Command passes argv directly,
	// so a path cannot inject arguments. The repository path comes from an
	// operator flag or config, never from an agent.
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "ls-files").Output()
	if err != nil {
		return nil, err
	}
	l := &Lexical{
		cc: cc, terms: map[string][]int{}, idf: map[string]float64{},
		history: map[string][]int{}, histIDF: map[string]float64{},
	}
	for _, f := range strings.Split(string(out), "\n") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		idx := len(l.files)
		l.files = append(l.files, f)
		for t := range tokenize(f) {
			l.terms[t] = append(l.terms[t], idx)
		}
	}
	// Second index: how this project DESCRIBES its work.
	//
	// Path matching is blind to a declaration that names no file. "fixing the
	// retry loop when tokens fail to refresh" shares no token with any path in a
	// repo whose file is called auth.go, so tier 0 predicted nothing and said
	// so. Honest, and useless to anyone evaluating whether the thing works.
	//
	// A commit subject is a description of work in the project's own words and
	// its files are what that work touched. That pairing is already the ground
	// truth `dibs calibrate` measures against, and the co-change miner already
	// reads both: this uses what was being thrown away.
	buildHistory(l, cc, held)

	n := float64(len(l.files))
	for t, posting := range l.terms {
		// Standard IDF. This is what makes a hand-maintained stopword list for
		// PATHS unnecessary: `internal`, `src` and `go` appear in most files and
		// earn a weight near zero on their own, without anybody deciding they
		// were uninteresting. Terms that name one package keep their weight.
		l.idf[t] = math.Log(1 + n/float64(len(posting)))
	}
	return l, nil
}

// ID names this scorer in recorded provenance, so a membership can say what
// decided it even years later.
func (l *Lexical) ID() string { return "lexical+cochange" }

// Version changes whenever the scoring changes in a way that would move
// thresholds, so a calibration can be invalidated rather than silently misread.
// Version 2 is paths + co-change + commit-message history. Version 1 was paths
// + co-change only.
//
// Bumped because a recorded score is provenance: SPEC-CHANNELS §4.3 says a
// membership records WHICH scorer produced its score so a later reader can tell
// whether two scores are comparable. Adding an evidence source and leaving the
// version at "1" makes a v1 score and a v2 score indistinguishable in the
// ledger forever, and they are not the same measurement.
func (l *Lexical) Version() string { return "2" }

// Files reports how many tracked files were indexed.
func (l *Lexical) Files() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.files)
}

// Predict scores every file by the IDF-weighted terms it shares with the
// declaration, then expands the result through co-change.
func (l *Lexical) Predict(ctx context.Context, declaration string, limit int) (Prediction, error) {
	if limit <= 0 {
		limit = 40
	}
	l.mu.RLock()
	defer l.mu.RUnlock()

	scores := map[int]float64{}
	var matched []string
	for t := range tokenize(declaration) {
		if actionWords[t] {
			continue
		}
		posting, ok := l.terms[t]
		if !ok {
			continue
		}
		// A term matching most of the repository is describing the repository,
		// not the work. Skipping it outright is cheaper than letting IDF shrink
		// it and still paying to walk a posting list the length of the tree.
		//
		// The absolute floor is what makes this safe on a SMALL repository, and
		// it is not a nicety: at three files the ratio alone is 0.75, so a term
		// appearing in a single file already "matches most of the repo" and gets
		// dropped. Every term was discarded and tier 0 predicted nothing at all,
		// silently, because an empty prediction is a legitimate answer meaning
		// "no evidence". Found by a live fleet run over a three-file fixture,
		// where two real agents declared plainly overlapping work and neither was
		// matched. A 121-file repository never trips it, which is why every
		// calibration to date looked healthy.
		if len(posting) > minCommonTerm && float64(len(posting)) > 0.25*float64(len(l.files)) {
			continue
		}
		matched = append(matched, t)
		w := l.idf[t]
		for _, idx := range posting {
			scores[idx] += w
		}
	}

	matched = append(matched, l.scoreHistory(declaration, scores)...)
	if len(scores) == 0 {
		// An honest empty answer. Callers must read this as "no evidence", never
		// as "no overlap" (SPEC-CHANNELS.md §10.1).
		return Prediction{ScorerID: l.ID(), Version: l.Version()}, nil
	}

	direct := make([]File, 0, len(scores))
	for idx, s := range scores {
		direct = append(direct, File{Path: l.files[idx], Weight: s})
	}
	direct = topN(direct, limit)

	files := direct
	evidence := []string{"matched terms: " + strings.Join(matched, ", ")}
	if l.cc != nil && l.cc.Commits() > 0 {
		before := len(files)
		files = l.cc.Expand(direct, 0.5, limit)
		if n := len(files) - before; n > 0 {
			evidence = append(evidence, "co-change added "+itoa(n)+" files from "+itoa(l.cc.Commits())+" commits")
		}
	}
	return Prediction{Files: files, ScorerID: l.ID(), Version: l.Version(), Evidence: evidence}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// actionWords are words that describe what is being DONE rather than what it is
// being done to.
//
// This list is short and deliberately not a general English stopword list: IDF
// already handles words that are common in PATHS. These are different: they
// are rare in paths and common in declarations, so IDF scores them as highly
// informative when they are the opposite. "fix the retry loop" should retrieve
// on "retry", and "fix" should not drag in the one file that happens to have
// `fix` in its name.
var actionWords = map[string]bool{
	"add": true, "adds": true, "added": true, "adding": true,
	"fix": true, "fixes": true, "fixed": true, "fixing": true,
	"update": true, "updates": true, "updated": true, "updating": true,
	"remove": true, "removes": true, "removed": true, "removing": true,
	"refactor": true, "refactors": true, "refactored": true, "refactoring": true,
	"implement": true, "implements": true, "implemented": true, "implementing": true,
	"work": true, "working": true, "make": true, "makes": true, "making": true,
	"use": true, "using": true, "support": true, "improve": true, "improving": true,
	"the": true, "and": true, "for": true, "with": true, "into": true, "from": true,
	"that": true, "this": true, "some": true, "new": true, "old": true,
}

// tokenize splits a path or a sentence into comparable lowercase terms.
//
// Handles the three casings that appear in one repository at once,
// `HookPoll`, `hook_poll`, `hook-poll`: because a declaration says "hook poll"
// and has to reach all three. Terms shorter than three characters are dropped:
// they are almost all `go`, `ts`, `js`, `md` extensions and single letters,
// which match everywhere and mean nothing.
func tokenize(s string) map[string]bool {
	out := map[string]bool{}
	var cur []rune
	flush := func() {
		if len(cur) >= 3 {
			out[strings.ToLower(string(cur))] = true
		}
		cur = cur[:0]
	}
	prevLower := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// A lower→upper transition is a camelCase word boundary.
			if prevLower && unicode.IsUpper(r) {
				flush()
			}
			cur = append(cur, r)
			prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		default:
			flush()
			prevLower = false
		}
	}
	flush()
	return out
}
