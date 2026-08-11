package overlap

// The evaluation set that matches what agents actually send.
//
// # Why the existing calibration is not this
//
// Calibrate samples COMMITS, predicts files from each commit MESSAGE, and calls a
// pair related when the two commits share a file. Production predicts from an
// AGENT DECLARATION (a paragraph about goals and intentions) and asks whether
// two agents are doing the same work. Those are different input distributions and
// different questions, and only the easy one was ever measured.
//
// A commit message names its subsystem, because it is written after the fact
// about files that exist: "fix retry backoff in auth/token.go". A declaration is
// written before the fact about work that does not exist yet, in the vocabulary
// of goals: "greening main's cross-cutting gates in my lane". The first is nearly
// a query for the files it touched. The second shares its most distinctive words
// with every other declaration in the same repository. CI, gates, docs, runtime
// and a path-token scorer turns exactly those into "evidence".
//
// That gap is why a smaller embedding model scored as well as a larger one and we
// believed it: on commit messages, retrieval is easy enough that model capacity
// barely shows. It is also why the fleet hit false positives within an hour of
// the thresholds being calibrated as sound.
//
// # Where these labels come from
//
// Observed, not invented. Every case below is a real declaration pair from a
// running fleet of Claude Code and Codex agents, with the ground truth known
// because the humans and agents involved said what they were doing and two of the
// agents independently reported the misclassification.
//
// Details are genericised: subsystem names, paths and identifiers are replaced
// with equivalents that preserve the SHAPE that matters: the vocabulary overlap,
// the directory disjointness, and whether the repository is shared. The failures
// reproduce on the genericised text, which is the point; if they did not, the
// fixture would be recording trivia rather than structure.
//
// # What the metric has to be
//
// PRECISION, weighted far above recall, and measured at the operating point
// rather than averaged. The costs are not symmetric:
//
//	false positive: an agent is told it is duplicating work that it is not, and
//	                 stands down something real, or spends a turn arguing its way
//	                 out of a lane. Both happened.
//	false negative: two agents collide, which is where they were before Lanes
//	                 existed. The system's own documentation already tells agents
//	                 a low score proves nothing.
//
// A missed match costs what you had before. A false match costs work you were
// going to do.

// GoldenCase is one labelled pair of agent declarations.
type GoldenCase struct {
	Name string
	A, B GoldenDecl
	// Same is the ground truth: are these two agents doing work that would
	// collide?
	Same bool
	// Why records the human-legible reason, so a failing case explains itself
	// rather than printing two paragraphs and a number.
	Why string
}

// GoldenDecl is everything an agent tells Lanes about itself: the whole signal,
// not just the sentence the scorer currently reads.
type GoldenDecl struct {
	Text   string
	Dirs   []string
	Refs   []string
	CWD    string
	Branch string
}

// GoldenSet is the labelled evaluation set. Small and real beats large and
// synthetic here: these are the cases the system actually got wrong.
var GoldenSet = []GoldenCase{
	{
		Name: "same repo, different subsystems, shared vocabulary",
		A: GoldenDecl{
			Text: "CLI, web UI, docs and JS dependency work plus cross-cutting full-scan gates. " +
				"Green-main work, dependency CVEs, terminology gates, feature PRs.",
			Dirs:   []string{"/repo/cli", "/repo/ui", "/repo/docs", "/repo/sdks/js"},
			Refs:   []string{"pr:1231", "gate:glossary"},
			CWD:    "/repo",
			Branch: "feat/terminal-native",
		},
		B: GoldenDecl{
			Text: "Runtime C++ and build-farm lane: outage recovery, migration off a personal " +
				"account to a service account, CI throughput, runtime and gate infrastructure.",
			Dirs:   []string{"/repo/tools/ci", "/repo/.github"},
			Refs:   []string{"incident:farm-down", "gate:codeowners"},
			CWD:    "/repo",
			Branch: "fix/farm",
		},
		Same: false,
		Why: "Both say gates and CI, and a path-token scorer matched them at 0.196 on " +
			"Justfile, ci.yml, CMakeLists.txt and a generated file: none of which either " +
			"agent declared. Their declared dirs are disjoint and each says explicitly it " +
			"is not touching the other's.",
	},
	{
		Name: "different repositories entirely",
		A: GoldenDecl{
			Text: "Private life-portfolio site and its desktop launcher: icon, launcher binary, " +
				"audit gates, front-page defects.",
			Dirs:   []string{"/other/site", "/other/launcher"},
			CWD:    "/other/site",
			Branch: "session/provenance",
		},
		B: GoldenDecl{
			Text: "CLI, web UI, docs and JS dependency work plus cross-cutting full-scan gates.",
			Dirs: []string{"/repo/cli", "/repo/ui", "/repo/docs"},
			CWD:  "/repo",
		},
		Same: false,
		Why: "Auto-joined on Justfile and ci.yml after the first agent merely READ one file " +
			"in the other repository as a format reference. Reported from inside: 'the " +
			"shared-file evidence is generic'.",
	},
	{
		Name: "genuinely the same work, declared differently",
		A: GoldenDecl{
			Text: "Fixing the stale owner handle in CODEOWNERS and MAINTAINERS: a renamed " +
				"account silently voids required owner review.",
			Dirs: []string{"/repo/.github"},
			Refs: []string{"gate:codeowners"},
			CWD:  "/repo",
		},
		B: GoldenDecl{
			Text: "Required-review gate is not firing. Chasing why approvals are not being " +
				"enforced on protected branches.",
			Dirs: []string{"/repo/.github"},
			Refs: []string{"gate:codeowners"},
			CWD:  "/repo",
		},
		Same: true,
		Why: "The prose shares almost no vocabulary. 'stale owner handle' against 'approvals " +
			"not enforced', but the declared directory and the ref are identical. This is " +
			"the case text similarity alone cannot get right and structure gets right for free.",
	},
	{
		Name: "same subsystem, genuinely different work",
		A: GoldenDecl{
			Text: "Adding a secret-scanning gate to CI.",
			Dirs: []string{"/repo/.github"},
			Refs: []string{"pr:1191"},
			CWD:  "/repo",
		},
		B: GoldenDecl{
			Text: "Cutting CI wall-clock: caching the toolchain and pruning the matrix.",
			Dirs: []string{"/repo/.github"},
			Refs: []string{"goal:fast-ci"},
			CWD:  "/repo",
		},
		Same: false,
		Why: "The hard case for structure: identical directory, different objectives. Neither " +
			"dirs nor text separates these; the refs do. A system that joins on dirs alone " +
			"will get this wrong in the other direction.",
	},
	{
		Name: "one agent read a file in the other's area, and is not working on it",
		A: GoldenDecl{
			Text: "Reading another project's icon manifest as a FORMAT REFERENCE only, to " +
				"copy the layout convention into my own launcher. Not modifying it.",
			Dirs:   []string{"/other/launcher"},
			CWD:    "/other/site",
			Branch: "session/provenance",
		},
		B: GoldenDecl{
			Text: "Icon and manifest work across the desktop app bundle.",
			Dirs: []string{"/repo/app/icon"},
			CWD:  "/repo",
		},
		Same: false,
		Why: "Reading is not working on. The declarations are ABOUT the same artifact type " +
			"and share its whole vocabulary, so semantic similarity is at its most confident " +
			"exactly where it is wrong; the cwd and the declared dirs are what separate them.",
	},
	{
		Name: "handoff continuation: the same work, one agent after the other",
		A: GoldenDecl{
			Text: "Landing the supervisor daemon for the build farm: liveness checks, runner " +
				"restart, disk trim.",
			Dirs: []string{"/repo/tools/ci"},
			Refs: []string{"goal:farm-up"},
			CWD:  "/repo",
		},
		B: GoldenDecl{
			Text: "Picking up the farm supervisor after the previous agent stopped: finishing " +
				"the restart path and its tests.",
			Dirs: []string{"/repo/tools/ci"},
			Refs: []string{"goal:farm-up"},
			CWD:  "/repo",
		},
		Same: true,
		Why: "The easy positive, included so the set cannot be gamed by a classifier that " +
			"simply says no. Everything agrees: refs, dirs, vocabulary.",
	},
	{
		Name: "same file, opposite intentions",
		A: GoldenDecl{
			Text: "Deleting the legacy retry shim now that every caller is migrated.",
			Dirs: []string{"/repo/internal/core"},
			Refs: []string{"pr:900"},
			CWD:  "/repo",
		},
		B: GoldenDecl{
			Text: "Adding jitter to the legacy retry shim's backoff.",
			Dirs: []string{"/repo/internal/core"},
			Refs: []string{"pr:901"},
			CWD:  "/repo",
		},
		Same: true,
		Why: "COLLIDING is the question, not 'doing the same thing'. One agent is deleting " +
			"what the other is editing: the most expensive collision there is, and the two " +
			"declarations share a directory and a subject while their refs differ. A rule " +
			"that trusts differing refs to mean 'unrelated' gets this wrong, which is why " +
			"refs may confirm a match and must not veto one.",
	},
	{
		Name: "same work, one agent declared almost nothing",
		A: GoldenDecl{
			Text: "Rewriting the queue promotion path so a promoted agent keeps the membership " +
				"it was matched with.",
			Dirs: []string{"/repo/internal/core"},
			CWD:  "/repo",
		},
		B: GoldenDecl{
			Text: "queue promotion",
			CWD:  "/repo",
		},
		Same: true,
		Why: "A terse declaration is common and must still match. Structure alone cannot see " +
			"it (B declared no dirs and no refs) so this is where semantic similarity has " +
			"to carry the case, and where dropping the text scorer would cost real recall.",
	},
}

// The label-vs-identifier cases moved to internal/core, where they can run
// against the real namespace table and the real cascade rather than a proxy.
