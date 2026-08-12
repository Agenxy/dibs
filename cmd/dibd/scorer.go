package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/overlap"
)

// Work-overlap scoring, installed at boot. See SPEC-CHANNELS.md.
//
// OFF unless a repository is named, and notify-only unless thresholds are given.
// Both defaults are deliberate:
//
//   - No repo means no index, and an index built from the wrong repository is
//     worse than none: it would match agents into agents on the strength of a
//     codebase they are not working in.
//   - Thresholds are unitless and scorer-relative. SPEC-CHANNELS.md originally
//     proposed 0.75; `dibs calibrate` on the Dibs repository itself returned
//     0.327. A shipped guess is wrong by a factor of two in one direction or the
//     other, and both failures are silent: everyone in one agent, or nobody in
//     any. So Dibs will suggest without being told a bar, and will not act.
//
// Mining runs in the background: it shells out to git over potentially thousands
// of commits, and the daemon must be answering agents long before that finishes.
type scorerFlags struct {
	// indexed is the set of trees already mined, keyed by the cwd that
	// introduced them, so each repository is indexed exactly once.
	discoverMu       sync.Mutex
	indexed          map[string]bool
	repo             string
	join             float64
	notify           float64
	history          int
	deadline         time.Duration
	director         bool
	autoJoin         string
	embedURL         string
	embedModel       string
	embedQueryPrefix string
	embedDocPrefix   string
	embedKey         string
	requested        bool
	// set records which knobs were given EXPLICITLY, by flag or environment.
	//
	// Without it, precedence was implemented as "is the value still its zero
	// value?", which silently makes 0 mean "unset". For a threshold, 0 is a
	// real setting: the -match-join help says so itself ("0 = suggest only,
	// never join"). So `-match-join 0` lost to a file's 0.5 and the daemon
	// auto-joined against an explicit instruction not to.
	set map[string]bool
	// degraded records that a configured embedding service could not be reached,
	// so the status can say results are weaker rather than letting a tier-0
	// answer pass for a tier-2 one.
	degraded bool
}

// defaultScorerFlags is the zero configuration, separated from flag
// registration so it can be constructed more than once: registerScorerFlags
// writes to the global flag set and panics if called twice.
func defaultScorerFlags() *scorerFlags {
	return &scorerFlags{history: 2000, deadline: 1500 * time.Millisecond, set: map[string]bool{}}
}

// markSetFlags records which match flags actually appeared on the command line.
// flag.Visit walks only those, which is the one way to tell `-match-join 0`
// from an absent -match-join.
func (f *scorerFlags) markSetFlags() {
	if f.set == nil {
		// A scorerFlags built as a struct literal (tests, and any future caller
		// that skips the constructor) has a nil map, and writing to one panics.
		f.set = map[string]bool{}
	}
	flag.Visit(func(fl *flag.Flag) {
		if name, ok := strings.CutPrefix(fl.Name, "match-"); ok {
			f.set[name] = true
		}
	})
}

func registerScorerFlags() *scorerFlags {
	f := defaultScorerFlags()
	flag.StringVar(&f.repo, "match-repo", "",
		"repository to mine for work-overlap matching (enables agent auto-matching; run `dibs calibrate` first)")
	flag.Float64Var(&f.join, "match-join", 0,
		"auto-join agents at or above this overlap score (0 = suggest only, never join)")
	flag.Float64Var(&f.notify, "match-notify", 0,
		"mention agents at or above this overlap score")
	flag.IntVar(&f.history, "match-history", 2000, "commits to mine for co-change history")
	flag.DurationVar(&f.deadline, "match-deadline", 1500*time.Millisecond,
		"give up on scoring after this long; declaring work never blocks on it")
	flag.StringVar(&f.embedURL, "match-embed-url", "",
		"OpenAI-compatible embeddings service for tier 2/3. Ollama, vLLM, TEI, "+
			"LM Studio, llama.cpp's server, or a hosted provider. Dibs owns the index "+
			"and only asks it to POST /v1/embeddings. Unreachable or slow degrades to "+
			"the built-in scorer and says so")
	flag.StringVar(&f.embedModel, "match-embed-model", "",
		"model name to request from that service (e.g. codefuse-ai/F2LLM-v2-4B)")
	flag.StringVar(&f.embedQueryPrefix, "match-embed-query-prefix", "",
		"marker prepended to a QUERY before embedding (default: inferred from the model name. "+
			"qwen3-embedding, nomic-embed, e5, arctic-embed and each BGE generation are "+
			"known). Retrieval "+
			"models are asymmetric; addressing one without its markers roughly halves how well "+
			"it separates related work from unrelated")
	flag.StringVar(&f.embedDocPrefix, "match-embed-doc-prefix", "",
		"marker prepended to a DOCUMENT before embedding (see -match-embed-query-prefix)")
	flag.StringVar(&f.embedKey, "match-embed-key", "",
		"bearer token for a hosted embeddings endpoint")
	flag.StringVar(&f.autoJoin, "match-auto-join", engine.AutoJoinDeclared,
		"who decides a match becomes a membership: declared (default: join only on a shared "+
			"ref, which both agents wrote down; everything else is proposed for the agent to "+
			"judge), always, or never")
	flag.BoolVar(&f.director, "match-director-required", false,
		"gate every matched join on a coordinator admitting it (admit). "+
			"Off by default: it serialises the fleet behind one agent")
	return f
}

// applyConfig folds the [match] table in, under the flags.
//
// Precedence is flag > environment > file > default, matching how `addr` already
// resolves. A flag has to win: overriding one setting for one run must never
// require editing the file everything else reads from.
func (f *scorerFlags) applyConfig(c MatchConfig) {
	// Environment FIRST, so the file fills only what is still unset.
	//
	// This comment claimed flag > environment > file while the code did flag >
	// file > environment: every env read happened later, by which point the file
	// value had already filled the slot and the guard `if x == "" ` could not
	// fire. So a dibs.toml naming a repository that no longer exists silently
	// beat a correct DIBS_MATCH_REPO, and matching stayed off with the
	// environment looking like it had been ignored, which it had.
	f.markSetFlags()
	f.applyEnv()

	for _, fill := range []struct {
		key string
		dst *string
		src string
	}{
		{"repo", &f.repo, c.Repo},
		{"embed-url", &f.embedURL, c.EmbedURL},
		{"embed-model", &f.embedModel, c.EmbedModel},
		// An EMPTY prefix is a real setting, not an absent one: a model that
		// documents no marker (bge-m3) is configured by passing "", and
		// Embed.SetAffixes treats both-empty as "disable markers" rather than
		// "restore detection". `-match-embed-query-prefix ""` must therefore beat a
		// file that names one, which `if x == ""` got exactly backwards.
		{"embed-query-prefix", &f.embedQueryPrefix, c.EmbedQueryPrefix},
		{"embed-doc-prefix", &f.embedDocPrefix, c.EmbedDocPrefix},
	} {
		if !f.set[fill.key] {
			*fill.dst = fill.src
		}
	}
	if !f.set["join"] {
		f.join = c.Join
	}
	if !f.set["notify"] {
		f.notify = c.Notify
	}
	if !f.set["history"] && c.History > 0 {
		f.history = c.History
	}
	// An EMPTY prefix is a real setting, not an absent one: a model that
	// documents no marker (bge-m3) is configured by passing "", and
	// Embed.SetAffixes treats both-empty as "disable markers" rather than
	// "restore detection". `-match-embed-query-prefix ""` must therefore beat a
	// file that names one, which `if x == ""` got exactly backwards.
	// Same for a bool: `-match-director-required=false` is an instruction, and
	// `if !f.director` let a file's `true` overrule it.
	if !f.set["director-required"] {
		f.director = c.DirectorRequired
	}
	if !f.set["auto-join"] && c.AutoJoin != "" {
		f.autoJoin = c.AutoJoin
	}
	if c.Deadline != "" {
		if d, err := time.ParseDuration(c.Deadline); err == nil {
			// `f.set`, not "does it still look like the default". Passing
			// `-match-deadline 1500ms` explicitly, which is a reasonable thing
			// to type when pinning behaviour: was indistinguishable from not
			// passing it, so a file's `deadline = "9s"` silently won and a
			// request that should have timed out at 1.5s ran for 2.2.
			if !f.set["deadline"] {
				f.deadline = d
			}
		} else {
			slog.Warn("ignoring unparseable match.deadline in dibs.toml",
				"value", c.Deadline, "err", err)
		}
	}
}

// applyEnv folds the environment in, under the flags and above the file.
//
// The environment is where a per-invocation override belongs, and where the
// embedding bearer token MUST live, since a file gets pasted into issues.
func (f *scorerFlags) applyEnv() {
	str := func(knob, env string, dst *string) {
		if f.set[knob] {
			return
		}
		if v, ok := os.LookupEnv(env); ok {
			*dst, f.set[knob] = v, true
		}
	}
	num := func(knob, env string, dst *float64) {
		if f.set[knob] {
			return
		}
		// LookupEnv, not Getenv: an explicit 0 is a setting, and a value that
		// does not parse is a mistake worth leaving to the lower tier rather
		// than silently becoming 0.
		if raw, ok := os.LookupEnv(env); ok {
			if v, err := strconv.ParseFloat(raw, 64); err == nil {
				*dst, f.set[knob] = v, true
			}
		}
	}
	str("repo", "DIBS_MATCH_REPO", &f.repo)
	num("join", "DIBS_MATCH_JOIN", &f.join)
	num("notify", "DIBS_MATCH_NOTIFY", &f.notify)
	str("embed-url", "DIBS_MATCH_EMBED_URL", &f.embedURL)
	str("embed-model", "DIBS_MATCH_EMBED_MODEL", &f.embedModel)
	str("embed-key", "DIBS_MATCH_EMBED_KEY", &f.embedKey)
}

// install builds the scorer and hands it to the engine. Never fatal: a
// coordination board that refuses to start because it could not read a git log
// is a broken board, not a careful one.
func (f *scorerFlags) install(ctx context.Context, eng *engine.Engine) {
	// Matching is always on. It used to be gated on -match-repo, so the feature
	// this product exists for was silent on every install that did not know to
	// set a flag, and there was no constant to default that flag to: the daemon
	// serves agents across every project open on the machine, so any baked-in
	// path is wrong for most of them.
	//
	// The fleet already knows the answer. Every agent registers with a cwd, and
	// the tree containing it is exactly the history worth mining, so each
	// repository is indexed the first time an agent turns up in it. -match-repo
	// survives only as a pre-warm: it indexes a tree before anybody registers,
	// which is worth it for a daemon started by launchd at login and worth
	// nothing otherwise.
	eng.OnRepoSeen(func(cwd string) { f.indexDiscovered(ctx, eng, cwd) })

	if repo := f.repo; repo != "" {
		eng.SetMatchStatus(engine.MatchStatus{Phase: engine.MatchIndexing, Repo: repo})
		go f.bringUp(ctx, eng, repo)
		return
	}
	eng.SetMatchStatus(engine.MatchStatus{
		Phase: engine.MatchOff,
		Hint: "no repository indexed yet: each one is indexed when an agent first " +
			"registers from it, so this turns itself on. -match-repo only pre-warms a tree",
	})
	slog.Info("work-overlap matching indexes each repository an agent registers from",
		"pre-warm", "-match-repo <path>, or [match] repo = \"…\" in dibs.toml")
}

// bringUp indexes the repository and publishes the scorer.
//
// Separated from install so that one function is lifecycle (is this configured,
// when does it run) and this one is the work. Every early return here records a
// STATUS as well as logging, because a feature that switched itself off quietly
// is indistinguishable from one that is working and found nothing.
func (f *scorerFlags) bringUp(ctx context.Context, eng *engine.Engine, repo string) {
	start := time.Now()
	topCtx, cancelTop := context.WithTimeout(ctx, gitDeadline)
	defer cancelTop()
	// #nosec G204,G702 -- no shell is involved: exec.Command passes argv directly,
	// so a path cannot inject arguments however it arrived. It now can arrive
	// from an agent's own registration rather than only an operator flag, which
	// is why that matters: -C takes the value as a working directory, never as
	// part of a command line.
	root, err := exec.CommandContext(topCtx, "git", "-C", repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		// Every failure below sets a status as well as logging. A feature
		// that silently switched itself off is indistinguishable from one
		// that is working and found nothing.
		eng.SetMatchStatus(engine.MatchStatus{
			Phase: engine.MatchOff, Repo: repo,
			Hint: "matching is off: " + repo + " is not a git repository. " +
				"Point -match-repo at a checkout with history. Dibs mines co-change " +
				"from git log, and cannot match without it",
		})
		slog.Warn("work-overlap matching disabled: not a git repository", "repo", repo, "err", err)
		return
	}
	dir := strings.TrimSpace(string(root))

	offBecause := func(what string, err error) {
		// Every failure sets a STATUS as well as logging: a feature that
		// switched itself off silently is indistinguishable from one that is
		// working and found nothing, and only the log knew the difference.
		eng.SetMatchStatus(engine.MatchStatus{
			Phase: engine.MatchOff, Repo: dir,
			Hint: "matching is off: " + what + " (" + err.Error() + ")",
		})
		slog.Warn("work-overlap matching disabled: "+what, "repo", dir, "err", err)
	}

	cc, err := overlap.MineCoChange(ctx, dir, overlap.CoChangeOptions{MaxCommits: f.history})
	if err != nil {
		offBecause("could not read git history", err)
		return
	}
	lex, err := overlap.NewLexical(ctx, dir, cc)
	if err != nil {
		offBecause("could not list files", err)
		return
	}
	scorer := f.withSidecar(ctx, lex)
	eng.SetScorerForRepo(dir, scorer, engine.MatchConfig{
		JoinThreshold: f.join, NotifyThreshold: f.notify, Deadline: f.deadline,
		DirectorRequired: f.director,
		AutoJoin:         f.autoJoin,
		// The tree the index was built from, so auto-join can ask whether the
		// declaring agent is even in it.
		Repo: dir,
	})
	mode := "suggest only"
	if f.join > 0 {
		mode = "auto-join at " + strconv.FormatFloat(f.join, 'f', 3, 64)
		if f.director {
			mode = "director-gated at " + strconv.FormatFloat(f.join, 'f', 3, 64)
		}
	}
	phase := engine.MatchReady
	if f.join == 0 {
		phase = engine.MatchNoThreshold
	}
	if f.degraded {
		phase = engine.MatchDegraded
	}
	eng.SetMatchStatus(engine.MatchStatus{
		Phase: phase, Scorer: scorer.ID(), Repo: dir,
		Files: lex.Files(), Commits: cc.Commits(),
	})
	slog.Info("work-overlap matching ready",
		"repo", dir, "files", lex.Files(), "commits", cc.Commits(),
		"scorer", scorer.ID(), "mode", mode,
		"took", time.Since(start).Round(time.Millisecond))
	// A retrieval model addressed symmetrically still works, still ranks, and
	// separates related from unrelated work about half as well. That is
	// invisible from the outside: nothing errors, matching just gets quietly
	// worse, so it is said once, at the moment it becomes true.
	// An index quietly smaller than the repository reports READY and then fails
	// to match work touching the files it never saw.
	if em, ok := scorer.(*overlap.Embed); ok {
		if missing := em.Unreadable(); len(missing) > 0 {
			slog.Warn("some tracked files are not in the embedding index",
				"count", len(missing), "first", missing[0],
				"consequence", "work touching them can never be matched",
				"cause", "unreadable at index time: a broken symlink, a permission, "+
					"or a file removed between `git ls-files` and reading it")
		}
	}
	if em, ok := scorer.(*overlap.Embed); ok && !em.Recognised() {
		q, d := em.Affixes()
		if q == "" && d == "" {
			slog.Warn("embedding model is being addressed symmetrically",
				"model", f.embedModel,
				"why", "its name matches no retrieval convention Dibs knows",
				"cost", "roughly half the separation between related and unrelated work",
				"fix", "pass -match-embed-query-prefix (and -match-embed-doc-prefix if the "+
					"family uses one), or run `dibs calibrate` to measure the difference")
		}
	}
	if f.join == 0 {
		// Said once, at boot, because a silently-suggest-only board looks
		// identical to a broken one from the outside.
		slog.Info("no join threshold set: agents will be suggested but never joined automatically; " +
			"run `dibs calibrate` and pass -match-join")
	}
}

// withSidecar wraps the built-in scorer in an embedding service when one is
// configured (tiers 2 and 3).
//
// markersGiven reports whether the operator stated a retrieval convention,
// including the empty one.
//
// Split out because it is the whole decision, and because testing it through
// withSidecar would need a live embeddings endpoint: the probe failing returns
// the fallback scorer, so the assertion would never reach the thing it means to
// check. A test that cannot fail for the right reason is not a test.
func (f *scorerFlags) markersGiven() bool {
	return f.set["embed-query-prefix"] || f.set["embed-doc-prefix"] ||
		f.embedQueryPrefix != "" || f.embedDocPrefix != ""
}

// An absent or unreachable sidecar is a DOWNGRADE, never an outage: matching
// keeps working on paths and co-change, and the recorded provenance says which
// tier actually answered. Returned even when the probe fails, because Remote
// falls back per call: a sidecar that starts late still gets used.
func (f *scorerFlags) withSidecar(ctx context.Context, base overlap.Scorer) overlap.Scorer {
	url, model, key := f.embedURL, f.embedModel, f.embedKey // resolved in applyConfig
	if url == "" {
		return base
	}
	em := overlap.NewEmbed(url, model, key, 2*time.Minute)
	// An explicit convention beats an inferred one: a model family Dibs has
	// never heard of still has one, and its operator knows it.
	//
	// Including an explicitly EMPTY one. This tested the VALUES rather than
	// whether they were given, so `-match-embed-query-prefix ""` resolved
	// correctly through precedence and was then dropped here, and the inferred
	// marker came back on the wire. SetAffixes documents both-empty as "disable
	// markers, do not detect again": a real configuration for a model whose
	// card states it needs none, which is exactly when an operator reaches for
	// it. Same zero-is-a-value fault, one layer down from the config.
	if f.markersGiven() {
		em.SetAffixes(f.embedQueryPrefix, f.embedDocPrefix)
	}
	if err := em.Probe(ctx); err != nil {
		f.degraded = true
		slog.Warn("embeddings service unreachable: matching continues on the built-in scorer",
			"url", url, "err", err, "fix", "start the service, or drop -match-embed-url")
		return base
	}
	// Indexing embeds every chunk in the repository, so it happens here, once,
	// before the scorer is published: never on an agent's request path.
	if err := em.Build(ctx, f.repo); err != nil {
		f.degraded = true
		// Say what to DO about it. The overwhelmingly common cause is scale:
		// a 7,400-file repository produced 58,710 chunks and the service gave
		// out partway through, which reads as "embeddings are broken" when it
		// means "this repository is larger than this service can index".
		//
		// It matters more than a normal warning because tier 0 cannot relate
		// work that shares neither words nor file history, which is the case
		// tier 2 exists for. Falling back is honest and it is not equivalent.
		slog.Warn("could not index with the embeddings service: continuing on the built-in scorer",
			"url", url, "err", err,
			"likely", "the repository is larger than this service can index in one pass",
			"fix", "point -match-repo at the subtree your agents actually work in, "+
				"or serve a faster embeddings backend; matching still works on file "+
				"names and commit history, but cannot relate work that shares neither")
		return base
	}
	slog.Info("embeddings service ready", "url", url, "model", model, "chunks", em.Chunks())
	return em
}

// indexDiscovered indexes a repository an agent turned up in.
//
// Every distinct tree, not the first one: the daemon serves agents across every
// project open on the machine, and an index built from project A answers
// confident nonsense about project B's sentences. One scorer per repository is
// the only shape that is correct for a fleet, and it is what removes the need
// for anybody to name a repository at all.
//
// Bounded, because indexing is git log mining and a machine could in principle
// have an agent registered from anywhere. Past the bound the tree is named in
// the log rather than silently ignored.
func (f *scorerFlags) indexDiscovered(ctx context.Context, eng *engine.Engine, cwd string) {
	f.discoverMu.Lock()
	if len(f.indexed) >= maxIndexedRepos {
		f.discoverMu.Unlock()
		slog.Info("work-overlap matching is at its repository ceiling; this tree is not indexed",
			"cwd", cwd, "ceiling", maxIndexedRepos)
		return
	}
	if f.indexed == nil {
		f.indexed = map[string]bool{}
	}
	if f.indexed[cwd] {
		f.discoverMu.Unlock()
		return
	}
	f.indexed[cwd] = true
	f.discoverMu.Unlock()

	go func() {
		if err := repoReadable(ctx, cwd); err != nil {
			// Forget it, so the slot is not spent and a later agent from a real
			// checkout still gets an index.
			f.discoverMu.Lock()
			delete(f.indexed, cwd)
			f.discoverMu.Unlock()
			// SAY SO. The first version of this returned here in silence, and a
			// daemon that cannot read the tree looked identical to one that had
			// simply not been told about it: matching stayed off, doctor said
			// "no repository indexed yet", and nothing anywhere named the cause.
			// That is the failure mode this file's own comments warn about.
			slog.Info("work-overlap matching skipped a tree it cannot read",
				"cwd", cwd, "err", err, "likely", tccHint(cwd))
			eng.SetMatchStatus(engine.MatchStatus{
				Phase: engine.MatchOff,
				Hint: "an agent registered from " + cwd + " but the daemon cannot read it (" +
					err.Error() + "). " + tccHint(cwd),
			})
			return
		}
		f.bringUp(ctx, eng, cwd)
	}()
}

// maxIndexedRepos bounds the number of trees mined. High enough for any real
// fleet, low enough that a stray registration cannot start unbounded work.
const maxIndexedRepos = 16

// repoReadable reports why a tree cannot be indexed, or nil if it can.
//
// Returns the error rather than a bool because the two reasons are completely
// different problems for the operator: "not a git checkout" is nothing to fix,
// while "operation not permitted" is a daemon that has been denied access to a
// directory it was pointed at, and only the second deserves an explanation.
func repoReadable(ctx context.Context, cwd string) error {
	// BOUNDED, because git does not always fail: it hangs.
	//
	// On macOS /usr/bin/git is a shim that dispatches into the selected Xcode.
	// Run from a launchd agent against a TCC-protected folder (Desktop,
	// Documents, Downloads) it blocks on an access prompt that can never be
	// shown to a background process, so it waits forever. Found on a real
	// machine: a rev-parse child of the daemon sat there for four minutes, the
	// indexing goroutine never returned, and every later registration was
	// deduplicated against a tree that was still "in progress".
	//
	// Unbounded work behind a deduplicating latch is a permanent silent
	// failure, which is the shape this file exists to avoid.
	ctx, cancel := context.WithTimeout(ctx, gitDeadline)
	defer cancel()
	// #nosec G204 -- argv is passed directly, no shell; cwd comes from an
	// agent's own registration and is only ever used as a working directory.
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("git did not answer within %s", gitDeadline)
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return err
}

// gitDeadline bounds any git the daemon runs against a tree it was told about.
// Long enough for a cold cache on a large repository, short enough that a hang
// is reported rather than waited on.
const gitDeadline = 20 * time.Second

// tccHint explains the macOS case, which is the one that looks like a bug.
//
// A launchd agent does not inherit the Full Disk Access granted to the app that
// installed it, so a daemon started at login is refused ~/Desktop, ~/Documents
// and ~/Downloads while the same binary run from a terminal reads them fine.
// Every git call against such a path fails, which empties the project label and
// leaves matching with nothing to index, and none of that names the cause.
func tccHint(cwd string) string {
	if runtime.GOOS != "darwin" || !protectedOnMacOS(cwd) {
		return "check the path exists and is a git checkout"
	}
	return "this is inside a macOS protected folder (Desktop, Documents, Downloads). " +
		"A daemon started by launchd is not granted access to those, and /usr/bin/git " +
		"BLOCKS rather than failing, so the call times out. A checkout outside those " +
		"folders needs no permission at all and is the better answer; granting dibd " +
		"Full Disk Access also works, but a coordination daemon should not need it. " +
		"Matching reads file paths and commit subjects, never file contents"
}

// protectedOnMacOS reports whether a path is inside a TCC-protected folder.
func protectedOnMacOS(cwd string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, name := range []string{"Desktop", "Documents", "Downloads"} {
		guarded := filepath.Join(home, name)
		if cwd == guarded || strings.HasPrefix(cwd, guarded+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
