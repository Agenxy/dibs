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

**Before the tag, a DIFFERENT model reads the whole release surface.** Not
optional, and not the author's own review: several versions have been spent
fixing things a careful reader would have caught, and the reader who misses them
is reliably the one who wrote them. `task review:release` runs it against the
last tag.

The value is the second opinion, so run something that is not what wrote the
code. What matters is what it is pointed at: this repository's recurring bug
classes (validation in `Apply` instead of `Admit`, an op that changes state
without advancing the serial, a renamed json tag, anything that reports success
while doing nothing) and its newest authorisation paths. Fix what it finds, run
`task ci`, and go round again until a pass turns up nothing worth fixing.

**Before the tag, re-run the harness survey**, because the harnesses move and the
trackers that used to hold this ("recheck on release", issues #23, #25, #26, #27,
#28, #31) were closed into this step. The checkouts live at `~/Desktop/harnesses`
on the machine this was written on; `git fetch origin` each and read
`origin/HEAD`, never the local branch. For each, the predicate that decides the
row, and the rule that a capability in source is not a behaviour:

- **Codex** (`openai/codex`): the executor is in main (`CoreHookMcpExecutor` in
  `core/src/session/session.rs`), and since 0.153 a user or local-plugin hook
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
- **Gemini CLI**: `initialize` 2025-06-18 over `httpUrl`, hooks are `command`
  only; `plugins/gemini-cli` covers it (2026-09-12).

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
