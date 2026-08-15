package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/overlap"
	"github.com/agenxy/dibs/internal/ui"
)

// calibrate measures the overlap scorer against this repository's own history
// and proposes thresholds for it.
//
// It exists because `join_threshold` is unitless and scorer-relative, so any
// number compiled into the binary is a guess, and the two ways of guessing
// wrong are both bad in a way the user only discovers weeks later. Too low and
// every agent collapses into one agent; too high and nobody ever meets and the
// feature may as well be off.
//
// The benchmark is the user's own git log, which is the only evaluation here
// that cannot be gamed: a commit message is a task declaration and its changed
// files are the label, and no published model has trained on a private
// repository's history. See SPEC-CHANNELS.md §9.
//
// Nothing is written and nothing is applied. It prints numbers and a suggestion
// for a human to accept, which §7 requires.
// buildHeldOut samples the evaluation commits FIRST, then builds the scorer
// without them.
//
// Tier 0 learns how the project describes its work from commit messages, and
// this evaluates by using a commit message as the query and that commit's files
// as the answer. Index them and the query retrieves the commit it came from:
// this repository jumped to recall@5 0.815 and MRR 1.000 the moment the history
// index landed, which is a leak wearing a result's clothes.
//
// Holding them out asks the question anybody actually cares about: does history
// help on work the index has never seen, and matches what production does,
// which is index the past and predict the present. Measured that way across four
// repositories with real history, recall@5 rose 58–91%: a real gain, an order of
// magnitude smaller than the leak that hid it.
func buildHeldOut(ctx context.Context, cc *overlap.CoChange, dir string, n, skip int,
) (*overlap.Lexical, []overlap.EvalCase, int, error) {
	cases, err := overlap.SampleCommits(ctx, dir, n, 25, skip)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("sampling commits: %w", err)
	}
	holdOut := make([]string, 0, len(cases))
	for _, c := range cases {
		holdOut = append(holdOut, c.Message)
	}
	lex, err := overlap.NewLexicalHolding(ctx, dir, cc, holdOut)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("indexing files: %w", err)
	}
	return lex, cases, cc.Commits(), nil
}

// calibrateThresholds measures the threshold on the DEPLOYED index and the
// separation on the held-out one. See overlap.CalibrateWith for why they differ.
func calibrateThresholds(ctx context.Context, dir string, cc *overlap.CoChange,
	heldOut overlap.Scorer, usingEmbed bool, cases []overlap.EvalCase,
) (overlap.Calibration, error) {
	if usingEmbed {
		// Tier 2 indexes file CONTENTS, not commit messages, so there is no
		// hold-out to undo and both questions use the same scorer.
		return overlap.Calibrate(ctx, heldOut, cases)
	}
	deployed, err := overlap.NewLexical(ctx, dir, cc)
	if err != nil {
		return overlap.Calibration{}, fmt.Errorf("indexing files: %w", err)
	}
	return overlap.CalibrateWith(ctx, deployed, heldOut, cases)
}

func calibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository to calibrate against")
	n := fs.Int("n", 200, "commits to evaluate")
	skip := fs.Int("skip", 0, "skip this many recent commits (work in flight)")
	history := fs.Int("history", 2000, "commits to mine for co-change")
	verbose := fs.Bool("v", false, "show per-case predictions")
	embedURL := fs.String("embed-url", "",
		"measure an OpenAI-compatible embeddings service (Ollama, vLLM, TEI, hosted) "+
			"instead of the built-in scorer, on the SAME cases with the same metric")
	embedModel := fs.String("embed-model", "", "model name to request from that service")
	embedKey := fs.String("embed-key", "", "bearer token, for hosted endpoints")
	// The same overrides the daemon takes, so a convention can be MEASURED here
	// before it is configured there.
	qPrefix := fs.String("embed-query-prefix", "",
		"marker prepended to a query before embedding (default: inferred from the model name)")
	dPrefix := fs.String("embed-doc-prefix", "",
		"marker prepended to a document before embedding (see -embed-query-prefix)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// #nosec G204 -- no shell is involved: exec.Command passes argv directly,
	// so a path cannot inject arguments. The repository path comes from an
	// operator flag or config, never from an agent.
	root, err := exec.Command("git", "-C", *repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("%s is not a git repository (calibration needs history): %w", *repo, err)
	}
	dir := strings.TrimSpace(string(root))

	// Tier 0 is seconds; embedding a whole repository through a 4B model is not.
	// One budget for both would either strangle the model or leave a hung git
	// call running for an hour.
	budget := 5 * time.Minute
	if *embedURL != "" {
		budget = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	cc, err := overlap.MineCoChange(ctx, dir, overlap.CoChangeOptions{MaxCommits: *history})
	if err != nil {
		return fmt.Errorf("mining co-change: %w", err)
	}
	lex, cases, commits, err := buildHeldOut(ctx, cc, dir, *n, *skip)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("no usable commits in %s: need commits touching 1..25 files with a message", dir)
	}

	fmt.Printf("repository  %s\n", dir)
	fmt.Printf("indexed     %d files, %d commits of co-change history\n", lex.Files(), commits)
	fmt.Printf("evaluating  %d commits (skipping %d most recent)\n\n", len(cases), *skip)

	// The whole point of one harness: tier 0 and tier 2 measured on identical
	// cases with an identical metric. Comparing a published benchmark against a
	// local one would compare two different questions.
	var scorer overlap.Scorer = lex
	usingEmbed := *embedURL != ""
	if *embedURL != "" {
		// No fallback here on purpose. When MEASURING, a silent degrade to tier 0
		// would report the sidecar's score as tier 0's and hide the answer.
		em, err := buildEmbedScorer(ctx, dir, *embedURL, *embedModel, *embedKey, *qPrefix, *dPrefix)
		if err != nil {
			return err
		}
		// No fallback when MEASURING: a silent degrade to tier 0 would report
		// tier 0's numbers under the model's name and hide the answer.
		scorer = em
	}

	res, err := overlap.Evaluate(ctx, scorer, cases, []int{5, 10, 20, 40})
	if err != nil {
		return err
	}

	fmt.Println(ui.Section("scorer: " + res.ScorerID))
	for _, k := range []int{5, 10, 20, 40} {
		fmt.Printf("  %s %s\n", ui.Dim(fmt.Sprintf("recall@%-3d", k)), fmt.Sprintf("%.3f", res.RecallAt[k]))
	}
	fmt.Printf("  %s %s\n", ui.Dim("MRR       "), fmt.Sprintf("%.3f", res.MRR))
	// Abstentions are reported rather than folded into the mean: a scorer that
	// says nothing is failing differently from one that says something wrong,
	// and a single averaged number hides which is happening.
	fmt.Printf("  no answer  %d/%d (%.0f%%)\n\n", res.Empty, res.Cases,
		100*float64(res.Empty)/float64(res.Cases))

	// Threshold on the DEPLOYED index, quality on the held-out one.
	//
	// These are two different questions and they need two different indexes.
	// "How good is retrieval" must hold the evaluation commits out or it
	// measures memorisation. "What score separates related work from unrelated"
	// must NOT, because the daemon runs with every commit indexed, and a
	// threshold measured against a weaker index is too low for the stronger one.
	//
	// Measured: the number printed from the held-out index let 10.6% and 14.4%
	// of unrelated pairs through on two repositories, against the ~5% its own
	// "95th percentile of unrelated pairs" label promises. The label was true of
	// the index it was measured on and false of the one it was for.
	cal, err := calibrateThresholds(ctx, dir, cc, scorer, usingEmbed, cases)
	if err != nil {
		return err
	}
	printThresholds(cal)

	if *verbose {
		printSamples(ctx, scorer, cases, 10)
	}

	fmt.Printf("took %s\n", time.Since(start).Round(time.Millisecond))
	if cal.Measured() {
		fmt.Fprintln(os.Stderr, "\nnothing was written; set these in your Dibs config if they look right")
	}
	return nil
}

// buildEmbedScorer indexes the repository with a remote embedding service.
func buildEmbedScorer(ctx context.Context, dir, url, model, key, qPrefix, dPrefix string) (*overlap.Embed, error) {
	em := overlap.NewEmbed(url, model, key, 120*time.Second)
	// An explicit convention beats an inferred one: a family Dibs has never
	// heard of still has one, and its operator knows it.
	if qPrefix != "" || dPrefix != "" {
		em.SetAffixes(qPrefix, dPrefix)
	}
	fmt.Printf("scorer      %s (%s): indexing…\n", url, model)
	if err := em.Build(ctx, dir); err != nil {
		return nil, fmt.Errorf("indexing with the embeddings service: %w", err)
	}
	reportMarkers(em)
	return em, nil
}

// reportMarkers says how the model was ADDRESSED, not just which one it is.
//
// Retrieval models are asymmetric, and one given no markers separates related
// from unrelated work about half as well: a difference that would otherwise
// show up in the numbers below with nothing on screen to explain it.
func reportMarkers(em *overlap.Embed) {
	fmt.Printf("            %d chunks embedded\n", em.Chunks())
	if q, d := em.Affixes(); q != "" || d != "" {
		fmt.Printf("            %s\n", ui.Dim(fmt.Sprintf(
			"query marker %q, document marker %q", ui.Elide(q, 46), d,
		)))
		return
	}
	// No markers has two causes and they call for opposite actions. Some models
	// document that they need none (bge-m3 says so outright) and telling that
	// operator to go and find a convention sends them looking for something that
	// does not exist, and invites them to invent one, which measured WORSE than
	// none. So ask Recognised(), which carries the difference explicitly rather
	// than inferring it from the markers being empty.
	if em.Recognised() {
		fmt.Println("            " + ui.Dim(
			"no retrieval markers: this model documents that it needs none, so "+
				"it is addressed symmetrically on purpose",
		))
		return
	}
	fmt.Println("            " + ui.Attn("no retrieval markers") + ui.Dim(
		": this model name matches no convention Dibs knows, so it is being "+
			"addressed symmetrically",
	))
	fmt.Fprintln(os.Stderr,
		"if it is a retrieval model, find its query/document convention and pass\n"+
			"-embed-query-prefix / -embed-doc-prefix. Measured here, the same model\n"+
			"addressed correctly separated roughly twice as well.")
}

// printThresholds reports the numbers AND what they rest on.
//
// The zero-pair case returns documented defaults, and this used to print them
// under "suggested thresholds for THIS repository … (95th pct of unrelated
// pairs)": a provenance that is false, alongside "set these if they look
// right". Nothing on screen distinguished 0.750-because-we-measured from
// 0.750-because-we-could-not, and an operator has no way to judge the
// difference by eye. A number whose evidence is not stated is worse than no
// number, because it will be configured.
func printThresholds(cal overlap.Calibration) {
	switch {
	case !cal.Measured():
		fmt.Println(ui.Alarm("NO THRESHOLD COULD BE MEASURED HERE"))
		fmt.Printf("  this repository yielded no unrelated commit pairs to compare, so there\n")
		fmt.Printf("  is nothing to take a percentile of. The numbers below are Dibs' built-in\n")
		fmt.Printf("  DEFAULTS, not a measurement of your repository:\n")
		fmt.Printf("    join_threshold    %s   %s\n", ui.Bold(fmt.Sprintf("%.3f", cal.Join)), ui.Dim("(default)"))
		fmt.Printf("    notify_threshold  %s   %s\n\n", ui.Bold(fmt.Sprintf("%.3f", cal.Notify)), ui.Dim("(default)"))
		fmt.Fprintln(os.Stderr,
			"calibration needs a repository with enough history for commits to be\n"+
				"compared against each other. Run this against the repository your agents\n"+
				"actually work in; if that IS this one, it is too new to calibrate from:\n"+
				"leave join_threshold at 0 so Dibs suggests spaces but never auto-joins.")
	case cal.Thin():
		fmt.Println("suggested thresholds for THIS repository. " + ui.Attn("THIN EVIDENCE"))
		fmt.Printf("  join_threshold    %s   %s\n", ui.Bold(fmt.Sprintf("%.3f", cal.Join)),
			ui.Dim(fmt.Sprintf("(95th pct of only %d unrelated pairs)", cal.Pairs)))
		fmt.Printf("  notify_threshold  %s   %s\n\n", ui.Bold(fmt.Sprintf("%.3f", cal.Notify)),
			ui.Dim("(median of the same, floored at join/2)"))
		fmt.Fprintf(os.Stderr,
			"%d pairs is too few for a 95th percentile to be one: the top 5%% holds less\n"+
				"than a whole sample, so a single unlucky pair set this number. Treat it as a\n"+
				"starting point, re-run as the repository grows, and prefer suggest-only\n"+
				"(join_threshold 0) until it settles.\n", cal.Pairs)
	default:
		fmt.Println(ui.Bold("suggested thresholds for THIS repository"))
		fmt.Printf("  join_threshold    %s   %s\n", ui.Good(fmt.Sprintf("%.3f", cal.Join)),
			ui.Dim(fmt.Sprintf("(95th pct of %d unrelated pairs)", cal.Pairs)))
		notifyNote := "(median of the same)"
		// Name the rule that actually ran. The median is often ZERO on a scorer
		// that discriminates well, so the floor is the common case, not the
		// exception, and calling it "the median" misdescribes the number in the
		// one place an operator decides whether to trust it.
		if cal.NotifyFloored {
			notifyNote = "(half the join bar: the median of unrelated pairs was lower, " +
				"and a notify bar at that median fires on nearly everything)"
		}
		if cal.Degenerate {
			// The UI claimed this floor before the code applied it. Now it does,
			// and it says so only when it actually happened.
			notifyNote = "(derived as join/2: the unrelated pairs scored too alike " +
				"for a median to differ from the 95th percentile)"
		}
		fmt.Printf("  notify_threshold  %s   %s\n", ui.Good(fmt.Sprintf("%.3f", cal.Notify)),
			ui.Dim(notifyNote))
		// What that bar costs, said out loud. See NegAboveNotify: the number was
		// always measured and never shown, so the only way to learn it was to
		// run the fleet and watch unrelated work get mentioned.
		if cal.Pairs > 0 {
			rate := 100 * float64(cal.NegAboveNotify) / float64(cal.Pairs)
			fmt.Printf("  %s\n\n", ui.Dim(fmt.Sprintf(
				"at this bar ~%.0f%% of unrelated pairs are also mentioned (%d of %d)",
				rate, cal.NegAboveNotify, cal.Pairs)))
		} else {
			fmt.Println()
		}
	}
	printSeparation(cal)
	warnIfZeroJoin(cal)
}

// printSeparation answers the question a threshold cannot: can this scorer
// actually tell related work from unrelated work on THIS repository?
//
// A bar on its own looks equally healthy whether the two populations are far
// apart or overlapping. Measured here with a small embedding model, two
// declarations describing identical work scored 0.566 and 0.509 against a bar
// of 0.542: correctly ranked, and one of them silently did not join. Nothing
// on screen said so.
func printSeparation(cal overlap.Calibration) {
	if cal.Positives == 0 {
		// Not "no separation": no measurement. The scorer had no opinion on a
		// single pair of commits that history says touched common files, so
		// there is nothing to compare the threshold against. Silence here reads
		// as though the numbers above were validated; they were not.
		if cal.Measured() {
			fmt.Println(ui.Attn("  separation could not be measured") +
				ui.Dim("  (this scorer predicted nothing for any pair history says was related)"))
			fmt.Fprintln(os.Stderr,
				"the threshold above separates unrelated pairs from each other, which is not\n"+
					"the same as separating related work from unrelated work. Point -embed-url at\n"+
					"an embedding service to score work whose words do not appear in filenames.")
		}
		return
	}
	sep := cal.Separation()
	line := fmt.Sprintf("  %.0f%% of genuinely-related work clears that bar   %s",
		100*sep, ui.Dim(fmt.Sprintf("(%d of %d related pairs, median %.3f)",
			cal.PosAboveJoin, cal.Positives, cal.PosMedian)))
	// The bands are set from measurement, not taste. Across five scorers on this
	// repository: the built-in one and four embedding models, each given the
	// query/document markers it was trained for: separation ranged from 36% to
	// 50%, with two completely different mechanisms both landing near 50%. So a
	// band that calls 45% "poor" would fire on every configuration anybody can
	// actually reach, and a diagnostic that always warns is one people stop
	// reading. 40% is the line between usable and not; below 25% auto-join
	// genuinely fires on almost nothing.
	switch {
	case sep >= 0.45:
		fmt.Println(ui.Good(line))
	case sep >= 0.25:
		fmt.Println(ui.Attn(line))
		fmt.Fprintln(os.Stderr,
			"a bar this many related pairs fall below will miss real collisions. Worth\n"+
				"running, a missed match costs you only what you had before Dibs, but\n"+
				"check that -embed-model is one whose query/document convention Dibs knows\n"+
				"(qwen3-embedding, nomic-embed, e5, arctic-embed, each BGE generation).\n"+
				"Using a retrieval\n"+
				"model without its markers roughly halves this number.")
	default:
		fmt.Println(ui.Alarm(line))
		fmt.Fprintln(os.Stderr,
			"most related work scores BELOW this bar, so auto-join would fire on almost\n"+
				"nothing while still occasionally firing on the wrong thing. Leave\n"+
				"join_threshold at 0 and act on the suggestions, which cost nothing when they\n"+
				"are wrong.")
	}
}

// warnIfZeroJoin explains the one value that means something other than itself.
//
// 0 is the SENTINEL for suggest-only: `-match-join 0` never auto-joins. So a
// measured 0.000 collides with a mode switch: the measurement is saying "no
// unrelated pair scored above zero, so anything above zero is a match", and the
// config reads the same number as "never join". The conservative reading wins,
// which is the right accident, but an operator who configures a measured
// threshold and silently gets suggest-only has been misled by their own tool.
func warnIfZeroJoin(cal overlap.Calibration) {
	if cal.Join != 0 {
		return
	}
	fmt.Fprintln(os.Stderr,
		"NOTE: a join_threshold of 0 does not mean 'join on any overlap'. 0 is the\n"+
			"sentinel for suggest-only, so setting it turns auto-join OFF. Here that is\n"+
			"the right outcome: nothing separated related work from unrelated work, so\n"+
			"there is no bar worth acting on yet. Dibs will still tell agents which\n"+
			"spaces look relevant; it just will not move them automatically.")
}

// printSamples shows what the scorer predicted for a handful of real commits,
// with a tick against the files that actually changed.
//
// This is the part of calibration a human reads to decide whether the numbers
// mean anything: a recall figure says how often it was right, and only the
// samples say whether it was right for the right reasons.
func printSamples(ctx context.Context, s overlap.Scorer, cases []overlap.EvalCase, n int) {
	fmt.Println("sample predictions:")
	for i, c := range cases {
		if i >= n {
			break
		}
		p, err := s.Predict(ctx, c.Message, 5)
		if err != nil {
			continue
		}
		changed := make(map[string]bool, len(c.Changed))
		for _, f := range c.Changed {
			changed[f] = true
		}
		fmt.Printf("  %-58.58s\n", c.Message)
		for _, f := range p.Files {
			mark := " "
			if changed[f.Path] {
				mark = "✓"
			}
			fmt.Printf("      %s %-56.56s %.2f\n", mark, f.Path, f.Weight)
		}
	}
	fmt.Println()
}
