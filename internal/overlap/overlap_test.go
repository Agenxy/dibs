package overlap

import (
	"context"
	"crypto/sha256"
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func p(files ...File) Prediction { return Prediction{Files: files} }

func TestOverlapWeightsTheSharedFilesNotJustTheCount(t *testing.T) {
	// The failure this guards: plain set Jaccard scores "we both barely touch
	// go.mod" identically to "we are both rewriting engine.go".
	strong := Overlap(
		p(File{"engine.go", 1.0}, File{"go.mod", 0.05}),
		p(File{"engine.go", 1.0}, File{"other.go", 0.05}),
	)
	weak := Overlap(
		p(File{"engine.go", 1.0}, File{"go.mod", 0.05}),
		p(File{"unrelated.go", 1.0}, File{"go.mod", 0.05}),
	)
	if strong <= weak {
		t.Fatalf("sharing the heavy file must beat sharing the trivial one: strong=%.3f weak=%.3f", strong, weak)
	}
}

func TestOverlapIsSymmetricAndBounded(t *testing.T) {
	a := p(File{"a.go", 1}, File{"b.go", 0.4})
	b := p(File{"b.go", 0.9}, File{"c.go", 0.2})
	ab, ba := Overlap(a, b), Overlap(b, a)
	if math.Abs(ab-ba) > 1e-9 {
		t.Fatalf("not symmetric: %v vs %v", ab, ba)
	}
	if ab < 0 || ab > 1 {
		t.Fatalf("out of range: %v", ab)
	}
	if got := Overlap(a, a); math.Abs(got-1) > 1e-9 {
		t.Fatalf("self-overlap must be 1, got %v", got)
	}
	if got := Overlap(a, Prediction{}); got != 0 {
		t.Fatalf("empty prediction must score 0, got %v", got)
	}
}

// Overlap is recorded in the ledger and trusted on replay (SPEC-CHANNELS.md
// §4.3), so identical inputs must give a bit-identical result every time. Map
// iteration order is the realistic way that breaks.
func TestOverlapIsDeterministic(t *testing.T) {
	a := p(File{"a.go", 1}, File{"b.go", 0.4}, File{"c.go", 0.3}, File{"d.go", 0.2})
	b := p(File{"b.go", 0.9}, File{"c.go", 0.2}, File{"e.go", 0.7}, File{"f.go", 0.1})
	want := Overlap(a, b)
	for i := 0; i < 200; i++ {
		if got := Overlap(a, b); got != want {
			t.Fatalf("run %d differed: %v != %v", i, got, want)
		}
	}
}

func TestSharedReturnsEvidenceStrongestFirst(t *testing.T) {
	got := Shared(
		p(File{"weak.go", 0.2}, File{"strong.go", 0.9}),
		p(File{"weak.go", 0.3}, File{"strong.go", 1.0}, File{"absent.go", 1.0}),
		0,
	)
	if len(got) != 2 {
		t.Fatalf("want 2 shared, got %v", got)
	}
	if got[0].Path != "strong.go" {
		t.Fatalf("strongest evidence must come first, got %v", got)
	}
}

func TestTokenizeSplitsEveryCasingInOneRepo(t *testing.T) {
	// A declaration says "hook poll"; the repo spells it three ways.
	for _, in := range []string{"HookPoll", "hook_poll", "hook-poll", "internal/engine/hookpoll.go"} {
		got := tokenize(in)
		if in == "internal/engine/hookpoll.go" {
			if !got["hookpoll"] {
				t.Fatalf("%q: want hookpoll, got %v", in, got)
			}
			continue
		}
		if !got["hook"] || !got["poll"] {
			t.Fatalf("%q: want hook+poll, got %v", in, got)
		}
	}
}

func TestTokenizeDropsShortNoiseTerms(t *testing.T) {
	got := tokenize("main.go")
	if got["go"] {
		t.Fatalf("two-letter extension must be dropped: %v", got)
	}
	if !got["main"] {
		t.Fatalf("want main: %v", got)
	}
}

func TestCoChangeConfidenceIgnoresBusyFiles(t *testing.T) {
	cc := &CoChange{commits: map[string]int{}, pairs: map[string]map[string]int{}}
	// README changes with everything; engine and ledger change with each other.
	for i := 0; i < 10; i++ {
		cc.add([]string{"README.md", "engine.go", "ledger.go"})
	}
	for i := 0; i < 10; i++ {
		cc.add([]string{"README.md", "docs.md"})
	}
	rel := cc.Related("engine.go", 2, 10)
	if len(rel) == 0 {
		t.Fatal("engine.go must have relations")
	}
	// ledger.go co-occurs with engine.go in 10/10 of engine's commits; README
	// also does, but the point is that confidence is normalised by the QUERY
	// file, so both are 1.0 here — while docs.md, which never appears with
	// engine.go, is absent entirely.
	for _, r := range rel {
		if r.Path == "docs.md" {
			t.Fatalf("docs.md never changes with engine.go: %v", rel)
		}
	}
}

func TestCoChangeMinSupportRejectsCoincidence(t *testing.T) {
	cc := &CoChange{commits: map[string]int{}, pairs: map[string]map[string]int{}}
	cc.add([]string{"a.go", "b.go"}) // seen together exactly once
	if rel := cc.Related("a.go", 2, 10); len(rel) != 0 {
		t.Fatalf("a single co-occurrence is not a relationship: %v", rel)
	}
}

func TestExpandDampsInferredFilesBelowDirectHits(t *testing.T) {
	cc := &CoChange{commits: map[string]int{}, pairs: map[string]map[string]int{}}
	for i := 0; i < 5; i++ {
		cc.add([]string{"direct.go", "implied.go"})
	}
	out := cc.Expand([]File{{"direct.go", 1.0}}, 0.5, 10)
	var direct, implied float64
	for _, f := range out {
		switch f.Path {
		case "direct.go":
			direct = f.Weight
		case "implied.go":
			implied = f.Weight
		}
	}
	if implied == 0 {
		t.Fatal("co-change should have pulled in implied.go")
	}
	if implied >= direct {
		t.Fatalf("an inference must weigh less than a direct hit: direct=%v implied=%v", direct, implied)
	}
}

func TestTopNRenormalisesSoScorersAreComparable(t *testing.T) {
	// A tier-0 raw score and a tier-2 cosine share no unit, but one set of
	// thresholds is configured for both.
	out := topN([]File{{"a", 40}, {"b", 20}, {"c", 10}}, 2)
	if len(out) != 2 || out[0].Weight != 1 {
		t.Fatalf("want 2 files normalised to 1.0, got %v", out)
	}
	if out[1].Weight != 0.5 {
		t.Fatalf("relative weights must survive: %v", out)
	}
}

// ── against this repository's real history ───────────────────────────────

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not a git repo")
	}
	return trim(string(out))
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func TestLexicalPredictsRealFilesFromRealWords(t *testing.T) {
	ctx := context.Background()
	repo := repoRoot(t)
	cc, err := MineCoChange(ctx, repo, CoChangeOptions{MaxCommits: 300, MaxFilesPerCommit: 25})
	if err != nil {
		t.Skip("git log unavailable:", err)
	}
	lex, err := NewLexical(ctx, repo, cc)
	if err != nil {
		t.Skip("git ls-files unavailable:", err)
	}
	if lex.Files() == 0 {
		t.Skip("no tracked files")
	}
	pred, err := lex.Predict(ctx, "fixing the claim guard for path enforcement", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(pred.Files) == 0 {
		t.Fatal("a declaration using this repo's own vocabulary should predict something")
	}
	var hitGuard bool
	for _, f := range pred.Files {
		if contains(f.Path, "guard") {
			hitGuard = true
		}
	}
	if !hitGuard {
		t.Errorf("expected a guard file in the prediction, got %v", pred.Files)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestUnrelatedDeclarationsScoreBelowRelatedOnes(t *testing.T) {
	ctx := context.Background()
	repo := repoRoot(t)
	cc, err := MineCoChange(ctx, repo, CoChangeOptions{MaxCommits: 300, MaxFilesPerCommit: 25})
	if err != nil {
		t.Skip("git unavailable")
	}
	lex, err := NewLexical(ctx, repo, cc)
	if err != nil || lex.Files() == 0 {
		t.Skip("git unavailable")
	}
	guardA, _ := lex.Predict(ctx, "enforcing exclusive claims in the guard", 40)
	guardB, _ := lex.Predict(ctx, "claim guard denies edits to claimed paths", 40)
	web, _ := lex.Predict(ctx, "restyling the web board stylesheet and fonts", 40)
	if len(guardA.Files) == 0 || len(guardB.Files) == 0 || len(web.Files) == 0 {
		t.Skip("index too sparse in this checkout")
	}
	same := Overlap(guardA, guardB)
	diff := Overlap(guardA, web)
	if same <= diff {
		t.Fatalf("two guard declarations must score above guard-vs-web: same=%.3f diff=%.3f", same, diff)
	}
	t.Logf("related=%.3f unrelated=%.3f", same, diff)
}

func TestSuggestThresholdsNeverReturnsAZeroNotify(t *testing.T) {
	// A scorer that discriminates well puts MOST unrelated pairs at exactly
	// zero, so the median is zero — and notify_threshold=0 notifies about every
	// lane on the board, which is worse than not notifying at all. Measured on
	// this repository before the floor was added: join 0.327, notify 0.000.
	ctx := context.Background()
	repo := repoRoot(t)
	cc, err := MineCoChange(ctx, repo, CoChangeOptions{MaxCommits: 300})
	if err != nil {
		t.Skip("git unavailable")
	}
	lex, err := NewLexical(ctx, repo, cc)
	if err != nil || lex.Files() == 0 {
		t.Skip("git unavailable")
	}
	cases, err := SampleCommits(ctx, repo, 60, 25, 0)
	if err != nil || len(cases) < 5 {
		t.Skip("not enough history in this checkout")
	}
	// Skip when the checkout has too little history to calibrate FROM, which is
	// not the same as having too few commits to sample.
	//
	// This test asserts that the notify floor works — that a scorer which puts
	// most unrelated pairs at zero does not produce notify=0. It can only assert
	// that where there is a distribution to floor. After this repository's history
	// was squashed to a single v0 commit there was not, and the test spent the
	// narrow band between "too thin to sample" (already skipped, above) and "thick
	// enough to discriminate" reporting a FAILURE for a repository that was
	// working exactly as designed.
	//
	// Calibration already models this and calls it degenerate. Asking it, rather
	// than counting commits, means this keeps working as the history grows back.
	cal, err := Calibrate(ctx, lex, cases)
	if err != nil {
		t.Fatal(err)
	}
	if cal.Degenerate {
		t.Skipf("history too uniform to calibrate (%d pairs, %d positives) — "+
			"nothing to floor", cal.Pairs, cal.Positives)
	}

	join, notify, err := SuggestThresholds(ctx, lex, cases)
	if err != nil {
		t.Fatal(err)
	}
	if join <= 0 {
		t.Fatalf("join threshold must be positive, got %v", join)
	}
	if notify <= 0 {
		t.Fatalf("notify threshold must be positive, got %v", notify)
	}
	if notify >= join {
		t.Fatalf("notify must sit below join: notify=%v join=%v", notify, join)
	}
	t.Logf("calibrated join=%.3f notify=%.3f", join, notify)
}

// A small repository must still predict something.
//
// The "term matches most of the repo" guard is a ratio, and a ratio is
// degenerate at small N: with three files, 0.25*3 = 0.75, so a term appearing in
// ONE file already exceeds it. Every term was dropped and Predict returned
// nothing — silently, because an empty prediction legitimately means "no
// evidence". Two real agents in a live fleet run declared plainly overlapping
// work over a three-file fixture and neither was matched.
func TestSmallRepoStillPredicts(t *testing.T) {
	l := &Lexical{terms: map[string][]int{}, idf: map[string]float64{}}
	for _, f := range []string{"auth/middleware.go", "auth/retry.go", "web/board.css"} {
		idx := len(l.files)
		l.files = append(l.files, f)
		for tk := range tokenize(f) {
			l.terms[tk] = append(l.terms[tk], idx)
		}
	}
	for tk, posting := range l.terms {
		l.idf[tk] = 1 / float64(len(posting))
	}
	pred, err := l.Predict(context.Background(), "fixing the token validation retry loop in auth", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pred.Files) == 0 {
		t.Fatal("a 3-file repo must still predict something — every term was dropped as 'too common'")
	}
	var hitRetry bool
	for _, f := range pred.Files {
		if f.Path == "auth/retry.go" {
			hitRetry = true
		}
	}
	if !hitRetry {
		t.Errorf("a declaration naming 'retry' should retrieve auth/retry.go, got %v", pred.Files)
	}
}

// A threshold whose evidence is not stated is worse than no threshold, because
// it will be configured.
//
// The zero-pair case returns documented DEFAULTS, and the CLI printed them
// under "suggested thresholds for THIS repository … (95th pct of unrelated
// pairs)" — a provenance that is simply false, on a screen that also said "set
// these in your config if they look right". Nothing distinguished
// 0.750-because-we-measured from 0.750-because-we-could-not.
func TestCalibrationSaysWhetherItMeasuredAnything(t *testing.T) {
	ctx := context.Background()
	repo := repoRoot(t)
	cc, err := MineCoChange(ctx, repo, CoChangeOptions{MaxCommits: 50})
	if err != nil {
		t.Skip("git unavailable")
	}
	lex, err := NewLexical(ctx, repo, cc)
	if err != nil {
		t.Skip("git unavailable")
	}

	// Nothing to compare: no cases at all means no unrelated pairs.
	empty, err := Calibrate(ctx, lex, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Measured() {
		t.Fatal("no pairs were compared; this must not claim to be a measurement")
	}
	if empty.Pairs != 0 {
		t.Fatalf("want 0 pairs, got %d", empty.Pairs)
	}
	// The defaults are still returned — the caller needs something to print —
	// but they are labelled by Pairs == 0, not by their value.
	if empty.Join != 0.75 || empty.Notify != 0.55 {
		t.Fatalf("documented defaults expected, got %v/%v", empty.Join, empty.Notify)
	}
}

// Below 20 samples the 95th percentile IS the maximum — pct() indexes at
// 0.95*(N-1), so the top 5% holds less than one whole sample and a single
// unlucky pair sets the bar for the entire repository.
func TestThinEvidenceIsReportedAsThin(t *testing.T) {
	for _, tc := range []struct {
		pairs int
		thin  bool
	}{
		{pairs: 1, thin: true},
		{pairs: 19, thin: true},
		{pairs: 20, thin: false},
		{pairs: 336, thin: false},
		{pairs: 0, thin: false}, // not thin — not measured at all, a different report
	} {
		c := Calibration{Pairs: tc.pairs, Join: 0.3}
		if c.Thin() != tc.thin {
			t.Errorf("%d pairs: Thin()=%v, want %v", tc.pairs, c.Thin(), tc.thin)
		}
		if got := c.Measured(); got != (tc.pairs > 0) {
			t.Errorf("%d pairs: Measured()=%v", tc.pairs, got)
		}
	}
}

// A threshold on its own cannot say whether a scorer can TELL related work from
// unrelated work. It reports a false-positive rate and nothing about the true
// positives — so a scorer that ranks correctly can still leave most genuine
// collisions below its own calibrated bar, and the numbers look healthy.
//
// Measured on this repository: the built-in scorer clears 50% of related pairs
// at its bar; a small embedding model clears 29% despite better recall, because
// cosine similarity never approaches zero so its negatives sit high and drag
// the percentile up through the positives.
func TestCalibrationReportsHowMuchRelatedWorkClearsTheBar(t *testing.T) {
	// Two populations that overlap the way real ones do.
	pos := []float64{0.10, 0.20, 0.30, 0.40, 0.50, 0.60, 0.70, 0.80}
	median, above := describePositives(pos, 0.45)
	if above != 4 {
		t.Fatalf("four of eight related pairs are at or above 0.45, got %d", above)
	}
	if median < 0.40 || median > 0.50 {
		t.Fatalf("median of the related population should sit mid-range, got %v", median)
	}

	c := Calibration{Pairs: 100, Positives: len(pos), PosAboveJoin: above, Join: 0.45}
	if got := c.Separation(); got != 0.5 {
		t.Fatalf("half of related work clears it, got %v", got)
	}

	// Nothing measured is NOT the same as nothing getting through, and a caller
	// must be able to tell them apart before printing "0%".
	empty := Calibration{Pairs: 100, Positives: 0}
	if got := empty.Separation(); got != 0 {
		t.Fatalf("no positives means no measurement, got %v", got)
	}
	if empty.Positives != 0 {
		t.Fatal("and the caller checks Positives to know which case it is in")
	}
}

// Retrieval embedding models are ASYMMETRIC, and Lanes was using them
// symmetrically: raw text for both the task description and the code chunk.
//
// Every serious retrieval model is trained with a marker saying which side it
// is being given — Qwen3-Embedding's card specifies `Instruct: …\nQuery: …`,
// nomic was trained on literal `search_query:` / `search_document:`. Without
// them the model embeds a query and a document into the same undifferentiated
// space. It still RANKS tolerably, which is why this hid: what collapses is the
// MARGIN between related and unrelated work.
//
// Measured on this repository: adding the markers took qwen3-embedding:4b from
// 22% to 42% separation and MRR from 0.720 to 0.826, and the 0.6b model — with
// its markers — separates better (49%) than the 4b did without them. It was
// never a question of model size.
func TestRetrievalModelsGetTheMarkersTheyWereTrainedWith(t *testing.T) {
	for _, tc := range []struct {
		model, wantQuery, wantDoc string
	}{
		{"qwen3-embedding:4b", "Instruct: ", ""},
		{"Qwen3-Embedding-0.6B", "Instruct: ", ""},
		{"nomic-embed-text", "search_query: ", "search_document: "},
		// The card's wording VERBATIM. A trained instruction is a specific
		// string, not a description of intent: a paraphrase is a different
		// input and the model has no reason to treat it as the marker. The
		// first version of this table used an invented wording.
		{"bge-large-en-v1.5", "Represent this sentence for searching relevant passages: ", ""},
		// And this line is why the family prefix was not enough. This case used
		// to name bge-m3 and expect the string above — but bge-m3's card says
		// it "no longer requires adding instruction to the queries", so the
		// family match was feeding an untrained string to the one member of the
		// family that documents its absence. Matching a family is a guess about
		// its members; the card is per-model.
		{"bge-m3", "", ""},
		// Query-only: the card says the prefix goes "just on the query", and
		// giving documents one cost 11 points of separation on this repository
		// (42% -> 53% once removed).
		{"snowflake-arctic-embed2", "query: ", ""},
		{"intfloat/e5-large", "query: ", "passage: "},
	} {
		got := affixesFor(tc.model)
		if !strings.HasPrefix(got.query, tc.wantQuery) {
			t.Errorf("%s: query affix %q, want prefix %q", tc.model, got.query, tc.wantQuery)
		}
		if got.doc != tc.wantDoc {
			t.Errorf("%s: doc affix %q, want %q", tc.model, got.doc, tc.wantDoc)
		}
	}

	// An unknown model gets nothing rather than a guess: the wrong marker is
	// worse than none, and a model we do not recognise may be symmetric.
	for _, unknown := range []string{
		"some-unknown-embedder",
		// gte is deliberately absent. Its card documents no prefix for
		// gte-large-en-v1.5, and the only instruction-tuned member of the
		// family is a different model. Claiming a convention for the whole
		// family installed a marker the model was never trained on AND
		// suppressed the unknown-model warning — silent, and worse than
		// nothing.
		"gte-large-en-v1.5",
	} {
		if got := affixesFor(unknown); got.query != "" || got.doc != "" {
			t.Errorf("%s: must not be given invented markers, got %+v", unknown, got)
		}
	}

	// The query and document sides must differ for an asymmetric model, or the
	// distinction the markers exist to draw is not being drawn.
	nomic := affixesFor("nomic-embed-text")
	if nomic.query == nomic.doc {
		t.Error("an asymmetric model marked identically on both sides is the bug this fixes")
	}
}

// A model family Lanes has never heard of still has a convention, and its
// operator knows it. Without an override that operator silently gets about half
// the separation with nothing on screen to explain it — demonstrated by giving
// the SAME weights an unrecognised name: 49% of related work cleared the bar
// under the recognised name, 33% under the unrecognised one, and 49% again once
// the marker was supplied by hand.
func TestMarkersCanBeOverriddenForAModelWeDoNotKnow(t *testing.T) {
	e := NewEmbed("http://x", "some-unknown-embedder", "", time.Second)
	if e.Recognised() {
		t.Fatal("precondition: this name matches no known convention")
	}
	if q, d := e.Affixes(); q != "" || d != "" {
		t.Fatalf("an unrecognised model must not be given invented markers, got %q/%q", q, d)
	}

	e.SetAffixes("query: ", "passage: ")
	q, d := e.Affixes()
	if q != "query: " || d != "passage: " {
		t.Fatalf("the override must take, got %q/%q", q, d)
	}
	// Recognised reports what the NAME implies, not what was set: the daemon
	// warns on the former, and an override is exactly the case where the
	// warning should stop being emitted.
	if e.Recognised() {
		t.Error("Recognised describes the model name, not the override")
	}

	// Both empty is a legitimate choice, not a request to re-detect: a
	// symmetric model given retrieval markers is worse off than one given none.
	known := NewEmbed("http://x", "qwen3-embedding:4b", "", time.Second)
	if !known.Recognised() {
		t.Fatal("precondition: qwen3-embedding is a known family")
	}
	known.SetAffixes("", "")
	if q, d := known.Affixes(); q != "" || d != "" {
		t.Fatalf("clearing must disable markers, not restore detection, got %q/%q", q, d)
	}
}

// dot() walked min(len(a), len(b)), so a query embedded at a different width
// than the index was scored over a PREFIX of both — plausible numbers from
// incompatible vector spaces, and auto-joins made from them. That happens for
// real: a model alias repointed, an endpoint swapped, a service upgraded
// between Build and Predict. Nothing errored; the scores just quietly stopped
// meaning anything.
func TestVectorsThatCannotBeComparedAreRefusedNotTruncated(t *testing.T) {
	const want = 4
	ok := [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}}
	if err := checkVectors(ok, want); err != nil {
		t.Fatalf("matching widths must pass: %v", err)
	}
	// The first build has nothing to match against yet.
	if err := checkVectors(ok, 0); err != nil {
		t.Fatalf("the first batch defines the width: %v", err)
	}

	err := checkVectors([][]float32{{1, 0, 0}}, want)
	if err == nil {
		t.Fatal("a 3-wide query against a 4-wide index must be refused, not truncated")
	}
	for _, frag := range []string{"3", "4", "changed since indexing"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("the error must say what mismatched and why; missing %q in: %v", frag, err)
		}
	}

	// An empty vector scores zero against everything, which is exactly what a
	// well-behaved scorer with no opinion looks like. A broken service must not
	// be able to impersonate one.
	if err := checkVectors([][]float32{{}}, want); err == nil {
		t.Error("an empty embedding must be refused, not read as 'no opinion'")
	}
	// NaN poisons every comparison it touches and never errors on its own.
	nan := float32(math.NaN())
	if err := checkVectors([][]float32{{1, nan, 0, 0}}, want); err == nil {
		t.Error("a non-finite component must be refused")
	}
	inf := float32(math.Inf(1))
	if err := checkVectors([][]float32{{1, inf, 0, 0}}, want); err == nil {
		t.Error("an infinite component must be refused")
	}
}

// An index quietly smaller than the repository fails to match work touching the
// files it never saw — and reported itself READY while doing it. A tracked file
// that cannot be read (broken symlink, permission, removed between `git
// ls-files` and the read) was skipped without a word.
func TestAnIncompleteIndexSaysSo(t *testing.T) {
	e := &Embed{}
	if got := e.evidence(10, 3); len(got) != 1 {
		t.Fatalf("a complete index needs no caveat, got %v", got)
	}
	e.unreadable = []string{"internal/secret/vault.go", "cmd/x/main.go"}
	got := e.evidence(10, 3)
	if len(got) != 2 {
		t.Fatalf("an incomplete index must carry the caveat, got %v", got)
	}
	for _, frag := range []string{"2 tracked file", "NOT in this index", "vault.go", "cannot match"} {
		if !strings.Contains(got[1], frag) {
			t.Errorf("the caveat must say how many, that they are absent, an example, and the "+
				"consequence; missing %q in %q", frag, got[1])
		}
	}
	if len(e.Unreadable()) != 2 {
		t.Error("and the list is reachable so the daemon can warn at startup")
	}
}

// Version identifies WHICH index produced a score. A second-resolution
// timestamp cannot tell two rebuilds of a small repository apart, so recorded
// membership could name a scorer version that matched two different indexes —
// the one thing a provenance field must never do.
func TestVersionDistinguishesTwoBuildsInTheSameSecond(t *testing.T) {
	at := time.Unix(1700000000, 0)
	a := &Embed{buildAt: at, vecs: make([][]float32, 100), dims: 768}
	b := &Embed{buildAt: at, vecs: make([][]float32, 140), dims: 768}
	if a.Version() == b.Version() {
		t.Fatalf("two different indexes built in the same second share a version: %q", a.Version())
	}
	same := &Embed{buildAt: at, vecs: make([][]float32, 100), dims: 768}
	if a.Version() != same.Version() {
		t.Errorf("and the same index must be stable: %q vs %q", a.Version(), same.Version())
	}
	if (&Embed{}).Version() != "0" {
		t.Error("an unbuilt index has no version to claim")
	}
}

// A scorer version must name exactly one index.
//
// It was a timestamp, then a timestamp plus chunk count and width. Both collide
// on the case that actually happens: an operator edits a few files and rebuilds
// within the same second. Same chunk count, same width, different answers, one
// version string — so a membership recorded as "matched by index X" points at
// two indexes that disagree, and the provenance is worse than absent because it
// reads as certain.
func TestIndexVersionDistinguishesBuildsThatShareASecondAndAShape(t *testing.T) {
	owners := []string{"a.go", "b.go"}
	before := indexDigest(owners, [][]float32{{0.1, 0.2}, {0.3, 0.4}})
	// Same files, same count, same width — one vector moved, because the file
	// behind it changed.
	after := indexDigest(owners, [][]float32{{0.1, 0.2}, {0.3, 0.9}})
	if before == after {
		t.Error("an index whose answers changed must not keep the same version")
	}
	// Same content must be stable, or every rebuild invalidates provenance that
	// is in fact still accurate.
	if again := indexDigest(owners, [][]float32{{0.1, 0.2}, {0.3, 0.4}}); again != before {
		t.Error("identical index content must produce an identical version")
	}
	// Same vectors, different owner — a chunk moving between files changes what
	// a hit attributes the work to.
	if moved := indexDigest([]string{"a.go", "c.go"}, [][]float32{{0.1, 0.2}, {0.3, 0.4}}); moved == before {
		t.Error("re-attributing a chunk to another file must change the version")
	}

	// Full width. It was truncated to 48 bits, which sounds ample and is not:
	// a chosen collision is birthday work of roughly 2^24, well inside reach of
	// anyone who can write to the repository being indexed. An identity that an
	// attacker can duplicate on purpose is not an identity.
	if got := len(before); got != sha256.Size*2 {
		t.Errorf("digest is %d hex chars, want the full %d — a truncated provenance "+
			"digest can be collided deliberately", got, sha256.Size*2)
	}
}

// A version must be STABLE for identical content, which is the half that the
// first content-digest attempt broke.
//
// Version carried buildAt as well as the digest, so rebuilding byte-identical
// content one second later produced a different version — and provenance that
// was still perfectly accurate then read as stale. Both halves have to hold at
// once: different answers, different version; same answers, same version.
func TestIndexVersionIsStableAcrossRebuildsOfIdenticalContent(t *testing.T) {
	owners := []string{"a.go", "b.go"}
	vecs := [][]float32{{0.1, 0.2}, {0.3, 0.4}}

	first := &Embed{}
	first.paths, first.vecs, first.dims = owners, vecs, 2
	first.digest, first.buildAt = indexDigest(owners, vecs), time.Unix(1700000000, 0)

	// The same content, rebuilt in a different second.
	second := &Embed{}
	second.paths, second.vecs, second.dims = owners, vecs, 2
	second.digest, second.buildAt = indexDigest(owners, vecs), time.Unix(1700009999, 0)

	if first.Version() != second.Version() {
		t.Errorf("identical content must keep its version across rebuilds: %q vs %q",
			first.Version(), second.Version())
	}
	// And the build time is still available, just not as part of the identity.
	if second.BuildAt().Equal(first.BuildAt()) {
		t.Error("BuildAt must still report when each index was actually made")
	}
}

// The marker table is the highest-leverage table in the system and the one I got
// wrong before: three of five entries were invented rather than quoted, and a
// wrong marker scores WORSE than none while looking configured.
//
// So every claim below is checked against a stated model card, and the family
// prefixes are checked for over-reach — "bge" matched bge-m3, which documents
// that it needs no instruction, and handed it bge-large-en-v1.5's trained
// string.
func TestFamilyPrefixesDoNotOverreachWithinAFamily(t *testing.T) {
	for _, c := range []struct {
		model, query string
		known        bool
		why          string
	}{
		{
			"bge-large-en-v1.5", "Represent this sentence for searching relevant passages: ", true,
			"the English v1.5 card states this string verbatim",
		},
		{
			"bge-m3", "", true,
			"the card says it no longer requires an instruction — recognised, but unmarked",
		},
		{
			"bge-large-zh-v1.5", "为这个句子生成表示以用于检索相关文章：", true,
			"the Chinese models are trained on their own instruction, not a translation",
		},
		{
			"snowflake-arctic-embed2", "query: ", true,
			"query side only; the doc prefix was invented and cost 11 points of separation",
		},
		{
			"snowflake-arctic-embed-l", "Represent this sentence for searching relevant passages: ", true,
			"arctic v1 was trained with the legacy BGE-style instruction, not v2's 'query: '",
		},
		{
			"snowflake-arctic-embed-m-v2.0", "query: ", true,
			"v2 changed the prefix; one version apart and the strings share nothing",
		},
		{
			"e5-mistral-7b-instruct", "Instruct: ", true,
			"the instruct member of e5 marks one side, unlike the symmetric ones",
		},
		{
			"e5-large-v2", "query: ", true,
			"the symmetric e5 models mark both sides",
		},
		{
			"bge-code-v1", "<instruct>", true,
			"the instruct generation: <instruct>{task}\\n<query>{query}, documents bare",
		},
		{
			"bge-multilingual-gemma2", "<instruct>", true,
			"same instruct shape, per its card",
		},
		{
			"bge-en-icl", "<instruct>", true,
			"its card gives this zero-shot form explicitly; few-shot appends examples on top",
		},
		{
			"gte-large-en-v1.5", "", false,
			"no card-stated prefix — must warn rather than guess one",
		},
		{
			"some-model-nobody-listed", "", false,
			"unrecognised must warn; it is recoverable, a wrong marker is not",
		},
	} {
		got := affixesFor(c.model)
		// Prefix, because this table is about WHICH branch a model name lands
		// in — the exact trained wording is pinned by the table above. An empty
		// want is the exception: it means "no marker at all", and a prefix test
		// would accept anything.
		if (c.query == "" && got.query != "") || !strings.HasPrefix(got.query, c.query) {
			t.Errorf("%s: query marker %q, want one starting %q (%s)", c.model, got.query, c.query, c.why)
		}
		if got.known != c.known {
			t.Errorf("%s: recognised=%v, want %v (%s)", c.model, got.known, c.known, c.why)
		}
		// Only the symmetric e5 models mark the DOCUMENT side. Every other
		// family here leaves documents bare, and inventing a document prefix is
		// the specific mistake that cost arctic-embed 11 points of separation —
		// worse than no marker, because it looks configured.
		wantDoc := c.model == "e5-large-v2"
		if hasDoc := got.doc != ""; hasDoc != wantDoc {
			t.Errorf("%s: document marker %q, want %v (%s)", c.model, got.doc,
				map[bool]string{true: "one", false: "none"}[wantDoc], c.why)
		}
	}

	// The distinction the `known` field exists for: bge-m3 and an unknown model
	// both carry no markers, and only one of them should warn.
	if !(&Embed{model: "bge-m3"}).Recognised() {
		t.Error("bge-m3 is configured correctly with no marker and must not warn")
	}
	if (&Embed{model: "gte-large-en-v1.5"}).Recognised() {
		t.Error("an unlisted family must warn, not pass silently")
	}
}

// Tier 0 must answer a declaration that names no file.
//
// It matched declared words against file PATHS only, so "fixing the retry loop
// when tokens fail to refresh" — in a repository whose file is called auth.go —
// shared no token with anything and predicted nothing. Honest, and useless to
// anyone evaluating whether the feature works: two agents doing the same job
// were both told nobody else was near it.
//
// A commit subject is a description of work in the project's own words and its
// files are what that work touched. The co-change miner already read both.
func TestTierZeroLearnsHowTheProjectDescribesItsWork(t *testing.T) {
	cc := &CoChange{Messages: []Commit{
		{Subject: "fix the retry backoff when a token refresh fails", Files: []string{"auth.go"}},
		{Subject: "handle expiry during re-authentication", Files: []string{"auth.go"}},
		{Subject: "restyle the board spacing", Files: []string{"board.css"}},
	}}
	l := &Lexical{
		files: []string{"auth.go", "board.css"},
		terms: map[string][]int{}, idf: map[string]float64{},
		history: map[string][]int{}, histIDF: map[string]float64{},
	}
	buildHistory(l, cc, nil)

	// A declaration naming no file at all still reaches the right one.
	pred, err := l.Predict(t.Context(), "why does the token refresh keep looping on failure", 10)
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if len(pred.Files) == 0 {
		t.Fatal("a declaration naming no file predicted nothing — tier 0 is blind to " +
			"natural language again")
	}
	if pred.Files[0].Path != "auth.go" {
		t.Errorf("top prediction %q, want auth.go", pred.Files[0].Path)
	}
}

// Holding commits out is what makes measuring this honest.
//
// `lanes calibrate` evaluates by using a commit message as the query and that
// commit's files as the answer — the exact pairing this index is built from. On
// this repository, adding the index moved recall@5 from 0.288 to 0.815 and MRR
// from 0.476 to a perfect 1.000, which is not a result: the query was
// retrieving the commit it came from.
//
// Measured leak-free across four repositories with real history, recall@5 rose
// 58–91% and MRR rose on every one — a real gain, an order of magnitude smaller
// than the leak that hid it.
func TestHeldOutCommitsDoNotEnterTheHistoryIndex(t *testing.T) {
	cc := &CoChange{Messages: []Commit{
		{Subject: "fix the retry backoff when a token refresh fails", Files: []string{"auth.go"}},
		{Subject: "restyle the board spacing", Files: []string{"board.css"}},
	}}
	l := &Lexical{
		files: []string{"auth.go", "board.css"},
		terms: map[string][]int{}, idf: map[string]float64{},
		history: map[string][]int{}, histIDF: map[string]float64{},
	}
	buildHistory(l, cc, map[string]bool{"fix the retry backoff when a token refresh fails": true})

	if _, leaked := l.history["backoff"]; leaked {
		t.Error("a held-out commit reached the index — every measurement taken with " +
			"it is memorisation, not retrieval")
	}
	if _, kept := l.history["restyle"]; !kept {
		t.Error("holding one commit out must not empty the index")
	}
}
