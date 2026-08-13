# Changelog

Notable changes to Dibs. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **A board written by v0.0.4 or earlier is no longer opened.** `dibd` now says
  so on startup and names the record it will not read. Set the file aside
  (`mv ~/.dibs/ledger.jsonl ~/.dibs/ledger.jsonl.old`) and start it again; your
  work is untouched, the coordination history is not. This is the same clean
  break the 0.0.2 notes describe, applied to the half of the rename that was
  missed, and it is the last one.

### Fixed

- **Upgrading silently demoted every persistent agent to ephemeral.** The
  vocabulary rename changed op field names as well as op kinds, and a renamed
  field does not fail: `lane_kind` was simply not read, so the op applied with
  the field zero and replay reported success over a board that had quietly lost
  its persistent agents (no nonce resume, no coordinator eligibility) while a
  replayed `sweep` that recorded `dead_lanes` marked nobody dead. Every release
  up to v0.0.4 wrote those names. `state == fold(ledger)` was broken with
  nothing raised anywhere, which is the one outcome a hash-chained ledger exists
  to prevent, so the daemon now refuses the ledger instead of misreading it.

  The test that froze the on-disk field names had been guarding this since
  v0.0.0. The sweep rewrote its frozen list to match the new tags, and the
  comment explaining why the list must never be rewritten, which it left saying
  "renames the participant from `Agent` to `Agent`". The list is now
  fingerprinted, so a sweep that rewrites the words fails on the hash it cannot
  recompute.

- The startup check for an outdated board listed the vocabulary it had just been
  renamed *to*, so it recognised `register` and `declare` as obsolete words: on
  any replay failure it told the owner their current board came from 0.0.2 and
  to move it aside. It also only ran when replay had already failed, which the
  case above never does. It now runs before the fold, reads the whole ledger
  rather than the first fifty records, and its words are checked against the
  core rather than a second hand-maintained list.

- A coordinator claim that presented the right secret spent it even when the op
  was then refused, leaving the board with no coordinator and no way to appoint
  one short of restarting the daemon. Checking the secret and spending it are
  now separate: the claim is consumed once the grant is ledgered.

- The bound on git calls made the permission it needed impossible to grant. A
  first call against a macOS protected folder puts a dialog on the user's
  screen, and macOS shows that dialog on behalf of the requesting process: a
  20-second timeout killed git while the dialog was still up, leaving the prompt
  with nothing to grant to. The deadline is now four minutes, which is a person
  reading a dialog rather than a hang, and the error says that answering it and
  registering again is all that is needed.

- The daemon identified itself to macOS as `a.out`, the Go toolchain's default,
  so any privacy grant was recorded against a name shared with every other
  ad-hoc binary. `task install` now always sets `org.agenxy.dibs`, whether or
  not a signing identity is configured.

- A repository the daemon could not read was written off for the life of the
  process. Dedup lived in two places: the daemon's, which releases a tree it
  failed to read so a later attempt retries, and the engine's, which never
  cleared. Granting the daemon access and registering again therefore did
  nothing, which is the exact situation the retry existed for.

- `task install` removed `$DEST/agents`, a name that has never existed, and then
  copied over the live `dibs`. That reuses the inode, macOS invalidates the
  cached signature, and every later run is SIGKILLed with no message: precisely
  the failure the comment directly above that line warns about.

- The data directory really is `~/.dibs` now. The 0.0.3 notes below said so and
  the code did not: the vocabulary rename turned every "lane" into an "agent"
  and took `~/.lanes` with it, so two releases shipped writing to `~/.agents`, a
  generic name in the user's home that any number of other tools could claim.
  The same slip put a `.agents` directory inside your repository for monitor
  state. An existing directory is still found and used, under either old name,
  so no board moves and nothing is lost; `dibs doctor` names the one it opened
  and gives you the `mv` if you want it.

- `dibs mcp-config` published the Codex/TOML server block as `[mcp_servers.agents]`
  while the JSON block correctly said `dibs`. Half of that command's output has
  been wrong since 0.0.3.

- The Claude Code plugin did not work at all. Its `.mcp.json` declared
  `"command": "agents"`, a binary that has never existed, so the harness spawned
  it, failed, and showed a server that never started. The Claude Desktop manifest
  and the OpenCode README named the same missing binary.

- The panel resource was advertised as `ui://agents/board` while every other
  resource is `dibs://`.

- The signature-verification command in the README passed
  `--certificate-identity .../Agenxy/agents/...`, so anyone checking a release
  signature got a mismatch and had every reason to think the artifact was bad.

- On Linux `dibs configure --service` wrote `agents.service` and told the
  operator to run `systemctl --user enable --now agents`. systemd also had no
  guard against installing a second unit beside an old one, which launchd has
  had since the `com.agents.dibd` incident: two units on one data directory
  means the second fails the directory lock and reads as a service that will
  not start.

- `dibs doctor` told you to move an inherited data directory without mentioning
  that a service unit pins that path, so following the advice left the daemon
  starting against a directory that was gone.

- `dibs mcp-config` panicked with a Go stack trace on a `local.secret` shorter
  than 16 bytes, which is what a truncated or hand-edited one looks like.

- The README's install line said `brew install agenxy/tap/agents`, naming a cask
  that has never existed, so the first command a new reader runs did nothing.
  It also omitted `brew trust agenxy/tap`, which Homebrew 6 requires before it
  will load a cask from a third-party tap; without it the install fails with a
  trust error.

- The tap offered a stale `lanes` cask beside `dibs`, so `brew install
  agenxy/tap/lanes` still resolved to 0.0.2. It is now a `tap_migrations.json`
  entry, which moves an existing install across on the next `brew update`
  instead of leaving it on a version that gets no releases.

- A `git` the daemon ran against a tree could hang forever, and matching would
  wait on it in silence. On macOS `/usr/bin/git` dispatches into Xcode, and from
  a launchd agent against a protected folder (Desktop, Documents, Downloads) it
  blocks on an access prompt that can never be shown to a background process, so
  it never returns rather than failing. Found on a real machine: a `rev-parse`
  child sat there for four minutes, the indexing goroutine never finished, and
  because that repository was already latched as in-progress every later
  registration was deduplicated against work that would never complete.

  Every git the daemon runs is now bounded, and a tree it cannot read is named
  in the log and in `dibs doctor` along with the likely cause, instead of
  leaving matching quietly off. Unbounded work behind a deduplicating latch is a
  permanent silent failure, which is the shape this codebase keeps paying for.

### Changed

- **Work-overlap matching is on by default, and indexes every repository your
  agents work in.** It used to be gated behind `-match-repo`, so the feature
  this product exists for was silent on every install that did not know to set
  a flag. There was no constant to default that flag to, because the daemon
  serves agents across every project open on the machine.

  The fleet already knew the answer: every agent registers with a working
  directory, and the tree containing it is exactly the history worth mining. So
  each repository is indexed the first time an agent turns up in it, up to
  sixteen, and there is one index per repository. An agent is scored by the tree
  it is working in; an agent in a tree that is not indexed gets no semantic
  suggestions rather than someone else's, because a co-change model asked about
  another project's sentence answers confidently and wrongly.

  `-match-repo` remains only as a pre-warm, for a daemon started at login that
  should have an index ready before the first agent arrives. Closes #7.

### Added

- `task install` honours `DIBS_CODESIGN_IDENTITY`, signing both binaries with a
  persistent identity. macOS keys a privacy grant to a code signature, and the
  Go toolchain signs ad-hoc, so every rebuild is a different program to the
  system and any Files-and-Folders or Full Disk Access grant silently stops
  applying. That matters when checkouts live under Desktop, Documents or
  Downloads, where the daemon needs permission to read them at all.

- `dibs doctor` reports an ad-hoc signed daemon and says what it costs, because
  the symptom (matching worked, then quietly stopped after an install) points at
  everything except the signature.

- `task smoke`, in the gate: it runs the built binaries and asserts on what they
  actually print, against expectations written by hand rather than generated
  from the source. Everything above was invisible to a green suite because the
  rename edited the fixtures and the code together, so the tests agreed with the
  bug: `doctor_test.go` asserted `[mcp_servers.agents]`. A check the sweep cannot
  reach is the only kind that can catch the sweep.

## [0.0.4] - 2026-08-11

### Fixed

- The rename left the old name in places only running the binary would show:
  `dibs version` printed `agents`, `dibs doctor` looked for a binary called
  `agents` on PATH and for an MCP server of that name in harness configs,
  `mcp-config` generated a server block named `agents`, every error prefix and
  the "did you mean" suggestion said `agents`, the shell completions declared
  `#compdef agents`, and the daemon's self-signed certificate carried it as its
  common name. Found by exercising the CLI rather than reading the diff, which
  is how it should have been found before 0.0.3.

## [0.0.3] - 2026-08-11

### Changed

- **Lanes is now Dibs.** The name collided with an established project in the
  same niche, and described the opposite of what this does: everywhere else a
  "lane" is an isolated parallel workstream, while here it was an agent's
  identity on a shared board.
  - The CLI is `dibs`, the daemon is `dibd`, the environment is `DIBS_*`, the
    data directory is `~/.dibs`, and resources are `dibs://`.
  - `brew install agenxy/tap/dibs`. The old cask still installs 0.0.2 and is
    not updated further.
- **A lane is now an agent, and a lane of work is now a space.** Both concepts
  were called lanes, which is why the tool names never quite made sense. A space
  is where semantically-related work congregates, and what draws agents into one
  is a match rather than a rule, so "lane" was the wrong shape for it.
- **Tool names are verb-first and drop the noun where nothing else could be
  meant.** `register`, `resume`, `update`, `sign_off`, `check_in`, `board`,
  `declare`, `undeclare`, `open_space`, `join_space`, `leave_space`,
  `close_space`, `read_space`, `merge_spaces`, `watch_space`, `lock_space`,
  `unlock_space`, `post`, `announce`, `ack_announcement`, `admit`, `evict`,
  `send`, `read_mail`, `ack`.

### Breaking

- **A 0.0.2 board cannot be replayed by 0.0.3.** The ledger records op kinds by
  name and those names changed, so the daemon refuses to start rather than serve
  a board that disagrees with its own history. It says which word it found, the
  one command that fixes it, and asks the agent reading it to tell you what was
  set aside. Sorry: one clean break at 0.0.x beat two later.

### Fixed

- A ledger line that was valid JSON but carried no op panicked the daemon
  instead of being reported as corruption.
- `verify --json` dropped the corrective hint the prose keeps, so a board that
  had never run reported `open …: no such file` and nothing else on the surface
  agents read. Thanks to @shaurya703 (#17).

## [0.0.2] - 2026-08-11

### Added

- `dibs completion <bash|zsh|fish>` prints a completion script, generated from
  the verb table the CLI dispatches on so it cannot drift from the commands that
  exist. Thanks to @shaurya703 (#15).
- `dibs man` renders the manual page from the same help text `dibs help`
  prints, and releases ship `lanes.1` in the archive and the Homebrew cask, so
  `man dibs` answers after an install. Thanks to @shaurya703 (#16).

- Nine real Git configurations are now regression tests. Each builds actual
  repositories, registers agents through the actual server, and asserts on the
  warning an agent would receive: an orphan `--single-branch` clone, a vendored
  subtree, shallow clones with removed origins, case-variant remotes, a stale
  url after a rename, a `git replace` graft. Synthetic values proved the
  decision and never exercised the resolver, which is how several of the defects
  fixed in this release reached a review rather than a test.
- Two standards are now checked rather than remembered. `internal/hygiene`
  fails the build on a shell script entering the tracked tree, by extension or
  by shebang, and on an em dash in prose. The shebang is read the way `env`
  reads it, quoting included, and anything that cannot be shown to be shell-free
  is treated as shell. En dashes are flagged only when spaced,
  since a numeric range is correct typography.
- The board says which project each agent is in. A machine usually has more
  than one repository open, and agents in three of them all reported branch
  `main`, so the rows were indistinguishable. The project is resolved from the
  agent's working directory once, at registration, and recorded, so it names the
  project even when the agent is several directories inside it.

### Changed

- **Dibs is now Apache 2.0**, relicensed from MIT. Both are permissive; Apache
  grants patent rights from contributors explicitly, terminates the licence of
  anyone who sues over the covered code, and states that no trademark rights come
  with it. `NOTICE` retains the MIT notice for the contributions made under it.
- `dibs doctor` exits nonzero when it finds a problem, so a script can act on it
  rather than parsing the output. Thanks to @floze-the-genius (#6).
- The Homebrew tap moved from `agenxy/homebrew-agents` to `agenxy/homebrew-tap`,
  so the install line is `brew install agenxy/tap/lanes` rather than repeating
  the project name. GitHub keeps a redirect, so the old form still works and
  nobody who already tapped needs to act.
- The README leads with a worked example of a collision being caught, and the
  documentation says `dibs stop` wherever it used to say `pkill dibd`.
- The service identifier is `org.agenxy.dibs`.
- An unstamped build reports `devel+<revision>` rather than a version number
  that names a release it is not.
- Prose throughout uses ordinary punctuation.
### Fixed

- A refused port said only `bind: address already in use`. It now names the
  dibd already holding the address, or says the holder is something else and
  how to find it. It does not fall back to another port: clients resolve the
  daemon from a fixed address, so one that quietly moved would be a daemon
  nobody could find.
- `com.agents.dibd` was missing from the labels the service installer checks
  before writing a new unit, so upgrading from that vintage could leave two jobs
  contending for one data directory.
- Repository identity survives three Git configurations that previously made two
  clones of one project look like strangers: a shallow clone (which does not have
  its root commit, so it now records none rather than a boundary), `git replace
  --graft` (identity is read with replacement objects disabled), and
  `url.*.insteadOf` (the effective remote is resolved instead of the configured
  string).
- Repository identity is re-read when a directory becomes a repository. A path
  observed before `git init` was remembered as not-a-repository for the life of
  the daemon, so an agent there was filed as being nowhere: no project on the
  board, and no identity for anything else to reason with.
- Repository identity is re-read when a checkout path is reused. It was memoised
  by path with no expiry, so after `rm -rf project && git clone something-else
  project` a long-running daemon went on describing the repository that used to
  be there, missing collisions inside the new project and inventing them against
  the old. The Git common directory is now checked on every cache hit.
- A shell reached through a tracked symlink is caught. A link named
  `fixture-python` pointing at `/bin/zsh`, plus a file whose shebang named it,
  ran under zsh while the check passed: both the walker and the reader follow
  symlinks, so nothing ever saw the link itself.
- Repository identity is decided by positive evidence in three forms: the same
  Git common directory, the same canonicalised remote, or equal root-commit sets.
  Root sets known on both sides and unequal mean different projects; everything
  else is unknown, and unknown warns.
  - Remotes are compared case-insensitively, because every forge people use
    serves `Acme/Api` and `acme/api` as one repository.
  - Roots are compared for EQUALITY rather than overlap. `git subtree add`
    imports a dependency's whole history, so two unrelated projects that vendored
    the same library share a root commit, and treating that as proof fired the
    strongest signal Dibs has between strangers.
  - The remote outranks history, because history can legitimately differ inside
    one project: a `--single-branch` clone of an orphan branch, or a history
    rewritten by filter-repo, shares no commit with its sibling.
  - A consequence worth knowing: two forks have equal root sets and are treated
    as one project, which is usually right, since a fork's references normally
    name the upstream tracker.
- A ref such as `issue:42` matched across repositories, so two agents in two
  projects were told they were pursuing the same objective: the strongest signal
  Dibs emits, telling each to stop work nothing else was doing. Refs are
  repository-scoped now, using an identity recorded at registration. The
  same-repository case, including two linked worktrees and two clones of one
  upstream, still reports as before.
- `systemd` expands `$VAR` inside `ExecStart` even though it is not a shell, so
  a daemon path containing a dollar sign started whatever the environment said.
  Literal dollars are escaped.
- A path containing U+FFFE or U+FFFF passed validation and was then silently
  rewritten by the XML encoder, producing a LaunchAgent pointing at a different
  data directory. Characters XML cannot represent are refused.
- The corrective commands printed when an older service unit is found were not
  shell-quoted, so a path containing a space was not runnable as shown.
- `dibs stop --help --force` printed help and exited 0, while `--force --help`
  refused: an unknown argument is now refused whichever side of help it sits.
- The em-dash removal replaced placeholder glyphs with commas in places that are
  not prose: a zero timestamp rendered as `seen , ` in the CLI and on the board,
  table cells meaning "not applicable" became `, `, and several comments and
  error strings were left starting mid-sentence.
- Documentation promised automatic subagent stamping everywhere. The
  `PreToolUse` stamp exists only where a harness offers a hook Dibs can use
  without spawning a subprocess, which today means Claude Code. `dibs doctor`,
  the README, `SKILLS.md` and `SPEC-SUPERVISION.md` now say so.
- An agent whose working directory exceeded 128 bytes could not register at all.
  A cwd was bounded as if it were a name; it is a path. Any checkout a few levels
  inside a home directory hit it, and the refusal was of the whole
  `register`, not of the field.

- `dibs stop --help` stopped the daemon. The dispatch discarded its arguments,
  so asking a destructive command what it does performed it and exited 0.
  `dibs configure --service --help` had the same defect and wrote a LaunchAgent.
- Service units generated by `configure --service` mishandled ordinary paths. A
  directory containing `&` produced a plist launchd will not parse; on Linux,
  spaces split one path into several arguments, `%` expanded as a systemd
  specifier, and a newline could inject a second directive. Paths containing
  control characters are now refused rather than silently altered.
- A relative `DIBS_DIR` was written into the unit verbatim, so the service
  started against whatever it resolved to under the init system.
- Upgrading from a unit written by an earlier version created a second job for
  the same data directory. The directory lock refuses the second, which looks
  like a service that will not start; `configure --service` now stops and says
  how to remove the old one.
- `Daemon.IsStranger` canonicalised only one side of its comparison, so with
  `DIBS_DIR` set, or on a symlinked path, every daemon looked like a stranger
  including itself.


## [0.0.1] - 2026-08-10

### Fixed

- Repairs the release identity. See 0.0.0 below; that version cannot be
  installed with `go install` and is retracted in `go.mod`.

### Added

- `dibs stop`, which stops the daemon for this data directory and leaves other
  daemons on the machine running. The documentation previously said
  `pkill dibd`, which ends every fleet on the host.
- `dibs configure --service` writes a launchd or systemd unit, so the daemon
  survives a closed terminal and a reboot.

## [0.0.0] - 2026-08-09

**Retracted. Do not use.** The tag was moved after the Go module proxy had
cached it, and `sum.golang.org` is append-only, so `go install …@v0.0.0` fails
with a checksum error under `GOPROXY=direct` and serves an older tree through
the default proxy. A moved tag cannot be repaired.

First public release. Everything below is what v0 ships with rather than a list
of changes from something earlier: there is no earlier.

### Coordination

- Dibs, slots and advisory path claims over an append-only, hash-chained JSONL
  ledger. Replay is exact (`state == fold(ledger)`), so the persistence is the
  audit history; `dibs verify` checks the chain.
- Private mailboxes with typed messages: question, request, notify, handoff,
  with delivery receipts, deadlines and idempotent retries.
- Ephemeral and persistent agents; standing roles sleep as `dormant` with durable
  mailboxes and wake by resuming.
- MCP-native: 40 advertised tools over the 2026-07-28 stateless contract, with
  the legacy 2025-11-25 path for current hosts. Five more are callable but not
  listed: the lifecycle hooks a harness invokes on the agent's behalf. A tool an
  agent cannot correctly call is not a capability, it is a trap.
- A coordinator can retire a finished agent with `close_space`. Auto-opened agents
  end themselves when their last member leaves; an agent a human opened outlives
  its members on purpose, and until now nothing could ever end one, so a board
  accumulated finished agents permanently and E_LANE_LIMIT advised a fix that did
  not work for them. Refuses an occupied agent, and one holding an unacknowledged
  announcement.
- The human can act from the board panel, proving they are there with Touch ID
  (the admin password on machines without a sensor). **Binary releases do not
  include the Touch ID helper.** It is a small compiled Swift program that has
  to sit beside the binaries, and the release pipeline builds Go; a Homebrew or
  archive install therefore falls back to the admin password, which is a
  supported path rather than a broken one. `task install`, or a build from
  source on a Mac with the Xcode command line tools, produces it. Dibs reports
  the sensor as `unavailable` and sends you to the password: it never claims a
  human was checked when none was. What they get back is an
  ordinary agent identity, not a privileged one: every action is the same op an
  agent would send, so nothing in the state machine learns that humans exist.
  A cancelled request is reported as `abandoned`, never as a decline: a client
  that disconnects or a daemon shutting down is not a person who was asked and
  said no, and telling somebody they changed their mind about a prompt they never
  saw is the kind of claim this path exists to avoid.
  The check cannot be disabled by configuration: there is a scripted-verdict mock
  for development, but it lives behind a build tag, so the code that reads it is
  not compiled into a release binary at all. An environment variable is inert in
  a shipped build, and two tests, one in the untagged build, one against a real
  release daemon with the variable already set, hold that line.
- An agent whose chosen name is taken is told so, and told by what. Asking for
  `sol` and being handed `sol-4` used to be silent, so an agent could publish an
  address nobody could write to and never learn why the mail stopped. The suffix
  itself stays, a stale agent still owns its mailbox, and giving its name away
  would redirect somebody else's mail, but the note names the holder, says
  whether it is a live conflict or a retired agent holding an id the ledger still
  refers to, and points at reattach if the older agent is in fact you.
- Dibs-issued coordination keys. Opening or joining an agent hands the agent an
  opaque key; declared back in `refs`, later work is matched to that agent exactly
  instead of inferred from wording. Checked rather than trusted, a key the
  declaring agent does not hold is struck out, so copying one buys nothing, and
  inherited down a vouched parent/child lineage, which is what lets one
  coordination decision cover a whole fan-out of subagents.

### Duplicate-work matching (spaces)

- Agents declare work in their own words and are matched against work already in
  flight, using the repository's file layout and git co-change history. No model,
  download or network required for the floor.
- Optional embedding tier behind one endpoint, so MLX, llama.cpp, Ollama or a
  hosted API all satisfy it. An unreachable service degrades to the built-in
  scorer and records `degraded` rather than failing.
- `dibs calibrate` measures thresholds against your own repository's history and
  reports how much genuinely-related work clears the bar, not just a number.
  **Recalibrate if you measured a bar before this release**, because the runtime
  now gates on the quantity calibration actually measures. `dibs calibrate`
  scores one declaration against another; the runtime used to score a declaration
  against an agent's MERGED footprint, so the bar was measured on one thing and
  applied to another, and nothing said so. A candidate is now judged against the
  closest single live declaration: the same comparison, at last. The merged
  footprint was diluted by every other member, so the same pair scores higher
  now: measured on this repository, one scenario moved 0.29 → 0.45.
- Matching stays off until a repository is configured, and auto-join stays off
  until a threshold is measured.
- Repository identity comes from Git, not from the shape of two paths: linked
  worktrees are recognised as one repository, and separate clones as separate.
  Three-valued, so "Git is not installed" and "different repositories" are not
  the same answer. Resolved off the state loop, so no agent waits on `git`.

### Supervision

- Detects whether a spawned subagent is working, thinking, blocked or stuck, from
  process liveness, CPU duty cycle and transcript growth.
- Elapsed time is measured on a monotonic clock, so a sleeping machine does not
  read as a stalled fleet.
- Attribution survives detaching, daemonisation and reparenting: in Claude
  Code, a `PreToolUse` hook stamps a spawned command with its parent's agent.
- Reports and never acts: it hands back the command to resume a stalled child
  rather than running it.

### Surfaces

- Live web board: server-rendered, SSE-streamed, dark/light, no framework and no
  build step. Gated on an admin password the agents do not hold.
- Terminal board that degrades cleanly under pipes, redirection and `NO_COLOR`.
- `dibs doctor`, which names the fix rather than only the fault.
- Board panel as an MCP App, which fills from whichever carrier a host actually
  forwards, tool-result `_meta`, ordinary content, or by fetching the board
  itself, and says so plainly when a host forwards none, rather than showing an
  empty board that looks like a server fault.
- The MCP server DELIVERS its own plugin. `dibs://plugin` carries the actual
  files, manifest, hooks, skill, MCP server definition, so an agent with no
  network and no checkout can install one, and an ordered setup procedure where
  every step says how to check it took effect. On first registration an agent is
  told whether a plugin exists for the harness it just named, and whether its
  lifecycle hooks have ALREADY reached this daemon: installed on disk and
  actually loaded are different claims, and only the daemon can tell them apart.
  Harnesses with hooks but no wake path are told mail is still pull-only there,
  rather than being invited to stop checking.
- `check_in` costs the model half what it did. It is the one tool every agent
  must call every activation, and it returned the whole checkpoint twice: once
  in `content` and again, identically, in `structuredContent`. The duplicate
  existed for hosts that drop `_meta` and forbid an app from calling tools, where
  it is the panel's only carrier; a panel that has successfully called through
  the bridge proves the host is not one of those, so the copy stops. Measured:
  6472 model-facing bytes before, 3238 after, with `content` unchanged.
- `task panel:inspect` renders that panel against the live daemon and prints what
  it DREW as text, with switches to withhold each carrier on purpose. Diagnosing
  a panel from a screenshot is how the carrier bug survived a green suite, and
  `--unlock` drives the human lock so the unlocked panel is observable without a
  person putting a finger on a sensor.
- The panel marks an agent going out of touch, once, as it happens: that agent's
  line to the board retracts and returns amber. It is the board's most
  consequential change and it used to occur in silence, between two frames. A
  agent that changes state also TRAVELS to its new status group rather than
  vanishing from one and reappearing in another, so a change of state reads as
  one event instead of two unrelated edits: both directions, because recovery
  is worth seeing too.
- The board panel is drawn as a spine rather than a stack of cards. Records hang
  off the status rail instead of each sitting in a bordered, filled, bevelled
  box that repeated what the group headers already said; a reading of zero that
  needs no action steps back so the exceptions carry the row; name, current work
  and standing description are ranked instead of typeset alike; and an
  out-of-touch timestamp is marked uncertain with a dotted rule rather than
  struck through, which had said the one number worth reading was void.

### Platform

- Verified on macOS. Builds for Linux and arm64 on every push; behaviour on a GNU
  userland is unverified: see README §Platform.
