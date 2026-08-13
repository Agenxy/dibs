package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
)

// Auto-joining: the wire between "what is this agent doing" and "who else is
// doing it". See SPEC-CHANNELS.md §3.
//
// THE SPLIT, which is the whole design:
//
//	engine (here) : impure: runs a scorer, reads the repository, produces a
//	                 prediction and a score
//	core          : pure: takes the RECORDED prediction and score as fact
//
// Everything impure happens once, at the edge, and is written into the op. Replay
// then reconstructs identical membership with no scorer, no index and no
// repository present. Getting this backwards (scoring inside Apply) would
// make every replay produce a different fleet and quietly void the hash chain
// (SPEC-CHANNELS.md §4.3).

// MatchConfig is the tuning for auto-join.
//
// The thresholds have NO safe defaults, and that is deliberate rather than an
// omission. They are unitless and scorer-relative: SPEC-CHANNELS.md originally
// proposed 0.75, and `dibs calibrate` against this very repository returned
// 0.327: the shipped guess would have auto-joined nothing and the feature would
// have looked broken rather than mis-tuned. So an unconfigured Dibs runs in
// notify-only mode and says so, instead of guessing a bar it cannot know.
type MatchConfig struct {
	JoinThreshold   float64
	NotifyThreshold float64
	// Deadline bounds the scorer. This sits in front of declare, which agents
	// call constantly; a slow model must never be what makes declaring work feel
	// slow. Past the deadline the declaration proceeds unmatched.
	Deadline time.Duration

	// DirectorRequired gates every join on a coordinator admitting it
	// (SPEC-CHANNELS.md §8.1). OFF by default, and §8.1 says why in its own
	// words: it serialises the fleet behind one agent. With it on, a match above
	// the join bar becomes a REQUEST to the director rather than a membership,
	// the agent is told who to ask and is not silently left wondering why the
	// agent it clearly belongs in never opened to it.
	DirectorRequired bool

	// Repo is the repository the index was built from, and it is here so that
	// auto-join can ask whether the declaring agent is even in it.
	//
	// Every declaration is scored against ONE index. Nothing used to check where
	// the agent actually was, so an agent working on an unrelated project was
	// scored against a tree it has no part in, and the only files that can match
	// across two unrelated trees are the generic ones every project has: Justfile,
	// ci.yml, CMakeLists.txt. Those co-change with everything, so they carry
	// almost no signal while looking like solid evidence.
	//
	// Empty means "unknown", which does not gate anything. See inMatchedRepo.
	Repo string

	// AutoJoin decides who makes the join decision: "declared" (default),
	// "always", or "never".
	//
	// The default routes it to whoever is better at it, and on a live fleet that
	// was not the scorer. Two agents caught false auto-joins from the inside,
	// immediately, with sharper reasoning than any threshold had. "the shared-file
	// evidence is generic", "we match mainly because we are both in the same repo",
	// while the scorer's own calibration put recall at 26% and its false positives
	// cleared the bar. Auto-join never saved either of them a decision either; it
	// just made the wrong one first and cost them a turn getting out.
	//
	// So an INFERRED match is now a proposal, and the agent judges it with the
	// evidence in hand. A DECLARED match (both sides wrote "pr:1231") is not a
	// judgement call, and still joins automatically, because there is nothing to
	// weigh.
	//
	// "always" restores unconditional auto-join above the bar for operators who
	// want it; "never" proposes everything, including declared overlap.
	AutoJoin string
}

// Auto-join policies.
const (
	AutoJoinDeclared = "declared" // default: certainty joins, guesses are proposed
	AutoJoinAlways   = "always"
	AutoJoinNever    = "never"
)

// DefaultMatchConfig is notify-only: it can suggest, and it will never
// auto-join, because nothing has told it what a high score means here.
var DefaultMatchConfig = MatchConfig{
	JoinThreshold:   0, // 0 disables auto-join entirely
	NotifyThreshold: 0,
	Deadline:        1500 * time.Millisecond,
}

// matchRepo is the indexed repository, for turning declared absolute paths into
// the relative form the index names files by.
func (e *Engine) matchRepo() string {
	e.matchMu.RLock()
	defer e.matchMu.RUnlock()
	return e.matchCfg.Repo
}

func (e *Engine) scorerAndCfg() (overlap.Scorer, MatchConfig) {
	e.matchMu.RLock()
	defer e.matchMu.RUnlock()
	return e.scorer, e.matchCfg
}

// scorerFor returns the index built from the tree this agent is working in.
//
// One scorer per repository, because a co-change model is only meaningful
// inside the history it was mined from: asking an index of project A which
// files project B's sentence predicts returns confident nonsense. Agents in
// different projects are already prevented from matching each other
// (differentProjects in core), and this is the same rule one level down.
//
// An agent whose cwd is in no indexed tree gets no scorer and therefore no
// semantic suggestions. It still gets the shared-refs and shared-dirs signals,
// which are computed in the pure core and need no index at all.
func (e *Engine) scorerFor(cwd string) (overlap.Scorer, MatchConfig) {
	e.matchMu.RLock()
	defer e.matchMu.RUnlock()
	if len(e.scorers) == 0 {
		return e.scorer, e.matchCfg
	}
	// Longest matching root wins, so a nested checkout is scored by its own
	// index rather than by the parent it happens to sit inside. The root is
	// kept here rather than recovered afterwards: searching the map for the
	// scorer we just chose meant comparing interface values with ==, which
	// panics outright on any implementation whose dynamic type is not
	// comparable, and it was a second scan for something already in hand.
	var (
		best     overlap.Scorer
		bestRepo string
	)
	for repo, s := range e.scorers {
		if inMatchedRepo(cwd, repo) && len(repo) > len(bestRepo) {
			best, bestRepo = s, repo
		}
	}
	if best == nil {
		return nil, e.matchCfg
	}
	cfg := e.matchCfg
	cfg.Repo = bestRepo
	return best, cfg
}

// SetScorerForRepo publishes the index for one repository. Thresholds are
// shared: they are a policy about how confident a match must be, not a property
// of any one tree.
func (e *Engine) SetScorerForRepo(repo string, s overlap.Scorer, cfg MatchConfig) {
	e.matchMu.Lock()
	defer e.matchMu.Unlock()
	if e.scorers == nil {
		e.scorers = map[string]overlap.Scorer{}
	}
	e.scorers[repo] = s
	cfg.Repo = repo
	e.matchCfg = cfg
	if e.scorer == nil {
		// Keep the single-scorer accessors working for callers with no agent in
		// hand, such as Predict from the human CLI.
		e.scorer = s
	}
}

// IndexedRepos lists the trees currently indexed, for status and for doctor.
func (e *Engine) IndexedRepos() []string {
	e.matchMu.RLock()
	defer e.matchMu.RUnlock()
	out := make([]string, 0, len(e.scorers))
	for repo := range e.scorers {
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

// Suggestion is one agent offered to an agent that just declared work.
type Suggestion struct {
	Agent   string   `json:"agent"`
	Topic   string   `json:"topic"`
	Score   float64  `json:"score"`
	Members int      `json:"members"`
	Owner   string   `json:"owner,omitempty"`
	Shared  []string `json:"shared,omitempty"`
	Action  string   `json:"action"` // joined | queued | consider | awaiting_director
	// SharedRefs are objective ids both sides declared: the difference between
	// knowing and guessing. See core.AgentMatch.SharedRefs.
	SharedRefs []string `json:"shared_refs,omitempty"`
	// Relation and Evidence are what the decision actually rested on. Absent from
	// the wire, an agent sees an action with no reason and cannot check it: and
	// neither could an end-to-end test, which is how a live reviewer got
	// auto-joined while every unit test passed.
	Relation core.Relation `json:"relation,omitempty"`
	Evidence core.Evidence `json:"evidence,omitzero"`
	Position int           `json:"queue_position,omitempty"`
	// Key is the coordination key, present only when this suggestion actually
	// joined the agent. Declared in `refs` on a later declare it matches this
	// agent exactly rather than by wording: see attemptJoin.
	Key  string `json:"key,omitempty"`
	Hint string `json:"hint,omitempty"`
}

// matchDeclaration scores a declaration against live agents and acts on it.
//
// Runs OFF the writer loop for the scoring, then re-enters it to join. That
// ordering matters: the scorer may take a second and the loop is the whole
// daemon, so holding it while a model runs would stall every other agent.
// matchOutcome distinguishes the two ways a declaration produces no agents.
//
// "I compared you against every agent and none was close" is real information.
// "I could form no opinion about what you are working on" is not, and reporting
// the second as the first is a confident claim built on no evidence.
type matchOutcome int

const (
	matchedNothing   matchOutcome = iota // compared, and nothing was close
	matchedNoOpinion                     // the scorer predicted nothing at all
	// matchedAlreadyIn: the closest agents are ones this agent is ALREADY in.
	//
	// A distinct outcome because the fallback for "no suggestions" is "you have
	// the field to yourself", and for this case that is flatly false: the agent
	// is standing in an agent with the very peers it would be told do not exist.
	// It happens on every slot refresh after an auto-join, which is to say
	// constantly, to precisely the agents that DID coordinate.
	matchedAlreadyIn
)

// declarationOf is everything the agent just said about its work, in one value.
//
// It used to be three loose arguments, and activity and holds were added to the
// op without being added here, so Complementary was never computed and a live
// REVIEWER was auto-joined into a duplicate-work agent while every unit test
// passed. A struct makes the omission a compile error instead of a silence.
func declarationOf(op *core.Op) core.Slot {
	return core.Slot{
		Text: op.Text, Dirs: op.Dirs, Refs: op.Refs,
		Activity: op.Activity, Holds: op.Holds,
	}
}

// cwd is passed in rather than resolved here because the scorer has to be
// chosen before the loop trip below, and DoMatched has already paid for that
// lookup: prediction, path relativisation and matching then all speak about the
// same tree instead of three possibly different ones.
func (e *Engine) matchDeclaration(
	ctx context.Context, token, cwd string, decl core.Slot,
) ([]Suggestion, matchOutcome) {
	declaration, declRefs, declDirs := decl.Text, decl.Refs, decl.Dirs
	scorer, cfg := e.scorerFor(cwd)
	if scorer == nil || (declaration == "" && len(declRefs) == 0) {
		// No scorer at all: the phase already says "off", and that hint is more
		// useful than either of the two outcomes here.
		return nil, matchedNothing
	}
	sctx, cancel := context.WithTimeout(ctx, cfg.Deadline)
	defer cancel()

	pred, err := scorer.Predict(sctx, declaration, 40)
	// A silent scorer must not silence the agent's own declarations.
	//
	// This used to return here whenever the scorer predicted nothing, BEFORE refs,
	// dirs and holds were ever looked at, so an exact pr:77 shared by two agents
	// was invisible if the prose happened to share no token with any file path.
	// Found end-to-end and not by any unit test: two agents declared the same PR,
	// the scorer had no opinion about either sentence, and Dibs told them both
	// they were alone.
	//
	// Tier 0 matches declared WORDS against file PATHS, so "adding the promotion
	// path" predicts nothing in a repository whose file is queue.go. That is a
	// statement about the scorer's vocabulary, not about the fleet, and it says
	// nothing whatsoever about a ref both agents typed by hand.
	declared := len(declRefs) > 0 || len(declDirs) > 0 || len(decl.Holds) > 0
	if err != nil || len(pred.Files) == 0 {
		// NO OPINION, which is not the same as "nobody else is doing this".
		// Reporting it as "you have the field to yourself" would be a confident
		// claim built on no evidence (SPEC-CHANNELS.md §10.1).
		if !declared {
			return nil, matchedNoOpinion
		}
		pred.Files = nil // carry on with facts alone
	}
	recorded := withDeclaredDirs(toPredFiles(pred.Files), declDirs, cfg.Repo)

	// Dibs opened before the index was ready carry no footprint and would be
	// invisible forever; give them one first.
	overlay := e.backfillFootprints(sctx, scorer)

	lens := e.repoLensForBoard(ctx)

	// One trip onto the loop to read the board coherently.
	var matches []core.AgentMatch
	var self, selfCWD string
	_, _ = e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if l == nil {
			return core.Result{}
		}
		self = l.ID
		if l.Agent != nil {
			selfCWD = l.Agent.CWD // where this agent actually is; see inMatchedRepo
		}
		mine := decl
		mine.Predicted = recorded
		matches = e.state.MatchAgentsEvidence(l.ID, mine, selfCWD, cfg.Repo, lens, overlay, 5)
		return core.Result{}
	})
	// An unresolvable token is the only genuine dead end here. NO MATCHES is not
	// one, it is the case that opens the first agent, below, and returning
	// early on it is what made that code unreachable on exactly the board where
	// it mattered most: an empty one.
	if self == "" {
		return nil, matchedNothing
	}

	out := e.suggestionsFor(ctx, token, matches, cfg, pred, recorded, selfCWD)

	// Nothing matched, so OPEN the first agent. SPEC-CHANNELS §3: "If no agent
	// matched at all, a new agent is opened with the declaration as its topic."
	//
	// This was the whole feature's missing half, and its absence was invisible
	// from the inside. Matching only ever compared a declaration against agents
	// that already existed, and nothing created the first one, so on a fresh
	// board two agents could declare identical work and BOTH were told "no other
	// agent is working on anything close to this: you have the field to
	// yourself." Precisely the false statement spaces exist to prevent, stated
	// with confidence, to the one agent that most needed the opposite.
	//
	// The end-to-end suite passed throughout because it calls open_space by hand
	// between the two agents, so it was always testing the second half of a
	// mechanism whose first half did not exist.
	if len(out) == 0 {
		if alreadyCoordinating(matches, cfg.NotifyThreshold) {
			return nil, matchedAlreadyIn
		}
		if s := e.openFirstSpace(ctx, token, declaration, pred, recorded); s != nil {
			return []Suggestion{*s}, matchedNothing
		}
	}
	return out, matchedNothing
}

// suggestionsFor turns scored agents into what the agent is told, joining it to
// the ones above the bar as it goes.
func (e *Engine) suggestionsFor(ctx context.Context, token string, matches []core.AgentMatch,
	cfg MatchConfig, pred overlap.Prediction, recorded []core.PredFile, selfCWD string,
) []Suggestion {
	// Positive evidence that this agent is working somewhere else entirely.
	// Everything still gets SURFACED; what this withholds is the automatic join.
	foreign := !inMatchedRepo(selfCWD, cfg.Repo)
	var out []Suggestion
	for _, m := range matches {
		if m.AlreadyIn || tooWeakToMention(m, cfg.NotifyThreshold) {
			continue
		}
		s := Suggestion{
			Agent: m.Agent, Topic: m.Topic, Score: round4(m.Score),
			Members: m.Members, Owner: m.Owner, Shared: predPaths(m.Shared),
			Action: "consider",
		}
		if h := withheldReason(m, foreign, selfCWD, cfg.Repo); h != "" {
			s.Hint = h
			out = append(out, s)
			continue
		}
		s.SharedRefs, s.Relation, s.Evidence = m.SharedRefs, m.Relation, m.Evidence
		s.Hint = explain(m)
		aboveBar := cfg.JoinThreshold > 0 && m.Score >= cfg.JoinThreshold
		// The director gate outranks the auto-join policy, and has to.
		//
		// An operator who said a coordinator decides memberships has not delegated
		// that to the agent either. Letting a proposal through here would mean the
		// agent simply calls join_space and the gate it was told about never
		// happens: the policy would silently become advice.
		if cfg.DirectorRequired && (aboveBar || len(m.SharedIDs) > 0) {
			s.Action = "awaiting_director"
			s.Hint = "a coordinator must admit you to this agent (admit); " +
				"send one a request naming " + m.Agent
			out = append(out, s)
			continue
		}
		// Certainty joins; a guess is offered. See MatchConfig.AutoJoin.
		if !shouldAutoJoin(cfg, m) {
			if len(m.SharedIDs) == 0 && aboveBar {
				s.Hint = "close enough to be worth your attention, but this is a SCORE, not a fact: " +
					"the evidence is the shared files above. Read the agent with read_space and " +
					"join_space if it is really your work. Declaring the same refs (pr:…, gate:…, " +
					"incident:…) as another agent joins you automatically, because that is not a guess."
			}
			out = append(out, s)
			continue
		}
		if aboveBar || len(m.SharedIDs) > 0 {
			s.Action, s.Position, s.Key = e.attemptJoin(ctx, token, m, cfg, pred, recorded)
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// withheldReason explains a match that will not be joined automatically no
// matter what the policy says, or "" when nothing stands in the way.
func withheldReason(m core.AgentMatch, foreign bool, cwd, repo string) string {
	if m.Declined {
		return "not joined automatically: you left this agent deliberately, and Dibs does not " +
			"put an agent back somewhere it walked out of. join_space if you have changed your mind."
	}
	if foreign {
		return "not joined automatically: you are working in " + cwd +
			", outside the matched repository " + repo + ", so the shared files above are " +
			"generic rather than evidence you are doing the same work. Join with join_space if " +
			"this is a worktree or a second checkout and the match is real."
	}
	return ""
}

// tooWeakToMention applies the score bar ONLY to matches that rest on the score.
//
// The bar used to run first, on everything, so a declared fact with no textual
// similarity was dropped before its relation was ever considered: two agents
// needing port:8080 for unrelated work score 0.0 on prose and were discarded one
// line after their contention had been correctly computed. Found end-to-end.
func tooWeakToMention(m core.AgentMatch, notify float64) bool {
	switch m.Relation {
	case core.RelationSameItem, core.RelationSameSurface, core.RelationContended:
		return false // rests on something the agents declared, not on a score
	case core.RelationPossible, core.RelationNone:
		return m.Score < notify
	}
	return m.Score < notify
}

// explain says WHY, always.
//
// An action with no reason cannot be checked by the agent it is aimed at: and
// the two agents who caught this system's false positives did it by reading the
// evidence, not the score.
func explain(m core.AgentMatch) string {
	why := m.Evidence.Strongest()
	if why == "" {
		return ""
	}
	return relationLead(m.Relation) + why
}

// relationLead opens the explanation with what the relation MEANS, so the agent
// does not have to infer the strength of a claim from the evidence behind it.
func relationLead(r core.Relation) string {
	switch r {
	case core.RelationSameItem:
		return "the same work item: "
	case core.RelationSameSurface:
		return "overlapping territory: you may or may not touch the same files: "
	case core.RelationContended:
		return "a resource collision, whatever the work is: "
	case core.RelationPossible:
		return "possibly related, on weak evidence: "
	case core.RelationNone:
		return ""
	}
	return ""
}

// shouldAutoJoin decides whether Dibs joins this match on the agent's behalf or
// hands it the decision.
//
// Declared overlap is a fact both agents wrote down; inferred overlap is a
// scorer's opinion, and the agents proved better at judging those than the bar
// was. See MatchConfig.AutoJoin.
func shouldAutoJoin(cfg MatchConfig, m core.AgentMatch) bool {
	switch cfg.AutoJoin {
	case AutoJoinNever:
		return false
	case AutoJoinAlways:
		return true
	default: // AutoJoinDeclared, and the zero value, which must be the safe one
		// The RELATION, not a score and not a ref list. Only "both named the same
		// thing that exists" is strong enough to act on without asking, and not
		// when the two agents hold different ROLES on it, because putting a
		// reviewer in a duplicate-work agent tells it to stop reviewing.
		return m.Relation == core.RelationSameItem && !m.Evidence.Complementary
	}
}

// withDeclaredDirs puts what the agent SAID it is working on ahead of what a
// scorer guessed from its prose.
//
// Tier 0 predicts by matching declaration words against file PATHS, one token at
// a time. Probed against a real repository, "CLI + web UI docs, cross-cutting
// gates" returns packages/coding-agent/src/cli/*, because the word "cli" occurs
// in those paths, and "runtime C++ forge, CI throughput, gate infrastructure"
// returns .github/workflows/pr-gate.yml, because of the word "gate". Neither
// declaration is about those files.
//
// That is where the fleet's false matches came from. Two agents in one repository
// write declarations sharing ordinary words, CI, gates, docs, runtime, those
// words map to overlapping path sets, and the overlap is reported back to them as
// evidence they are doing the same work. The evidence was generated by the
// scorer, not observed from either agent.
//
// `dirs` is the opposite kind of signal: the agent named those directories
// itself. So a declared directory enters the footprint at full weight, ahead of
// anything inferred, and an agent that says where it works is believed over a
// scorer that guessed from its adjectives.
//
// Guessed files are kept rather than replaced. They are how an agent that
// declared no dirs is matched at all, and how a match is found in a directory the
// agent had not thought to name.
func withDeclaredDirs(pred []core.PredFile, dirs []string, repo string) []core.PredFile {
	if len(dirs) == 0 {
		return pred
	}
	out := make([]core.PredFile, 0, len(pred)+len(dirs))
	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		rel := relativeTo(d, repo)
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		// Weight 1: stated, not inferred. Nothing a scorer produces outranks it.
		out = append(out, core.PredFile{Path: rel, Weight: 1})
	}
	for _, f := range pred {
		if !seen[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// relativeTo expresses a declared directory the way the index names files, so a
// declared path and a predicted one can be compared at all.
func relativeTo(dir, repo string) string {
	if dir == "" {
		return ""
	}
	if repo == "" {
		return strings.TrimPrefix(dir, "/")
	}
	if rel, ok := strings.CutPrefix(dir, strings.TrimSuffix(repo, "/")+"/"); ok {
		return rel
	}
	if dir == strings.TrimSuffix(repo, "/") {
		return "" // the whole repo says nothing about WHERE in it
	}
	// Outside the matched repository. The repo guard already refuses to auto-join
	// on this; adding the path here would put a file the index has never heard of
	// into the footprint, where it can only ever match another agent declaring the
	// exact same outside path, which is signal, not noise.
	return strings.TrimPrefix(dir, "/")
}

// alreadyCoordinating reports whether the declaring agent is already in one of
// the agents this declaration matched. If it is, its work already has a home and
// a second agent for the same thing is noise: an agent that refines its slot
// text must not spawn an agent per edit.
func alreadyCoordinating(matches []core.AgentMatch, notify float64) bool {
	for _, m := range matches {
		// RELEVANT membership, not any membership. This counted a match at any
		// score above zero, so an agent that moved on to genuinely different
		// work could never open a space for it: a faint accidental overlap with
		// the agent it was still in (one shared file is enough) suppressed the
		// new agent and told it "the work closest to this is in an agent you are
		// already in; you are not working alone", about work it had stopped
		// doing.
		//
		// An agent's second task is the normal case, not an edge one, and the
		// bar for "you are already coordinating on this" has to be the same bar
		// used for "this is worth mentioning at all".
		if m.AlreadyIn && m.Score >= notify {
			return true
		}
	}
	return false
}

// spaceName turns a declaration into a short, readable agent name.
//
// Keeps the first few words that carry meaning and drops the ones that do not:
// an agent writes "I am fixing the retry loop when tokens fail to refresh" and
// the agent should be called "fixing-retry-loop-tokens", not the whole sentence.
// This is not cosmetic: the id is what another agent passes to join_space and
// what a human reads on the board, and a fifty-character slug of somebody's
// first-person phrasing is unusable as both.
//
// Deliberately dumb: no stemming, no model, no cleverness that could differ
// between builds. The TOPIC keeps the declaration verbatim, so nothing is lost.
func spaceName(declaration string) string {
	// Filler carries no information about the work. Kept short and literal
	// rather than exhaustive: this only has to make ids readable, and a longer
	// list is a longer thing to be subtly wrong about.
	skip := map[string]bool{
		"i": true, "im": true, "am": true, "is": true, "are": true, "the": true,
		"a": true, "an": true, "to": true, "of": true, "in": true, "on": true,
		"for": true, "and": true, "with": true, "when": true, "that": true,
		"this": true, "it": true, "its": true, "my": true, "we": true, "our": true,
		"be": true, "been": true, "will": true, "just": true, "some": true,
		"working": true, "work": true, "doing": true, "going": true,
	}
	var kept []string
	for _, w := range strings.Fields(strings.ToLower(declaration)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		// Apostrophes are INTERIOR, so trimming the ends leaves "i'm" intact and
		// it misses the filler list: the id came out as "i'm auth middleware".
		// Dropping them folds contractions onto their bare forms, which is what
		// the list is written in.
		w = strings.ReplaceAll(w, "'", "")
		w = strings.ReplaceAll(w, "\u2019", "")
		if w == "" || skip[w] {
			continue
		}
		kept = append(kept, w)
		if len(kept) == 5 {
			break
		}
	}
	if len(kept) == 0 {
		// Every word was filler, which is a real declaration ("just working on
		// it") even if an unhelpful one. Fall back rather than open a nameless
		// agent, and let cleanID truncate.
		return declaration
	}
	return strings.Join(kept, " ")
}

// openFirstSpace creates the agent a declaration deserves when none exists yet,
// with the declaration as its topic and its predicted files as its footprint.
//
// The footprint is the point: an agent with none can never be matched against, so
// opening one without it would leave the next agent exactly as alone as before.
//
// Failure is deliberately silent. This is additive: the agent has already
// declared its work successfully, so an agent limit, an id collision with a
// human-named agent, or a lost race with another agent declaring the same thing
// costs a suggestion, never the declaration.
func (e *Engine) openFirstSpace(ctx context.Context, token, declaration string,
	pred overlap.Prediction, recorded []core.PredFile,
) *Suggestion {
	// The topic keeps the declaration, bounded: it is what a reader sees to
	// understand what the agent is FOR, and what the next agent reads before
	// deciding to join.
	topic := declaration
	if len(topic) > 120 {
		topic = strings.TrimSpace(topic[:120]) + "…"
	}
	// The agent's TOPIC is the declaration verbatim; its NAME is not.
	//
	// Ids are slugified topics, so naming an agent after a whole first-person
	// sentence produced things like
	// "i-am-fixing-the-retry-loop-when-tokens-fail-to-refresh": the id an agent
	// types into join_space, a human reads on the board, and a projector shows to
	// a room. An agent is named for the WORK, not for the sentence somebody used
	// to describe it.
	name := spaceName(declaration)
	// A slug collision must not disable bootstrapping for that wording forever.
	//
	// The agent id is derived from the declaration, so an unrelated agent a human
	// happened to name the same thing takes the id and every future declaration
	// phrased that way fails to open, gets no agent, and is told it is alone,
	// permanently, because the next agent collides identically. Disambiguating
	// costs one suffix; the SECOND agent then matches this agent on its footprint
	// and joins, which is the behaviour that matters.
	var res core.Result
	var err error
	for attempt := 1; attempt <= 4; attempt++ {
		attempted := name
		if attempt > 1 {
			attempted = fmt.Sprintf("%s %d", name, attempt)
		}
		res, err = e.Do(ctx, &core.Op{
			Kind: core.OpSpaceOpen, Token: token, Space: attempted, Text: topic,
			Predicted: recorded, ScorerID: pred.ScorerID, ScorerVersion: pred.Version,
			// Dibs opened this, not an agent, so it may be reclaimed when it
			// empties, and an agent somebody opened deliberately may not.
			Auto: true,
		})
		if err == nil {
			break
		}
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Code != "E_LANE_EXISTS" {
			return nil // a limit, a closed agent, anything else: not ours to retry
		}
	}
	if err != nil {
		return nil
	}
	id, _ := res["agent_id"].(string)
	if id == "" {
		return nil
	}
	return &Suggestion{
		Agent: id, Topic: topic, Action: "opened", Members: 1,
		// Says what was MEASURED, not what it implies.
		//
		// This read "nobody else is declaring work like this yet", which is a
		// claim about the world that Dibs cannot make. Recall at tier 0 is about
		// 0.3: for two thirds of declarations the right agent is not in the top
		// five, so silence is the common case rather than evidence. SKILLS.md
		// tells agents in as many words never to conclude from silence that they
		// are alone, and then the API said exactly that, with more authority than
		// the document, because it arrives as a result rather than as advice.
		//
		// A reviewer took it at face value and reported being alone on work
		// another agent had declared minutes earlier.
		Hint: "no existing agent cleared the match threshold, so one was opened for " +
			"this work: the next agent whose declaration does clear it joins you here " +
			"instead of duplicating. A miss is not proof you are alone: recall is " +
			"partial, so declare refs (pr:, issue:, key:) if you want to be found " +
			"exactly rather than approximately",
	}
}

// attemptJoin performs the auto-join and reports what happened.
//
// A join that FAILS is not an error the agent needs to see: the suggestion still
// stands, and it can join by hand. Losing the whole match because the agent
// turned exclusive a moment ago would be worse than telling the agent about it.
func (e *Engine) attemptJoin(ctx context.Context, token string, m core.AgentMatch,
	cfg MatchConfig, pred overlap.Prediction, recorded []core.PredFile,
) (action string, position int, key string) {
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpSpaceJoin, Token: token, Space: m.Agent,
		Score: m.Score, Threshold: cfg.JoinThreshold,
		ScorerID: pred.ScorerID, ScorerVersion: pred.Version,
		Evidence: predPaths(m.Shared), Auto: true,
		Predicted: recorded,
	})
	switch {
	case err != nil:
		return "consider", 0, ""
	case res["queued"] == true:
		p, _ := res["queue_position"].(int)
		return "queued", p, ""
	case res["joined"] == true:
		// Hand back the coordination key, exactly as an explicit join_space does.
		//
		// Two routes reach membership, asking, and being matched, and only the
		// first returned the key. So an agent the matcher joined was a member of a
		// agent it could not name exactly: the key exists to be declared in `refs`
		// on the NEXT declare so Dibs matches by identity instead of guessing
		// from wording, and the agent auto-joined by guessing was the one left with
		// no way to stop being guessed at. It could only recover by calling
		// join_space on an agent it was already in, which returns the key it should
		// have been given: a recovery nothing told it about.
		k, _ := res["key"].(string)
		return "joined", 0, k
	}
	return "consider", 0, ""
}

// Predict exposes the scorer for callers that need a recorded footprint before
// opening an agent, so a new agent starts with a footprint rather than an empty one
// that nothing can ever match against.
func (e *Engine) Predict(ctx context.Context, declaration string) ([]core.PredFile, string, string) {
	scorer, cfg := e.scorerAndCfg()
	return e.predictWith(ctx, scorer, cfg, declaration)
}

// predictIn predicts using the index built from the tree at cwd, and reports
// which tree that was so the caller can relativise declared paths against the
// same one.
//
// Split from Predict because the declare path must not use the global scorer.
// That field holds whichever repository was indexed FIRST, so on a machine with
// more than one project every other agent's footprint was predicted from a
// history it has nothing to do with, and op.Predicted is persisted: the wrong
// answer went into the ledger and became what later comparisons matched on.
func (e *Engine) predictIn(
	ctx context.Context, cwd, declaration string,
) ([]core.PredFile, string) {
	scorer, cfg := e.scorerFor(cwd)
	pred, _, _ := e.predictWith(ctx, scorer, cfg, declaration)
	return pred, cfg.Repo
}

func (e *Engine) predictWith(
	ctx context.Context, scorer overlap.Scorer, cfg MatchConfig, declaration string,
) ([]core.PredFile, string, string) {
	if scorer == nil || declaration == "" {
		return nil, "", ""
	}
	sctx, cancel := context.WithTimeout(ctx, cfg.Deadline)
	defer cancel()
	pred, err := scorer.Predict(sctx, declaration, 40)
	if err != nil {
		return nil, "", ""
	}
	return toPredFiles(pred.Files), pred.ScorerID, pred.Version
}

func toPredFiles(in []overlap.File) []core.PredFile {
	out := make([]core.PredFile, 0, len(in))
	for _, f := range in {
		out = append(out, core.PredFile{Path: f.Path, Weight: f.Weight})
	}
	return out
}

// repoLensForBoard resolves where every agent actually is, off the state loop.
//
// Git is the only thing that can tell a linked worktree from a separate clone,
// and paths.Identify shells out to it on a cache miss. Doing that inside the
// matching trip would hold the whole board still for as long as `git rev-parse`
// took, so the directories are collected in one cheap read and identified out
// here: leaving core a lookup that cannot block and cannot fail.
func (e *Engine) repoLensForBoard(ctx context.Context) core.RepoLens {
	var cwds []string
	_, _ = e.query(ctx, func() core.Result {
		for _, l := range e.state.Agents {
			if l.Agent != nil {
				cwds = append(cwds, l.Agent.CWD)
			}
		}
		return core.Result{}
	})
	return newRepoLens(cwds)
}

func predPaths(in []core.PredFile) []string {
	out := make([]string, 0, len(in))
	for _, f := range in {
		out = append(out, f.Path)
	}
	return out
}

// round4 keeps recorded scores short enough to read on a board without losing
// the resolution a threshold comparison needs.
func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}

// DoMatched runs a declaring op and then tells the agent who else is doing that.
//
// The order is deliberate: the declaration ALWAYS lands first, and matching is
// strictly additive afterwards. Dibs never blocks an agent from saying what it
// is working on (the same rule declare's overlap check already follows) so a
// scorer that is slow, broken or absent costs the agent a suggestion and never
// costs it the ability to declare.
func (e *Engine) DoMatched(ctx context.Context, op *core.Op) (core.Result, error) {
	// Predict BEFORE applying, so the declaration's own footprint is part of the
	// recorded op and lands on the slot.
	//
	// The order used to be the other way round, which meant the slot stored no
	// footprint of its own and the only thing matching could compare against was
	// the agent's accumulated union: the union that grows with every join and
	// never shrinks. Predicting first is what makes slot-to-slot comparison
	// possible at all, and it costs nothing extra: the same prediction is reused
	// below instead of being computed twice.
	// Resolved once, before anything uses it, so the prediction, the paths it
	// is relativised against and the match all speak about the same tree.
	// matchRepo() cannot serve here: it returns whichever repository was
	// indexed LAST, so declared dirs were being made relative to a root the
	// declaring agent may never have seen.
	cwd := e.cwdForToken(ctx, op.Token)
	if len(op.Predicted) == 0 && op.Text != "" {
		pred, repo := e.predictIn(ctx, cwd, op.Text)
		op.Predicted = withDeclaredDirs(pred, op.Dirs, repo)
	}
	res, err := e.Do(ctx, op)
	if err != nil {
		return nil, err
	}
	sug, outcome := e.matchDeclaration(ctx, op.Token, cwd, declarationOf(op))
	annotateMatching(res, sug, outcome, e.MatchStatus())
	return res, nil
}

// annotateMatching explains the result: which agents, and: when there are none
// WHY there are none. The three answers are genuinely different and were once
// all the same silence.
func annotateMatching(res core.Result, sug []Suggestion, outcome matchOutcome, st MatchStatus) {
	res["matching"] = st.Phase
	if len(sug) > 0 {
		res["agents"] = sug
		// A degraded scorer's results are real but weaker, and saying so is what
		// stops a thin match being read as a confident one.
		if st.Phase == MatchDegraded {
			res["matching_hint"] = st.Hint
		}
		res["agents_hint"] = agentsHint(sug)
		return
	}
	switch {
	case st.Hint != "":
		res["matching_hint"] = st.Hint
	case outcome == matchedAlreadyIn:
		res["matching_hint"] = "the work closest to this is in an agent you are already in. " +
			"read it with read_space; you are not working alone"
	case outcome == matchedNoOpinion:
		res["matching"] = "no-opinion"
		res["matching_hint"] = "Dibs could not tell what this work touches, so it has " +
			"compared you against nothing: this is NOT a finding that you are working " +
			"alone. The built-in scorer matches your words against file PATHS; if none " +
			"of them name a file in this repository it has nothing to go on. Name a " +
			"file or package you expect to touch, or configure an embedding service " +
			"(-match-embed-url), which reads content rather than filenames"
	default:
		res["matching_hint"] = "no other agent is working on anything close to this. " +
			"you have the field to yourself"
	}
}

// agentsHint is the one line a model will actually read. A bare array of scored
// objects gets skimmed past; naming the action and the peer does not.
//
// Two things here were reported as wrong by agents on a live fleet, and both were.
//
// It told an agent to read the agent "BEFORE YOU START": in the return value of
// declare, which is how an agent declares it HAS started. There is no before
// left. The instruction an agent can actually act on is to check whether the work
// it has just announced duplicates something already under way, and to stand down
// if it does; so that is what this says now.
//
// And it asserted "agents doing the same work" flatly, at any score above the
// bar. A confident false positive is the expensive kind of error here, because an
// agent that believes it will stand down real work: the report came from one that
// had been told this at 0.196 on four generic build files. The claim is now
// scaled to the evidence, which is all the score ever supported.
func agentsHint(sug []Suggestion) string {
	joined, consider, opened := 0, 0, 0
	var top float64
	for _, s := range sug {
		switch s.Action {
		case "joined", "queued", "awaiting_director":
			joined++
			if s.Score > top {
				top = s.Score
			}
		case "opened":
			opened++
		default:
			consider++
		}
	}
	// Deliberately vague at the bottom. The scores are unitless and
	// scorer-relative, so the honest reading of a bar-clearing match is "worth
	// looking at", and only a strong one supports "the same work".
	claim := "may overlap yours"
	if top >= 0.5 {
		claim = "looks like the same work"
	}
	switch {
	case joined > 0 && consider > 0:
		return "you were placed in an agent whose work " + claim +
			", and there are others worth a look: read yours with read_space now and " +
			"stand down if you are duplicating what is already under way"
	case joined > 0:
		return "you were placed in an agent whose work " + claim +
			": read it with read_space now and stand down if you are duplicating " +
			"what is already under way"
	case opened > 0:
		// "No agent existed" overstates the same way: what happened is that nothing
		// cleared the threshold, which is a statement about the measurement.
		return "no existing agent cleared the match threshold, so one was opened for " +
			"you: the next agent whose declaration does clear it joins you here " +
			"instead of duplicating. A miss is not proof nobody else is on this"
	default:
		return "other agents may be doing overlapping work; " +
			"join_space to coordinate, or watch_space to just watch"
	}
}

// OpenWithPrediction fills in a new agent's footprint before opening it.
//
// An agent opened with no footprint can never be matched against, so it would sit
// on the board invisible to exactly the auto-join mechanism that gives it a
// point. Predicting from the topic at open time is what makes the first agent
// discoverable by the second agent.
func (e *Engine) OpenWithPrediction(ctx context.Context, op *core.Op) (core.Result, error) {
	if len(op.Predicted) == 0 {
		// The topic is the declaration here: it is what the agent is FOR.
		op.Predicted, _, _ = e.Predict(ctx, op.Text)
	}
	return e.Do(ctx, op)
}

// agentsNeedingFootprints reads the board once, coherently, and reports both the
// agents still missing a footprint and which agents exist at all: the second so
// the cache can forget the ones that do not.
func (e *Engine) agentsNeedingFootprints(ctx context.Context) (need map[string]string, live map[string]bool) {
	_, _ = e.query(ctx, func() core.Result {
		live = make(map[string]bool, len(e.state.Spaces))
		for id, ch := range e.state.Spaces {
			live[id] = true
			if len(ch.Predicted) > 0 || ch.Topic == "" {
				continue
			}
			if _, done := e.footprints[id]; done {
				continue
			}
			if need == nil {
				need = map[string]string{}
			}
			need[id] = ch.Topic
		}
		return core.Result{}
	})
	return need, live
}

// forgetFootprint drops one agent's cached footprint the moment that agent ends.
func (e *Engine) forgetFootprint(id string) {
	e.matchMu.Lock()
	defer e.matchMu.Unlock()
	delete(e.footprints, id)
}

// forgetDeadFootprints drops cached footprints for agents that no longer exist.
//
// A BACKSTOP, not the mechanism. Invalidation happens on the agent.reclaimed and
// agent.merged events (see publish), which have no window; this sweep catches
// anything that ends by a path those events do not cover, and bounds the map.
//
// The cache is keyed by agent id, and agent ids are DERIVED FROM THE DECLARATION,
// so identical work reuses one, and agents are now reclaimed automatically when
// their last member leaves. Left alone, a reclaimed agent's footprint stays here
// forever and is handed to whatever opens that id next: the new agent gets
// matched on the OLD agent's files, and the `if _, done := e.footprints[id]`
// guard in backfillFootprints means it never gets its own.
//
// The same shape as the announcement leak: state keyed by an agent id that
// outlived the agent. Announcements were the instance somebody found; this is the
// one that was looked for afterwards, in the same place.
func (e *Engine) forgetDeadFootprints(live map[string]bool) {
	e.matchMu.Lock()
	defer e.matchMu.Unlock()
	for id := range e.footprints {
		if !live[id] {
			delete(e.footprints, id)
		}
	}
}

// backfillFootprints predicts a footprint for any agent that has none.
//
// An agent records its file footprint when it is opened, so later declarations
// have something to match against. But an agent opened BEFORE the scorer finished
// indexing gets an empty one, and an empty footprint is permanent: nothing ever
// matches it, so nobody joins, so it never gains one. The agent sits on the board
// looking fine and silently coordinating nobody.
//
// That window is not rare. Embedding a repository takes minutes, the daemon
// serves throughout (deliberately), and a restart re-opens it every time. It was
// found by an end-to-end run against a real embeddings service, where the agent
// was opened two seconds before the index was ready.
//
// The footprint is a CACHE, not a fact: the join op records the joiner's own
// prediction and score, which is what replay reconstructs from (§4.3). So
// deriving a missing one here, at the edge, from the agent's topic costs nothing
// in determinism, and it is computed once per agent, not per declaration.
func (e *Engine) backfillFootprints(ctx context.Context, scorer overlap.Scorer) map[string][]core.PredFile {
	need, live := e.agentsNeedingFootprints(ctx)
	for id, topic := range need {
		p, err := scorer.Predict(ctx, topic, 40)
		if err != nil || len(p.Files) == 0 {
			continue
		}
		e.matchMu.Lock()
		if e.footprints == nil {
			e.footprints = map[string][]core.PredFile{}
		}
		e.footprints[id] = toPredFiles(p.Files)
		e.matchMu.Unlock()
	}
	e.forgetDeadFootprints(live)

	e.matchMu.RLock()
	defer e.matchMu.RUnlock()
	if len(e.footprints) == 0 {
		return nil
	}
	out := make(map[string][]core.PredFile, len(e.footprints))
	for k, v := range e.footprints {
		out[k] = v
	}
	return out
}

// OnRepoSeen is told, once per repository, where a registering agent is working.
//
// Work-overlap matching used to require -match-repo and was therefore OFF on
// every install that did not know to set it, which is the headline feature
// silent by default. There is no sensible constant to default the flag to: the
// daemon serves agents across every project open on the machine, so any repo
// baked in would be wrong for most of them.
//
// The agents know, though. Each one registers with a cwd, and the repository
// containing it is exactly the tree whose history is worth mining. So the
// daemon learns the answer from the fleet instead of from configuration.
//
// Called on the engine's own goroutine, so the callback must not block: the
// daemon's implementation hands the path to an indexer and returns.
func (e *Engine) OnRepoSeen(fn func(repo string)) {
	e.matchMu.Lock()
	defer e.matchMu.Unlock()
	e.onRepoSeen = fn
}

// noteRepoOf tells the daemon where a registering agent is working.
//
// Deliberately NOT deduplicated here. It used to be, and that was a bug with a
// long fuse: the daemon already dedupes, and it does so correctly, releasing
// its claim on a tree it could not read so a later attempt can retry. A second
// permanent dedup in front of that turned the first failure into the last word.
// Somebody granted the daemon access to the folder, registered again, and
// nothing happened, because this map had already written the repository off for
// the life of the process.
//
// One layer of dedup, owned by the side that knows whether the work succeeded.
func (e *Engine) noteRepoOf(cwd string) {
	if cwd == "" {
		return
	}
	e.matchMu.RLock()
	fn := e.onRepoSeen
	e.matchMu.RUnlock()
	if fn != nil {
		fn(cwd)
	}
}

// cwdForToken is where the holder of this token is working, or "".
//
// On the loop, and read-only. The first version called authRead directly from
// matchDeclaration, which runs on the CALLER's goroutine after Do has already
// dispatched into the writer loop: authRead writes e.seen, so two agents
// declaring at once was a concurrent map write, which Go turns into a fatal
// error that takes the whole board down. Every other authRead caller goes
// through query for exactly this reason.
//
// It does not use authRead even now. That function also spends a rate-limit
// token, and asking where an agent works is not an action the agent took.
func (e *Engine) cwdForToken(ctx context.Context, token string) string {
	if token == "" {
		return ""
	}
	res, err := e.query(ctx, func() core.Result {
		l := e.state.AgentByToken(token)
		if l == nil || l.Agent == nil {
			return core.Result{}
		}
		return core.Result{"cwd": l.Agent.CWD}
	})
	if err != nil {
		return ""
	}
	cwd, _ := res["cwd"].(string)
	return cwd
}
