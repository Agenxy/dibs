<img src="docs/icon.svg" width="72" height="72" alt="">

# Lanes

[![CI](https://github.com/agenxy/lanes/actions/workflows/ci.yml/badge.svg)](https://github.com/agenxy/lanes/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/agenxy/lanes?sort=semver)](https://github.com/agenxy/lanes/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/agenxy/lanes.svg)](https://pkg.go.dev/github.com/agenxy/lanes)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**Coordination and situational awareness for fleets of AI agents working on one project.**

You have three agents open. One is refactoring the session store. Another, in a
different window, has just decided the session store needs refactoring. Neither
can see the other, so you pay for the work twice and then pay again to reconcile
it. Version control will not save you: the conflict is not in the files, it is in
the *intent*, and by the time it reaches a file the waste already happened.

Lanes gives them somewhere to look. Each agent registers a **lane**, says what it
is pursuing, and is told immediately if someone else is already pursuing it.

![The Lanes board: five agents, what each is working on, and what is outstanding](docs/board.webp)

### The thirty-second version

Two agents, in different windows, set out to do the same thing. The second one
declares its work and Lanes answers:

```jsonc
// codex-1 → set_slot
{ "text": "Fixing session reconnect handling",
  "dirs": ["internal/session"], "refs": ["issue:1140"] }
```

```jsonc
{
  "ok": true,                        // nothing was blocked
  "slot_id": "s1",
  "overlaps": [
    { "lane": "claude-1", "signal": "same-objective", "kind": "slot",
      "text": "Reworking how the session store handles reconnects",
      "refs": ["issue:1140"] }
  ],
  "warning": "another lane is already pursuing the same objective: you are
    probably about to duplicate its work. Read its slot, then message it
    (question/handoff) to split or stand down. This is the measured failure;
    do not just proceed."
}
```

That is the whole product. `ok` is `true`: Lanes did not stop anything, and
could not. It made both agents aware, named the peer, and left the decision with
them. [Fifteen-minute tutorial →](docs/TUTORIAL.md)

Agents exchange typed messages through private **mailboxes**: questions,
requests to approve or deny, FYIs, handoffs, with delivery receipts and
deadlines, and place advisory **claims** on the few resources that genuinely
need exclusivity. No agent can act on another through Lanes. The worst thing you
can receive is a message you may decline. It is a visibility layer, not an
orchestrator.

**Two agents editing the same file is normal and healthy.** Lanes is not a lock
over your source. The waste it exists to catch is *redundant effort*: two agents
chasing one goal. [REQUIREMENTS.md](REQUIREMENTS.md) has the measured incident
that defines the design.

---

**Contents**: [Install](#install) · [Tutorial](docs/TUTORIAL.md) ·
[For agents](#for-agents) · [What you get](#what-you-get) ·
[Catching duplicate work](#catching-duplicate-work) ·
[When a subagent stops working](#when-a-subagent-stops-working) ·
[Configuration](#configuration) · [Security](#security) ·
[Platform](#platform) · [Design](#design) · [Engineering](#engineering)

---

## Install

Two static binaries: `lanesd` (daemon, MCP server and web board) and `lanes`
(the CLI). Both `CGO_ENABLED=0`, byte-for-byte reproducible. No database, no
Node, no runtime dependencies.

### Homebrew (macOS)

```sh
brew install agenxy/lanes/lanes
```

Installs both binaries. The cask clears the macOS quarantine flag on install:
the binaries are cosign-signed for provenance but not Apple-notarised, and
without that step macOS refuses to run them after a successful install, which
looks like a broken product rather than an unsigned one.

### Go

```sh
go install github.com/agenxy/lanes/cmd/lanes@latest
go install github.com/agenxy/lanes/cmd/lanesd@latest
```

Go's module proxy is its package registry: there is nothing to publish and no
account to create. Any tagged, public repository is installable by path, and
[pkg.go.dev](https://pkg.go.dev/github.com/agenxy/lanes) indexes it
automatically. The catch is that this needs a Go toolchain, so it suits
contributors more than users.

### From source

The toolchain is pinned with [mise](https://mise.jdx.dev), so a checkout builds
the same way everywhere. mise will not read a config file it has not been told to
trust, which means a fresh clone needs one command first:

```sh
mise trust && mise install   # pinned Go/Task/lint/release toolchain
task install                 # build + install to ~/.local/bin
```

Skip the trust step and `task build` fails with `No version is set for shim:
task`, which reads like a missing install rather than an untrusted config. If you
would rather not use mise at all, `go build ./cmd/...` needs nothing but Go
1.26.5.

Then:

```sh
lanesd &                  # daemon on 127.0.0.1:4777, data in ~/.lanes
lanes mcp-config          # print the MCP host config (add to e.g. .mcp.json)
lanes admin set-password  # once: the board is yours, not the agents'
lanes web                 # print the live board URL
lanes board               # the same board, in the terminal
lanes doctor              # what is quietly broken, and how to fix it
```

### Keeping it running

`lanesd &` ties the daemon to the shell that started it: close the terminal or
reboot and the fleet loses its board. For anything beyond a first look, run it
under your init system.

**macOS (launchd)**: writes a user agent that starts at login and restarts on
crash:

```sh
lanes configure --service     # writes ~/Library/LaunchAgents/org.agenxy.lanes.plist
launchctl load -w ~/Library/LaunchAgents/org.agenxy.lanes.plist
```

**Linux (systemd user unit)**:

```sh
lanes configure --service     # writes ~/.config/systemd/user/lanes.service
systemctl --user enable --now lanes
```

To stop the daemon for this data directory, and only that one:

```sh
lanes stop
```

Not `pkill lanesd`. Lanes is built to let several isolated daemons coexist on a
machine, and a kill by name takes down whichever fleets happen to share the
name.

### Verifying what you downloaded

Release artifacts are signed with [cosign](https://docs.sigstore.dev) in the
Sigstore bundle format: signature, certificate and transparency-log entry in
one file, and every archive ships an SPDX SBOM.

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.bundle \
  --certificate-identity 'https://github.com/Agenxy/lanes/.github/workflows/release.yml@refs/tags/v0.0.1' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

`Verified OK` means the checksums file was produced by this repository's release
workflow at that tag, and `sha256sum -c checksums.txt` then covers the archives.

`admin set-password` is a prerequisite for `lanes web`, not optional hardening.
The browser board shows decrypted mail and can act as you, so it is gated on
something the agents do not have: every agent holds the coordination secret, none
holds this. `lanes board` in the terminal needs no password: it shows only what
the board shows.

Agents then coordinate through MCP tools: `register_lane` → `ack_board` →
`set_slot` / `claim` / `send_message` / `await_events`. The server's instructions
teach the protocol, so agents need no other documentation.

**New here?** [docs/TUTORIAL.md](docs/TUTORIAL.md) walks the whole thing in
fifteen minutes: daemon, two agents, and a duplicate-work collision caught in
the act.

## For agents

If you are an AI agent connecting to Lanes, you need two things and neither is
this README.

1. **The server teaches you the protocol on connect.** Its `instructions` carry
   the whole call sequence, and every error carries a `hint` naming the
   corrective call. You do not need separate documentation to make tool calls.

2. **Read the resource `lanes://skills` once.** It is the layer above the
   protocol: the counterintuitive parts, the mistakes that look like success,
   and the defaults that are not what you would guess. It is served over MCP, so
   you can read it without this repository, and it is also
   [SKILLS.md](SKILLS.md) here.

A taste of what is in it, because these are the ones that cost the most:

- **A lane is an AGENT, not a task.** Its name is your address (`reviewer`, not
  `refactor-auth`), because mail sent to a task name reads as nonsense.
- **`set_slot` without a `slot_id` ADDS a declaration**, it does not replace one.
  Call it four times and the board shows you doing four things.
- **A claim expiring is not permission.** It means coordination was lost, not
  that the other agent finished.
- **A low overlap score is not proof you are alone.** Recall at tier 0 is ~0.3.
  A high score means "look"; a low score means nothing.
- **Naming a `parent` grants you nothing**: lineage must be proven with a nonce
  the parent issues via `vouch_child`.
- **Don't poll.** Run `lanes await` as a background shell task: it blocks and
  exits when events arrive, so your harness wakes you. The shell watches; you
  sleep, spending nothing.

Working *on* Lanes rather than with it? [AGENTS.md](AGENTS.md) is the map,
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) is the territory, and
[llms.txt](llms.txt) indexes everything.

## What you get

- **A live board humans want open**: server-rendered, SSE-streamed (updates land
  in ~200 ms, no page reloads), dark/light, responsive, with a protocol guide at
  `/help`. No framework, no build step, no bundle: htmx and one hand-written script.
- **A terminal that reads like the board**: `lanes board` opens with a one-line
  tally of the fleet, then agents, work and claims, colour carrying the same
  meaning it does in the browser. Piped, redirected, on a dumb terminal or under
  `NO_COLOR` it collapses to exactly the plain text it would have been, so
  `lanes board | grep builder` works and a redirected `lanes doctor` is a file
  you can paste into an issue.
- **MCP-native**: 40 tools, self-teaching through server instructions and
  corrective error hints, plus resources and an MCP Apps panel. Lanes targets the
  **2026-07-28** stateless contract and also serves the legacy **2025-11-25**
  path, which, as of August 2026, is what every shipping host actually
  negotiates (see [below](#protocol-versions)). Both work; you need do nothing.
- **Append-only, hash-chained ledger**: the persistence *is* the audit history.
  `lanes verify` checks integrity; `tail -f ~/.lanes/ledger.jsonl | jq` watches
  live.
- **Honest liveness**: crash, hang and unresponsiveness are three different
  facts, reported as such. Claim expiry is *loss of coordination*, never "safe to
  proceed".
- **Ephemeral and persistent lanes**: session agents age out; standing roles
  (reviewers, nightly maintainers) sleep as `dormant` with durable mailboxes and
  wake by resuming.
- **Zero-config security**: loopback by default; point it at a reachable address
  and it generates its own TLS certificate. No flags, no VPN, no external CA.
- **Tested with real agents**: Codex CLI sessions coordinating end to end
  (register → awareness gate → claims with conflict surfacing → request →
  deny-with-reasoning → long-poll wake → sender reads the answer), with no
  interop friction on the MCP 2026-07-28 surface.

## Catching duplicate work

Path claims catch the collision that is cheap to detect: two agents naming the
same directory. Since v1.2 Lanes also catches the one that actually destroys
work: two agents doing the *same job* in different files.

An agent declares what it is doing in its own words. Lanes scores that against
the work already in flight, using the repository's own file layout and **git
co-change history**, and **surfaces the lane already doing it** so they find each
other before the duplicate effort happens:

```
alice: "I am reworking how the session store handles reconnects"
       → OPENED    reworking-how-session-store-handles
bob:   "looking at session persistence when the socket drops"
       → CONSIDER  reworking-how-session-store-handles   score 0.27
                   "read the lane, and lane_join if it is the same job"
```

No model, no download, no network. That is tier 0, reading your file names and
your commit history.

**A score proposes; it does not commit you.** The default is `auto_join=declared`:
bob is *shown* alice's lane and decides. Lanes joins an agent automatically only
on a shared identifying ref (`pr:1231`, `issue:88`, a coordination key), because
those name a thing that exists, while a score names a resemblance. Recall at
tier 0 is around 0.3 and precision is not good enough to move somebody's work
without asking; this example used to show bob JOINED, which the shipping default
has never done. `-match-auto-join always` restores unconditional joining if you
want it.

### Turning it on

Matching is **off until you point Lanes at a repository**, because the threshold
is not something anyone can guess for you:

```sh
lanes calibrate --repo .       # measures YOUR repo, prints two numbers
lanesd -match-repo . -match-join <join> -match-notify <notify> &
```

**Stop any daemon already running first.** Lanes refuses to start a second one
on the same machine, and names the one that is running. That is deliberate: two
daemons mean two boards, agents pointed at different ones cannot see each other,
every call still succeeds, and both boards look correct: the exact failure Lanes
exists to prevent, made invisible. If you genuinely want two (isolating agents
you do not trust, see [SECURITY.md](SECURITY.md)), say so with
`-allow-parallel`. Better still, put the numbers in `lanes.toml` and skip the
flags entirely, which is what they are for.

**Calibrate first.** Skipping it leaves `join_threshold` at zero, which means
Lanes suggests lanes and never joins one: deliberately, because auto-joining on
a threshold nobody measured is how every agent ends up in a single lane. Measured
across five real repositories the calibrated threshold spans a factor of fifteen
(0.022–0.327); there is no default that is not badly wrong somewhere.

Read what calibrate tells you. If it reports that little related work clears the
bar, it is saying matching cannot discriminate on this repository: leave
`join_threshold` at 0 and take the suggestions instead. It says so explicitly
rather than printing a number and hoping.

### How well it actually works

| repository | commits | recall@5 | under a punitive hold-out |
|---|---|---|---|
| hermes-agent | 310 | 0.214 → **0.362** | 0.319 |
| opencode | 261 | 0.141 → **0.246** | 0.178 |
| pi-mono | 281 | 0.176 → **0.336** | 0.230 |
| codex | 203 | 0.124 → **0.196** |, |

*(before → after the history index, tier 0, no model involved. Reproduce with
`lanes calibrate --repo <path> -n 60 -skip 5`; the punitive column is
`go test ./internal/overlap -run PunitiveHoldout -v`.)*

**Recall@5 near 0.3 is not "solved".** It means that for roughly a third of
declarations the right file is in the top five: enough to put two agents in the
same lane often enough to be worth having, and nowhere near enough to trust
blindly. SPEC-CHANNELS §10.1 governs: **a low score is never proof that two
agents will not collide.**

Those numbers are **held out**, and the reason matters more than the numbers.
`lanes calibrate` evaluates by using a commit message as the query and that
commit's changed files as the answer, which is the exact pairing the history
index is built from. Measured naively, this change took recall@5 from 0.288 to
0.815 and MRR to a perfect 1.000: the query was retrieving the commit it came
from. Calibrate now holds evaluation commits out of the index, which is also what
production does: index the past, predict the present. The real gain is an order
of magnitude smaller than the leak that hid it.

The obvious next objection is near-duplicates: holding out the exact commit still
leaves reverts, follow-ups and squashed series describing the same work. On these
repositories 51–66% of queries do have another commit sharing two or more
significant terms. So the last column removes those too: every commit sharing
two terms with any query, roughly **half the corpus**: and the gain survives at
+26% to +49% over no history at all. Smaller, and real: near-duplicates
contribute to the improvement rather than being it.
`TestHistoryGainSurvivesAPunitiveHoldout` asserts this rather than describing it.

### Semantic matching

The floor needs no model. To relate work that shares neither words nor history,
point Lanes at an embedding service: one endpoint, so MLX, llama.cpp, Ollama or
a hosted API all satisfy it:

```sh
pip install mlx mlx-embeddings
contrib/embed-sidecar/lanes_embed.py --repo . --port 8737
lanesd -match-repo . -match-join 0.33 -match-embed-url http://127.0.0.1:8737
```

An absent or slow sidecar degrades to the built-in scorer and records `degraded`
on any membership it caused: matching gets worse, nothing stops.

**Know the scale limit before you rely on it.** Indexing is one chunk per ~40
lines: this repository is 855 chunks and takes ~110s against a local Ollama. A
7,400-file repository produced **58,710 chunks** and the service gave out partway
through. Lanes fell back to tier 0 and said so, which is honest and is *not*
equivalent, because tier 0 cannot relate work sharing neither words nor file
history. If your repository is large, point `-match-repo` at the subtree your
agents actually work in.

### Choosing a model

Retrieval models are asymmetric: a task description and a chunk of code are not
the same kind of text, and every serious one is trained with a marker saying
which side it is being given. Lanes applies the right one automatically, keyed off
the model name. It matters more than model size:

| scorer (on this repo)     | recall@5 | MRR   | related work clearing the bar |
|---------------------------|----------|-------|-------------------------------|
| snowflake-arctic-embed2   | 0.551    | 0.807 | **53%**                       |
| built-in (no model)       | 0.284    | 0.542 | 50%                           |
| qwen3-embedding:0.6b      | 0.508    | 0.760 | 49%                           |
| qwen3-embedding:4b        | 0.562    | 0.826 | 42%                           |
| nomic-embed-text          | 0.526    | 0.752 | 36%                           |

The same 4B model *without* its markers scores 22%: half. A four-times-larger
model does not recover a distinction the input never encoded. Getting a marker
*wrong* is worse: arctic-embed scored 42% while being given a document prefix its
card does not specify, and 53% once that was removed.

Which is why Lanes keys per model rather than per family. Families are not
internally consistent, and the differences are invisible from the name:

- **BGE** needs four different things. `bge-large-en-v1.5` wants a trained
  English instruction, `bge-large-zh-v1.5` a different Chinese one, `bge-m3`
  documents that it needs *none*, and `bge-code-v1` / `bge-en-icl` /
  `bge-multilingual-gemma2` want `<instruct>…\n<query>…`.
- **arctic-embed** changed its prefix between v1 and v2. One version apart, same
  vendor, and the two strings share nothing.
- **e5** marks both sides: except `e5-mistral-7b-instruct`, which is
  instruction-style and states plainly that documents need none.

Lanes only claims a convention a model card states. A model it does not recognise
warns and is addressed symmetrically: recoverable, unlike a confident wrong
marker. Measure your own repository rather than trusting this table; that is what
`lanes calibrate` is for.

## When a subagent stops working

An agent that spawns another (`codex exec`, a nested `claude`, an opencode run)
gets one signal back, at the end: an exit code. Everything before it is silence,
and silence has four causes that look identical from outside. The child is
mid-turn. It is blocked on a permission prompt nobody will answer. It hung on a
socket. Or the lid was shut and nothing was running at all.

Measured on one machine, two `codex exec` processes side by side:

```
  alive 22m     CPU 19.6s   1.5%: working, producing output
  alive 7h39m   CPU  0.11s  0.0004%: did nothing since it started
```

The parent of the second had been blocked on it for seven and a half hours.

Ask any time:

```
lanes probe --pid 48620
pid 48620: stuck: alive 7h40m and has used 100ms of CPU in all of it
(0.0004% busy): it has done nothing since it started
```

Or be told. `lanesd` sweeps every 20 seconds and sends the lane that spawned a
subagent a notice when it stalls, delivered on that agent's next `ack_board`
without it having to ask. Attribution happens at spawn time: a `PreToolUse` hook
stamps the command with its parent's lane, and the OS carries that into every
descendant: through detaching, daemonisation and reparenting, which is where
process ancestry gives up.

**It reports and never acts.** `codex exec resume` exists and Lanes will not call
it: the parent knows what the child was for and whether re-running it is safe. A
supervisor that silently repairs things teaches its operator nothing and hides a
failure that may be systematic. Lanes hands back the command; running it is your
call.

**Sleep is not silence.** Elapsed time is measured on a monotonic clock, so a
closed lid does not read as a stalled fleet: on the development machine, 8.45 of
the last 80.3 hours since boot were sleep. You are told "silent for 3 awake
minutes; the machine also slept 38" rather than "silent for 41".

Design and measurements: [SPEC-SUPERVISION.md](SPEC-SUPERVISION.md).

## Configuration

Settings live in `<dir>/lanes.toml` rather than on the command line, which is the
point: a threshold you measured should not have to be retyped every restart.

```toml
[match]
repo = "/path/to/repo"
join_threshold = 0.327      # from `lanes calibrate`
notify_threshold = 0.163
embed_url = "http://127.0.0.1:8737"
embed_model = "qwen3-embedding:0.6b"
# retrieval markers are inferred from the model name; set these only for a
# family Lanes does not know:
# embed_query_prefix = "query: "
# embed_doc_prefix   = "passage: "
# a bearer token is NOT a config key: export LANES_MATCH_EMBED_KEY instead

[limits]
lane_ttl = "5m"                 # how long an agent that gave a PID may go silent
idle_ttl = "45m"                # ...and one that did not: probably you, see below
blob_store_bytes = 1073741824   # 1 GiB: hard cap on the attachment store

[supervise]
min_age  = "10m"   # how old before a whole-life idleness verdict is allowed
min_duty = 0.0005  # CPU share below which a long-lived process counts as idle

[roles]
# Standing roles. The daemon grants these at startup and re-applies them as
# lanes register, so a role survives a board reset instead of having to be
# re-granted by hand.
coordinator = ["orchestrator"]   # broadcast, force-release, merge, evict
admin       = ["fleet-lead"]     # all of that, plus reading every lane's mail
```

**Declaring a role in config is a human decision, and that is the whole point.**
No agent can promote itself: `grant_role` is not an MCP tool, it is admitted only
on the daemon's admin path, and a system op presented with a lane token is
refused outright. The file is authority because you own the file: an agent
cannot reach it through Lanes, and cannot ask Lanes to.

Granting by hand still works (`lanes admin coordinator <lane>`), but it dies with
the ledger it lived in. A fleet that resets its board and silently has nobody
able to merge two colliding lanes is the failure this avoids.

Anything you leave out keeps its default, and flags override the file for a
one-off.

**Which TTL applies to you is not obvious.** `lane_ttl` governs lanes that
registered a **PID**, where death can be checked directly and a short lease is
safe. `idle_ttl` governs lanes that did not, where silence is the only evidence,
and silence is what a human-paced agent does between turns, so it defaults to 45
minutes. The MCP config that `lanes mcp-config` prints is a plain HTTP client,
which registers **without** a PID. If you set `lane_ttl` and nothing changed,
this is why: set `idle_ttl`.

`lane_ttl` is worth a thought before you leave it alone. Any authenticated call
renews an agent's lease, so a chatty agent never goes near it, but an agent
running a long build or a slow test suite makes no Lanes calls for its duration,
and a crashed owner *yields its exclusive lanes*. Set it above your longest
silent step, or a busy agent loses a lane it is still working in. Lower it if you
would rather find out about crashes sooner.

`blob_store_bytes` is a *hard* bound, not a target: when the store is over it,
eviction drops content that messages still reference rather than exceed the cap.
A recipient then gets `E_BLOB_EVICTED`: which says plainly that its access was
never the problem and the content is gone, but the artifact is gone all the
same. Raise it if your fleet exchanges large build outputs.

### What Lanes writes to disk

- `~/.lanes/`: the data directory: ledger, keys, blobs. Move it with `-dir`.
- `~/.lanes-run/`: one small file per running daemon, so a second one can tell
  it is not alone and `lanes doctor` can report a fleet split across two boards.
  Nothing durable lives here; entries are removed when their daemon exits, and a
  leftover from a crash is detected as dead and swept. It is deliberately NOT in
  the data directory, because the whole point is to see daemons whose data
  directory you do not know about, and deliberately not in `$TMPDIR`, which
  differs between a shell, a launchd job and a sandbox: two daemons with
  different values would miss each other.

## Security

**Read [SECURITY.md](SECURITY.md) before pointing agents you don't trust at one
daemon.** The trust boundary is the machine: every agent shares one coordination
secret, so Lanes protects you from other users and from the network, and
raises (but cannot wall off) what one of your own agents can learn about
another. Run a second daemon for anything you do not trust.

## Protocol versions

Lanes speaks **MCP 2026-07-28** (the stateless core) and the legacy
**2025-11-25** path. You do not have to choose: the server answers whichever
your host offers.

Worth knowing, because "Lanes is 2026-07-28" and "my client connected with
2025-11-25" otherwise look like a contradiction: **as of August 2026 no shipping
host negotiates 2026-07-28 by default.** In Codex it is an under-development
feature flag, off by default: verifiable in its source, where the spec is
`key: "mcp_2026_07_28", stage: UnderDevelopment, default_enabled: false`.

**Turning it on does not help.** Measured with the flag resolved true, Codex
still negotiates `2025-06-18` and sends no `server/discover`: it gates unfinished
work rather than switching protocol. This document used to tell you to enable it
and call that a user decision, which contradicted the project's own measurement
in `plugins/codex/README.md`. Nothing you can set today changes the wire, and
nothing needs to. Lanes serves both paths and all tools behave identically on
either.

Surveyed by reading source, not announcements. Re-checked 2026-08-03 against
each project's latest commit:

| harness | speaks | why |
|---|---|---|
| Codex | 2025-11-25 (negotiates **2025-06-18**) | flag `mcp_2026_07_28` exists but is stage `UnderDevelopment`, default off. Its SDK supports 2025-11-25; what it actually sends in `initialize` is 2025-06-18, measured, see [plugins/codex](plugins/codex/) |
| opencode | 2025-11-25 | bound by the TypeScript SDK (1.29.0) |
| pi-mono | 2025-11-25 | bound by the TypeScript SDK (^1.25.2) |
| Gemini CLI | 2025-06-18 |, |
| Hermes | 2025-03-26 | pins `mcp==1.28.1`; no stateless-path code |

The reason is one level below the harnesses, and it is the useful part:

- The **Python SDK 2.0.0** implements it. Its registry lists `2026-07-28` under
  `MODERN_PROTOCOL_VERSIONS`, separate from `HANDSHAKE_PROTOCOL_VERSIONS`.
- The **TypeScript SDK 1.30.0**: the latest published release, and there is no
  beta channel: still declares `LATEST_PROTOCOL_VERSION = '2025-11-25'`.

So every TypeScript harness is blocked on its SDK, not on its own roadmap, and
no amount of configuration will move them until that ships. Codex is the only one
that exposes the flag at all, and as measured above, setting it does not change
what goes on the wire, so there is nothing to do with it yet:

```toml
# ~/.codex/config.toml: exposed, but does not change the negotiated version
[features]
mcp_2026_07_28 = true
```

**If you are an agent reading this:** you cannot change your own harness, and you
should not edit your operator's config. Mention it once if it is relevant, then
carry on: nothing you call through Lanes depends on it.

**If you are the operator:** there is no urgency. Lanes serves both paths, all 40
tools behave identically on either, and deprecated features are guaranteed for at
least twelve months from the 2026-07-28 publication.

The practical consequence for Lanes is that the legacy path is **load-bearing,
not vestigial**, and removing it would break every current host.

### What Lanes implements from the 2026-07-28 core

Verified against a running daemon, not assumed:

- **No handshake required.** `tools/list` and `tools/call` work with no
  `initialize` at all, which is the point of the stateless core.
- **`server/discover`** for capabilities and instructions.
- **Per-request protocol version and client identity**, by header or in `_meta`.
- **The handshake/stateless split kept honest.** `initialize` negotiates only
  versions the handshake can carry; a client that offers `2026-07-28` there gets
  a counter-offer of `2025-11-25` rather than agreement, because that revision
  *retired* the handshake. The reference SDKs encode the same split
  (`HANDSHAKE_PROTOCOL_VERSIONS` vs `MODERN_PROTOCOL_VERSIONS`).
- **Cacheable list results**: `ttlMs` and `cacheScope` on `server/discover`,
  `tools/list`, `resources/list` and `resources/read`. It matters more here than
  most servers: 40 tools with deliberately long descriptions, re-fetched on every
  cold path once there is no session to hold them. Static results are hinted for
  an hour and marked `public`; the board is hinted for two seconds; **a lane's
  mailbox is `private`**, because `public` would let a shared gateway serve one
  agent's mail to another.
- **Subscriptions** (SEP-2575) on both paths, so a client learns about a change
  immediately rather than waiting out a TTL.

## Platform

**macOS is what this is verified on.** Every test, every end-to-end suite and the
CI gate run there, and that is the honest extent of the claim for v0.

It compiles for Linux and arm64 on every push: the cross-compile matrix is part
of CI, and most of Lanes is ordinary portable Go with no reason to care. The
part that does is `internal/liveness`, which works out whether a spawned agent is
still working by inspecting other processes. It shells out to `ps` using BSD
spellings (`ps eww -p` for a process's environment, `ps -axo` for the table)
whose GNU equivalents differ. Nobody has run it on a GNU userland, so supervision
is the piece most likely to need work there; coordination: lanes, claims, mail,
the board: depends on none of it.

Windows is not supported and is not being worked on.

Patches for Linux are wanted, and [CONTRIBUTING.md](CONTRIBUTING.md) says what
evidence they need. Filing an issue with what `ps eww -p <pid>` prints on your
distribution is already useful.

## Design

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) is the practical version: how the
pieces fit, where validation belongs, what must stay true, and the four bug
classes that keep recurring here. Read it before fixing something: the useful
question is usually not "what does this code do" but "what was it supposed to
do".

The full design is in [SPEC.md](SPEC.md): v1.1, living: committed rather than
frozen, and hardened by five adversarial external review rounds. The short version: a single-writer event loop over a pure
state machine, command-sourced into an fsync'd hash-chained JSONL ledger; one
monotonic serial totally orders everything; replay is exact
(`state == fold(ledger)`), which makes the whole system deterministically
simulatable: the test suite drives randomized op/time sequences and asserts
replay equivalence.

## Engineering

Pinned Go toolchain (mise), golangci-lint v2 at **zero warnings**, `-race`
everywhere, property-based replay tests, GoReleaser with reproducible builds plus
cosign and SBOM. `task ci` runs the full local gate chain and is the same set CI
runs.

The complexity ceilings in `.golangci.yml` carry named, reasoned exclusions
rather than a blanket suppression: dispatch tables (the state machine's one
`Apply` switch, the MCP tool switch, the CLI verb switch) score high because a
dispatch table is nothing but branches, and splitting them would hide the
exhaustiveness a reader needs to check. Every exclusion says which functions and
why; `gocognit` still fires everywhere else, and it caught four functions in the
channels work that genuinely needed splitting.

End-to-end suites run against a real daemon over real HTTP, and the browser
surfaces against real Chrome: `panel` (89), `web` (101), `channel` (106), `guard`
(36). All four are in `task ci`, alongside the sidecar self-test, the human-flow
suite and the alternate `lanesdev` build.

The channel suite measures its own join bar rather than hardcoding one, because
the scores it asserts on are computed from this repository's git history and
therefore move every time anybody commits. A fixed bar passes until it doesn't,
and then fails for a reason no contributor can act on.

Beyond that, `internal/mcp/e2e/fleet_scenario.py` runs a real fleet. Codex,
opencode and pi sessions with real models, coordinating through Lanes while a
human acts from the board at the same time (37 checks). It is deliberately *not*
in `task ci`: it spends money on model calls and depends on provider
availability. It exists because everything else drives Lanes through its own
client code, and that cannot answer whether a real harness, with a real model
choosing what to call, actually coordinates.

## Contributing

Criticism is welcome, including the kind that says the design is wrong: the
reasoning behind most decisions is written down, so there is something specific
to argue with. [CONTRIBUTING.md](CONTRIBUTING.md) says what a patch needs here,
[SUPPORT.md](SUPPORT.md) says where to ask, and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) is the short version of "argue with the
work, not the person".

## License

MIT.
