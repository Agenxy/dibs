# AGENTS.md: orientation for agents working on Dibs

Read `PHILOSOPHY.md` first; it is the decision procedure. This file is the map.
`docs/ARCHITECTURE.md` is the territory: how it fits together, and the four bug
classes that keep recurring here. If you are an agent *using* Dibs rather than
changing it, you want `SKILLS.md` (also served over MCP as `dibs://skills`).

## What this repo is

A Go daemon (`dibd`) + CLI (`dibs`) implementing a local coordination service for
fleets of AI agents. Agents connect over **MCP**. See `SPEC.md` (living, not frozen,
change it when reality disagrees, and record why).

## Layout

| Path | Role |
|---|---|
| `internal/core/` | **Pure deterministic state machine.** No I/O, no goroutines, no wall clock. `Apply(op, now) → (result, events)`. The invariant `state == fold(ledger)` lives or dies here. |
| `internal/ledger/` | Append-only, hash-chained, encrypted JSONL. Truth. |
| `internal/engine/` | Single-writer event loop over `core`. All impure inputs (liveness probes, clocks) are *recorded into ops* so replay reproduces decisions. |
| `internal/mcp/` | MCP surface: tools, resources, `subscriptions/listen`. Dual-version (2026-07-28 + legacy). |
| `internal/blobstore/` | Content-addressed attachment bytes, encrypted at rest. Outside the replay model by design. |
| `internal/web/` | Human board (SSE + htmx; one ~320-line script in `internal/assets/board.js`, no framework or build step). |
| `cmd/dibd/` | Daemon + auth gate. `cmd/dibs/` | CLI. |

## Rules you must not break

1. **`internal/core` stays pure.** No I/O, no `time.Now()`, no randomness, no network. If
   you need an impure input, record it in the `Op` so replay applies the same decision.
2. **An op is ledgered iff it changed replayable state.** The engine ledgers exactly when
   the serial advanced. Never let those disagree.
3. **Non-deterministic things live outside the core** as derived, rebuildable views.
   Losing a derived view must not lose coordination state.
4. **Advisory, not coercive.** Declaring work never fails. Don't add blocking semantics.
5. **The board may WAKE an agent, and may not steer one.** Reaching an idle agent so it
   can read its own mail is the product: a message service whose recipient must already
   be running is a polling API. There are two routes and no others: `[wake.exec]`, argv
   from the operator's config, which spawns a process and is the one Dibs can confirm;
   and the session socket the harness publishes, which needs no config and is BEST
   EFFORT, because the receiver decides whether to accept a peer message and sends no
   receipt. A session in bypassPermissions mode holds them. Both carry one fixed sentence,
   no shell, nothing an agent said, rate limited, logged. Everything past "you have mail"
   is still forbidden: no prompt injection, no session management, no deciding what an
   agent does next. See `WAKE-MECHANISMS.md` §5 and §5b, which argued against both for
   months and now records why that was wrong.
6. **Honesty in errors.** Every error carries a `hint` that tells a drifted agent the
   corrective call.

## Working here

**Once per clone**, before anything else. `task` itself is pinned by mise, so a
fresh checkout has no runner until this has run:

```bash
mise trust && mise install
```

Then:

```bash
task ci                   # THE gate: vet, lint, -race, build, 7 e2e suites + the
                          # sidecar contract, and the SPEC §17 coverage floor.
                          # Includes cross-compilation to all three release
                          # targets and govulncheck.
go build ./...            # quick build
go test -race ./internal/...
gofmt -w <files>          # always
```

**Check the exit status of `task ci`, not its output.** Grepping for "checks
passed" and concluding green is a mistake already made in this repository: one
suite printed a failure line that did not match the pattern, and a red run was
reported as green.

Lint alone: `mise exec golangci-lint@2.12.2 -- golangci-lint run`

Use **bun**, never npm, for any JS/TS work.

**No shell scripts.** Not for build steps, task running, hooks or test
harnesses: see `CONTRIBUTING.md` for why. Python with a `uv` shebang and PEP 723
inline dependencies, or Go, or the existing runner.

## Design for 2026, not for what shipped

PHILOSOPHY.md rule 9 is a standing architectural position and not a compatibility
note: **MCP 2026 is where this is going, and the 2025 path is a transitional
courtesy to harnesses that have not migrated.** New work is designed the 2026 way
first and made to work on the legacy path afterwards, never the reverse.

The practical form of that rule: a feature shaped around `initialize` and a
long-lived session has to be redesigned when the session goes away, and 2026 is
stateless. When a 2025-only assumption is load-bearing, say so where it is made.

## Easy to miss

Things that have cost real time here, none of which are visible in the diff:

- **Validation belongs in `core.Admit`, never `core.Apply`.** `Apply` is the
  fold, and replay runs it over ops accepted by *older code*. A rule added there
  is retroactive: the daemon refuses its own ledger and will not boot.
- **`e.query()` sends on `e.ops`, which is nil on a zero-value `Engine`.** A test
  that builds `&Engine{}` and calls an exported wrapper **blocks forever** rather
  than failing. That is why the decision is split from the wrapper (`noteChild`,
  `reportStallLocked`): test the decision.
- **A parameter you declare but never read is invisible from outside.** The call
  succeeds and the effect silently does not happen; the schema is the only thing
  an agent can see. `TestEveryDeclaredParameterIsReadByAHandler` enforces this.
- **Renaming an op's json TAG is a silent data-loss bug, not a rename.** A
  retired op *kind* stops the fold, loudly. A retired *field* stops nothing: the
  op applies with that field zero and replay reports success. `lane_kind` →
  `agent_kind` shipped in every release to v0.0.4 and silently demoted every
  persistent agent to ephemeral on upgrade. Rename the Go identifier freely; the
  tag is frozen, and `TestLedgerFieldNamesAreFrozen` fingerprints the list
  because the same sweep that renames tags will happily rewrite the list that
  guards them, which is exactly how this got through.
- **A path is evidence on ONE computer**, and this is the rule this
  repository has learned six separate times. /workspace/repo on two
  machines is two unrelated directories, so every lookup, comparison and
  projection that takes a path needs the host beside it: the claim rule
  (round 3), differentProjects (8), hook resolution (36), a `file:` remote
  in sameRepoIdentity (62) and then in differentProjects again (64), the
  hook-health diagnostic and the compact board (65). Each was fixed where
  it was named and the next round found another place.
  `TestNoExportedLookupTakesADirectoryWithoutAMachine` ends that by shape
  rather than by memory: nothing exported from `internal/core` may take a
  `cwd`, `dir` or `root` without a host. A caller with no machine to state
  passes `""`, which every lookup reads as "no evidence of difference", and
  has to type it. If you are adding a comparison rather than a lookup, the
  rule still applies and the guard cannot see it: ask what the path means
  on the other side.

- **`core` imports nothing, so everything can import `core`.** Rule 1 is
  usually read as a restriction on core; its more useful half is the
  permission it grants everyone else. Three packages kept a hand-copy of a
  rule core already states, each with a comment giving the reason: "core
  may not import this package", "overlap sits below core and does not
  import it". Both were true about the direction they named and beside the
  point: core depends on nothing, so it is at the bottom, and a package
  that needs one of its rules calls it. When you find yourself about to
  copy a constant or an eight-line function out of core and write a drift
  guard for the copy, import core instead.
  `TestCoreImportsNothingThatCouldMakeItImpure` is what keeps that true.

- **A sweep rewrites the tests and comments that exist to catch the sweep.**
  After any find-and-replace across the tree, read the diff of every *guard*
  first: frozen-string tables, retired-vocabulary lists, fixtures, and the prose
  explaining why they must not change. One of those comments was left reading
  "renames the participant from `Agent` to `Agent`", and the guard beneath it had
  been turned into a list of the current vocabulary.
- **`light-dark()` takes colours only.** Using it for a number or a keyword is
  invalid at substitution and falls back to `initial`: silently. This shipped a
  completely unreadable board past 155 passing browser checks.
- **The space e2e scores against this repo's own git history**, which changes
  with every commit. It measures its bar at runtime. Never assert an absolute
  score; assert the property.
- **`SKILLS.md` has a copy at `internal/mcp/skills.md`** because `go:embed`
  cannot reach above its package. The root file is canonical;
  `skills_embed_test.go` fails if they drift. Edit the root one and copy.
- **A failing probe is usually a broken probe.** Before concluding the product is
  broken, check that your measurement is sound: assert your setup steps
  succeeded. Three false alarms in one session came from this.
- **The wake path's refusals are Debug-level.** A healthy board makes many,
  so they are invisible by default, and an hour went into a two-host suite
  whose wake had stopped firing before anyone could see why ("called Dibs
  recently"). `DIBS_LOG_DEBUG=1` on `dibd` shows them, and the remote e2e
  runs its hub that way and prints the tail of the hub log on a failed wake.
- **A long lived bridge must remember nothing about this machine.** The
  stdio bridge read `local.secret` once at spawn and sent that string for the
  life of the process, and resolved the board's address once and retried
  against it forever. Both are machine facts the machine can change under a
  running process, and both failed silently and permanently: a board reset
  mints a new secret, so nine live sessions got 401 from every Dibs call with
  a working bridge, a reachable daemon and no corrective action an AGENT can
  take, the remedy being "restart your harness". This is PHILOSOPHY rule 9
  arriving from an unexpected direction: 2026 is stateless, so a long lived
  bridge has to behave as though it were spawned for each request, and the
  per-call plugins (pi, opencode) get that for free by actually doing so. The
  credential is refreshed in `guardedTransport`, which is the one place every
  credential-bearing request passes through, so every caller and every
  reconnecting stream gets it without remembering to; the address is
  re-resolved on retry and on stream reconnect, which is free on the happy
  path because a stale address has exactly one symptom. If you add a third
  thing the bridge reads at startup, ask what happens to a session when an
  operator changes it.

- **A harness's contract is a measurement, not a memory.** The wake path sent
  `hookSpecificOutput.additionalContext` on Stop and nothing else, under a
  comment citing Claude Code's documentation as saying that keeps a
  conversation going. The current documentation says it "does not by itself
  block the stop", and `decision: "block"` is what continues one. Every wake
  delivered on that path had been landing nowhere, with the daemon recording
  the mail as delivered: the product's one promise, failing silently, on a
  citation that had gone stale. `stop_hook_active` moved too, and in a
  direction that makes it dangerous to wire up: it now means "a Stop hook is
  configured", which is always true where Dibs is installed, so treating it as
  "this turn was already continued" would switch every delivery off. When a
  guard's premise is a sentence from somebody else's docs, re-read the docs
  before trusting the guard, and prefer a rule the daemon can check itself:
  freshness, not a field the harness defines.

- **A hook TYPE can be unavailable for a hook EVENT, and the harness says so
  in a line nobody reads.** Both of Dibs' SessionStart hooks were `mcp_tool`,
  which Claude Code resolves against the session's connected MCP clients:
  there are none that early, so the hook is skipped and exits 1. 307 of those
  errors across 22 projects over six weeks, every session start, all ours, and
  the event whose whole job is delivering mail to a reopened window had never
  once fired. Two lessons, and the second is the one worth keeping. A warning
  printed on every single session start is decoration, not a signal, so
  "somebody would have noticed" is false for anything that fires always. And
  it was found while reading the harness binary for an unrelated question,
  which is the argument for reading a harness you depend on when nothing is
  known to be wrong, rather than only when something is. Grepping a
  transcript history for the harness's own error strings takes a minute and
  would have found it any day in those six weeks.

- **"Passes locally, fails on CI" is usually the two machines producing
  DIFFERENT ERRORS for the same event, not noise.** The bridge-follows-a-moved-
  board test failed once on CI and passed 30 times in a row locally. It was not
  timing. With connection pooling on, the bridge holds an idle connection to the
  old board, so closing that board makes the next send fail with ECONNRESET;
  locally the pool happened to be empty, the send dialled fresh, and it got
  ECONNREFUSED. `dialFailed()` matches refused and NOTHING else, deliberately,
  because a reset cannot prove the request was not already applied and a
  retried claim is worse than a failed call. So the test had been asserting
  something the product promises not to do, and passed only when the pool was
  empty. The fix was in the test (keep-alives off, so every send is the fresh
  dial that re-resolution is for), and the lesson is the diagnosis order: read
  the ERROR the failing machine reported before reaching for "flaky". The error
  text named the syscall, and the syscall named the branch.

- **When you fix "a rule applied at one site and not its siblings", the grep
  finds the siblings that LOOK right and the test finds the one on the path.**
  Four places assigned an agent's identity payload wholesale where `update`
  merged it. A grep for the assignment found three, all three were fixed, and
  the regression test went on failing: the fourth was `takeActivation`, which
  is the path a session MOVE takes, and a session move was the event the whole
  bug report was about. The three that read like the answer were the three that
  were not on the path. So write the failing test FIRST, fix every site the
  grep names, and then believe the test rather than the grep. It is also why
  the probe has to assert its own setup: an earlier version of that test passed
  against the unfixed code because a register without a pid and a kind was
  answered as a duplicate retry, nothing was applied, and nothing was dropped
  because nothing happened.

- **A diagnosis is only half a hint until you have found the DEFAULT behind
  it.** The socket wake route was documented, correctly, as held by a Claude
  Code session in bypassPermissions mode, and then described as though nothing
  could be done: "no message a sender can construct changes that". True about
  the wire and false about the situation. The receiving client's rule is mode
  PARITY with an explicit `crossSessionInbound` overriding it, so one line in
  the receiving user's settings turns the free route on, and three documents
  and a `dibs doctor` warning had all stopped at the problem. Measured
  2026-09-23 on 2.1.280 the only way that settles it: two headless sessions,
  both bypassPermissions, the same frame written to each socket, one answering
  `peer_message_hold` with cause `no-mode-asserted` and no turn, the other
  starting a turn with a `kind:"peer"` origin. Note also that the rule reads
  backwards and is not: bypass is strict at the door precisely because it is
  permissive downstream. When this repository documents somebody else's gate,
  the finding is not complete until it says whether the gate has a switch, who
  owns the switch, and what flipping it costs. Same shape as
  `internal/appfirewall`, which is the pattern to copy: detect, explain, print
  the fix, never run it.

- **The macOS firewall swallows a hub, it does not refuse one.** The
  Application Firewall is on by default and filters inbound connections per
  executable. A binary that is not in its list is not rejected: the handshake
  completes, the connection sits in the accept queue and `Accept` never fires,
  so the client's `connect()` succeeds and its read hangs while the daemon
  shows an open socket, a clean log and no traffic. It asks the person at the
  keyboard, and an install over ssh has nobody to ask, so the default stands
  with no prompt and no line anywhere. The first hub deployment lost an
  afternoon to it, and two probes made it longer: `/usr/bin/nc` served the same
  address perfectly (it is IN the list) and a re-signed copy of nc did too
  (matched as the binary it was copied from), both of which read as proof that
  the firewall was innocent. What settled it was a listener of our own in each
  language, ad-hoc signed and unlisted: BOTH hung on the LAN address and both
  answered instantly on loopback, and `--listapps` explained every case.
  `--getappblocked` says "permitted" throughout and is not an oracle for this:
  it reports whether an explicit block rule exists, not whether connections
  arrive. `internal/appfirewall` reports it now, from `dibd` at startup and
  from `dibs doctor`. The fix is printed, never run, and on an MDM-managed Mac
  there is no command to print: `socketfilterfw` refuses every modifying verb,
  so the hint names the settings pane instead.

- **A doc-count guard is only as good as the spellings it knows.** The tool
  count appears in six documents and has now gone stale three times in three
  different shapes: a plain wrong number, `one tool of forty-two`, and
  `Tools (40)`. The check read `N tools` only, so two of those passed it for
  months and were found by a person reading. When you add a claim a test
  guards, add the SHAPE of the claim too.
- **The CHANGELOG is release surface, and nothing runs it.** Every other claim
  here is checked by something: the tool count, the e2e suite count, the frozen
  json tags, the drift between `SKILLS.md` and its embedded copy. The changelog
  is prose written by hand about code written by tests, so it goes stale in the
  one direction no gate looks: silently, while `task ci` stays green. It fell
  twelve review rounds behind during the v0.0.7 cycle, and the fix is not a
  guard, it is remembering that a user-visible change which is in the diff and
  not in the changelog has shipped without being announced. `docs/REVIEW.md`
  asks the reviewer to check it now.

- **A PASSING probe proves nothing until you have seen it fail.** The same
  session that produced those three then wrote four consecutive versions of one
  test that passed against the code they were written to catch: the fixture gave
  a sibling the read end of a pipe instead of the write end; the next killed the
  process under test instead of its parent; the next pointed the daemon at an
  empty data directory, so it exited at once for want of a local secret. Each
  looked like a green test of a real guarantee. For anything that asserts a
  behaviour you have just added, run it against the commit before the fix and
  watch it fail. `git worktree add --detach <dir> HEAD` makes that thirty
  seconds, and it is the only thing that distinguishes a regression test from a
  decoration.

## Where the reasoning lives

| Doc | What it settles |
|---|---|
| `PHILOSOPHY.md` | What Dibs is, is not, and the test for any change |
| `SPEC.md` | The protocol and its guarantees (living) |
| `REQUIREMENTS.md` | The measured real-world failure that defines the requirements |
| `WAKE-MECHANISMS.md` | How agents learn about events; what was rejected and why |
| `docs/NETWORK.md` | More than one machine: host identity, claims that cross hosts, liveness, and why the hub does not run remote wake routes |
| `SPEC-ATTACHMENTS.md` | Blob/attachment design |
| `docs/ARCHITECTURE.md` | Structure, request path, invariants, recurring bug classes |
| `SKILLS.md` | Agent-facing: how to USE Dibs well (served as `dibs://skills`) |

## Distribution

Releases are cut by tagging: the workflow re-runs the whole gate against the tagged
commit, then publishes signed artifacts, attaches an MCP Bundle (`dibs.mcpb`, built by
`tools/mcpbundle` from the same binaries) with its digest, and publishes `server.json`
to the official MCP Registry as `io.github.Agenxy/dibs` with a `packages` entry naming
that bundle. No source is updated by hand: if those disagree, that is a bug in the
pipeline, not a chore.

**The Homebrew cask is the one step that still needs a person, and it is worth knowing
why.** The tap requires changes through a pull request, so GoReleaser pushes the updated
cask to a `cask-<version>` branch of `agenxy/homebrew-tap` over SSH; merging it is a
click. It cannot open the PR itself: a deploy key can push and cannot call the API, which
is the trade the key was chosen for. Until that branch is merged, the release is
published and `brew upgrade` still serves the previous build, so the release job printing
green does not mean the cask moved. This used to read as though tagging did everything;
it does not, and a documented guarantee that quietly needs a click is worse than one that
says so. Closing it properly means a workflow in the tap that watches for `cask-*`.

**Before the tag, read the release surface.** Several versions were spent
fixing things a careful reader would have caught, and the reader who misses
them is reliably the one who wrote them, so this step exists. **Do it
yourself** unless the operator asks for otherwise: that is their standing
instruction, given after an automated loop spent ~2.96M tokens of their model
allowance across nine rounds they had already asked to stop.

`task review:release` runs a reader against the last tag, and what it costs
is the operator's own allowance, 240k to 410k tokens a round. It is for a
decision worth that, not for "let's see if anything turns up". Say what it
will cost and what would make it the last round BEFORE starting one, run at
most two after a substantive change, and stop the moment the person paying
says so, whatever the last round found. Every round's own fixes give the next
round something to find, so "the last one found two real things" is what the
loop will say forever; it is not an argument for another.

What to point the reading at, however it is done: this repository's recurring
bug classes (validation in `Apply` instead of `Admit`, an op that changes
state without advancing the serial, a renamed json tag, a rule applied at one
call site and not its siblings, anything that reports success while doing
nothing) and its newest authorisation paths. Fix what it finds, run `task ci`,
and then decide about shipping. The exit condition is that decision, not a
clean round.

**Before the tag, re-run the harness survey**, because the harnesses move and the
trackers that used to hold this ("recheck on release", issues #23, #25, #26, #27,
#28, #31) were closed into this step. The checkouts live at `~/Desktop/harnesses`
on the machine this was written on; `git fetch origin` each and read
`origin/HEAD`, never the local branch. For each, the predicate that decides the
row, and the rule that a capability in source is not a behaviour:

- **Codex** (`openai/codex`): the executor is in main (`CoreHookMcpExecutor`, now in
  `codex-rs/core/src/hook_mcp_executor.rs` and used from
  `codex-rs/core/src/session/session.rs`; the path here said `core/src/…`
  and cost a grep). **Two flags now look like the one to set and only one is**:
  `mcp_2026_07_28` governs Dibs; `codex_apps_mcp_2026_07_28` is the hosted
  `codex_apps` HTTP server only and explicitly not third-party HTTP or local
  stdio servers. Since 0.153 a user or local-plugin hook
  is UNTRUSTED until reviewed and dropped silently until then, which is why
  0.153.4 "fired nothing" on 2026-09-05. Install the plugin from this checkout
  (`codex plugin marketplace add <checkout>`, `codex plugin add dibs@dibs`),
  run `dibs codex-hooks --trust`, then `codex exec` once and watch
  `/api/hook-health`'s poll count rise by two (SessionStart, Stop): measured
  2026-09-19 on 0.155.0-alpha.9.2. `--dangerously-bypass-hook-trust` on
  `codex exec` is the control that proves the gate is trust. Over `url`,
  `features.mcp_2026_07_28 = true` alone negotiates `server/discover`
  2026-07-28; `tools/list` only, no `resources/list` (2026-09-12).
- **Claude Desktop**: the app's own clients (`claude-ai/0.1.0` for chat,
  `local-agent-mode-<server>/1.0.0` per configured server) both `initialize`
  2025-11-25 and never send `server/discover`, measured 2026-09-15 on 1.52386.6
  through a `dibs mcp-stdio` entry in `claude_desktop_config.json` with
  `DIBS_LOG_RPC=1` on the daemon. Re-measure the same way, then REMOVE the entry:
  it shadows the plugin's server in Code-tab sessions (WAKE-MECHANISMS.md §3).
- **opencode**: `git grep 2026-07-28 origin/HEAD -- packages` outside tests;
  empty as of 2026-09-12. The wake path (`plugins/opencode`) works.
- **Hermes**: its `mcp` extra pins `mcp==2.0.0`, which implements 2026-07-28; what
  a session negotiates is unmeasured. Run one against the scratch daemon.
- **Pi**: `git grep -il modelcontextprotocol origin/HEAD -- packages/*/src`; a
  lockfile hit is not an MCP client. None as of 2026-09-12.
- **Gemini CLI**: `initialize` 2025-06-18 over `httpUrl` (2026-09-12,
  unchanged). Its HOOKS moved: as of 2026-09-25 (20f7075) the types are
  `command`, `http` and `prompt`, the hook input carries a real `session_id`
  and a `transcript_path`, and `BeforeAgent` takes `additionalContext`. So the
  two sentences this file used to carry about Gemini (command-only, no session
  id, therefore the directory fallback) are about `plugins/gemini-cli` now and
  not about the harness. Re-read the predicate here rather than the row.

Update the survey table in `README.md` (re-date its introduction) and the rows
in `WAKE-MECHANISMS.md` with what was MEASURED, and put the date on each. A row
that was not re-measured keeps its old date, which is the honest state.

**`task release VERSION=<the next version>` is the one step before the tag.**
This used to name a literal `0.0.6`, which is the version already tagged: the
command as written refuses, because a release that goes backwards would leave
every installer offering an older build than the one before it. An instruction
that cannot be followed is worse than none, and this one sits at the step where
somebody is following instructions exactly. It claims the
changelog's `## [Unreleased]` section for that version and stamps every manifest that
states one, then stops: tagging publishes, so it stays yours to do. Doing it by hand is
how two manifests sat at `0.0.0` through five releases, and the tagged commit is now
checked against its own tag, so the release fails rather than shipping a version no file
in it names.

**A failed release burns the version, and that is the rule working.** Release
tags here are immutable: `refs/tags/v*` refuses deletion, update and
non-fast-forward, because a tag that can move is a tag nobody can pin. The
tag workflow re-runs the whole gate against the tagged commit BEFORE it
publishes, so a tag that fails leaves a tag pointing at a commit with no
release behind it, and there is no way to repair that tag. The next attempt
is the next version. v0.0.8 went this way on 2026-09-22, on a board defect
that had been written off as a flaky browser check an hour earlier; nothing
was published, which is the point. Two consequences worth knowing before you
tag. Run the gate locally to a PASS first, and treat "it only fails sometimes
and passes on CI" as a bug you have not understood rather than as noise.
And `task release` cannot express this situation: it claims `## [Unreleased]`
and refuses a version that is not newer than the changelog's top section, so
re-stamping means folding the burned section back under `[Unreleased]` and
running it for the next number. Leave a stub for the dead version saying why,
because somebody will find the tag and look.

**Trunk-based, deliberately: `main` is always the release candidate.** There are no
release branches, and adding one would create a second place that has to agree with main
about what is shipping, which is this repository's most expensive recurring bug (see the
drift guards for `skills.md`, the plugin copies, the tool count, the vocabulary). Topic
branches live hours, not weeks. What is going into the next version is `## [Unreleased]`.

**We do not pay to be listed** (PHILOSOPHY.md rule 8). Several MCP directories now gate
submission behind a fee, and the answer is no every time, however good the traffic
numbers look. Free listings, PRs to community lists, and registries that index from the
official one are the whole strategy. When a directory says "publish to the Official MCP
Registry and we will pick you up", that is the strategy working.

Before submitting to a community list, READ ITS RULES. Several set a minimum age or star
count and auto-close everything else; submitting anyway spends a maintainer's attention
to no purpose and is the kind of thing that gets a project remembered for the wrong
reason.

## Testing expectations

Behavioural tests, not coverage theatre. Every guarantee in `SPEC.md` that can be tested
should have a test that fails if it regresses. Replay determinism, advisory-not-coercive
semantics, and the honesty rules are the ones that matter most: see
`TestRedundantObjectiveIsCaught` and `TestConcurrentFileWorkIsNotAnAlarm` for the
shape: each encodes a *decision*, with the reasoning in the comment.
