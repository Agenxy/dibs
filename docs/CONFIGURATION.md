# Configuring Dibs: `dibs.toml`

Every setting the daemon reads, in one place, with what happens if you leave it
alone. It lives at `<data dir>/dibs.toml`: `~/.dibs/dibs.toml` unless you moved
it, and `dibs doctor` prints the directory it actually opened.

**Dibs runs correctly with no configuration file at all.** Nothing here is
required, and most fleets never write one. The defaults are chosen for a single
machine with a person at it; the settings exist for the cases a default cannot
know about, and each entry below says which case that is.

**An unknown key stops the daemon**, deliberately, rather than being ignored. A
setting that was never going to take effect must not look applied: a misspelt
`agent_ttl` that silently did nothing would be indistinguishable from one that
did, and you would tune it for an afternoon.

```
unknown setting(s) in dibs.toml: match.subagent_inherit: check the spelling
and the table they are under ([match], [limits]); nothing here took effect
```

Precedence, highest first: **command-line flag → environment variable → this
file → the default.**

---

## Top level

| Key | Default | What it decides |
|---|---|---|
| `addr` | `127.0.0.1:4777` | What the daemon listens on. Set a LAN or tailnet address to serve agents on other machines; TLS is then arranged automatically and each machine must `dibs trust` the certificate once. Also `-addr`, `DIBS_ADDR`. |
| `tls_cert` | *(auto)* | An explicit certificate, when you would rather supply one than have Dibs manage its own. |
| `tls_key` | *(auto)* | Its key. Both or neither. |
| `insecure_plaintext` | `false` | Serve a non-loopback address without TLS. Only for a network you already trust end to end; the name is the warning. |

```toml
addr = "100.72.14.3:4777"    # a tailnet address: agents on four machines, one board
```

---

## `[wake]`: when an agent hears about mail

| Key | Default | What it decides |
|---|---|---|
| `extend_turn_for` | `all` | Which news may extend an agent's turn: `all`, `urgent`, `none`. |
| `notices_wake` | `true` | Whether situational awareness alone may extend a turn. |
| `sockets` | `true` | Whether the session-socket routes run at all: the daemon's peer-socket wake and the bridge's self-wake. |
| `remind_stale_after` | `1h` | How long a live session may go without coordinating before its digest says so. `off` disables. |
| `exec.<harness>.argv` | *(none)* | The command that reaches that harness when an agent is **not running**. |
| `exec.<harness>.cooldown` | `90s` | The shortest gap between two wakes of the same agent. |

### `[wake.exec]`: reaching an agent that is not running

Every other delivery path waits for the agent to come to Dibs. A hook fires on
the agent's own turn boundary, a call returns what is waiting, a long poll
parks until something arrives, and all three need the agent to be executing
already. An idle session has no boundary coming and makes no calls, so its mail
waits until somebody tells it out loud.

Give a harness a command and the board will run it when work somebody is
blocked on arrives for one of its agents that has stopped:

**The command runs on the machine the DAEMON is on, in the agent's own working
directory.** That is not a detail, it is the constraint the whole thing is
shaped by: the daemon starts a process, so the process starts here. Both
documented commands care where they start (`codex exec resume` refuses outside
a trusted directory), so when the recorded directory is missing the wake runs
where the daemon does and usually fails, with a warning saying so.

Two ways for it to be missing. A removed worktree is the local one. An agent on
ANOTHER COMPUTER is the other, and there nothing you write in this file can
help: the command would have to run over there. Run a bridge on that machine and
give it its own `[wake.exec]`. `dibs doctor` counts an agent whose directory is
not on this machine as having no route, rather than as covered. See
[NETWORK.md](NETWORK.md) for why the hub decides THAT an agent is woken and only
its own machine can decide how.

```toml
[wake.exec.codex]
argv     = ["/Applications/ChatGPT.app/Contents/Resources/codex",
            "exec", "resume", "{thread}", "{message}"]
fallback = ["/Applications/ChatGPT.app/Contents/Resources/codex",
            "queue", "--thread", "{thread}", "--message", "{message}"]
cooldown = "90s"
```

**Which Codex command, and why this one.** Both were measured on 2026-08-22,
and the `fallback` on 2026-09-05.

`codex exec resume <uuid> "<text>"` continues that thread's history in a new
headless process, which registers, reads its mail and acts. It works for a
thread nothing has open; a thread the desktop app holds open refuses it, which
is what the `fallback` line is for (see below), so an entry without one leaves
open desktop threads unreachable. On builds from
2026-08-18 it takes a per-thread writer lock, so it refuses rather than colliding
with a session that is already running; on older builds two of them interleave
into one transcript, which is a good reason to keep the cooldown.

`codex queue --thread <uuid> --message "<text>"` is the other candidate and is
**not** sufficient on its own. It enqueues durably and wakes the thread only if
it is already **loaded** in a running app server. Pointed at a thread whose app
was not running it returned `Queued message …` and nothing woke: the message
waits for somebody to open that conversation. Useful when you know the app is
up and you want the existing window to act; not a wake on its own.

**Claude Code.** Measured on 2026-08-26, the same way.

```toml
[wake.exec."claude code"]
argv = ["claude", "--resume", "{thread}", "-p", "{message}"]
cooldown = "90s"
```

`claude --resume <session-id> -p "<text>"` continues that session in a new
headless process. Watched end to end: a seeded session's transcript went from 15
lines to 56, the notice arrived as a turn, and the agent's first action was to
call `register` to go and look at the board, which is exactly what a wake is for
and the whole of what it should cause.

One caveat worth knowing before you rely on it. A headless `-p` process does not
have the permissions an interactive session does, so an agent woken this way can
find its own Dibs calls refused, which is what happened on the measured run
after it decided to look. It still beats not being told: the session is running
and its own hooks fire from there. If your agents need tool access on a wake,
give that process the permissions it needs rather than assuming it inherits
them.

**A harness may refuse to resume a thread it already has open, so give it a
second command.** `codex exec resume` starts a CLOSED thread and fails on one
that is open in the Codex desktop app: `thread-store conflict: thread <id>
already has an active writer`, exit 1, and no configuration changes that. The
app holds the writer for as long as the thread is open. `codex queue` is the
other half: it delivers straight into an OPEN thread, where the app's own
app-server drains it and injects it as a user message, and to a closed thread
it exits 0 and parks the message where nothing reads it until somebody opens
that thread by hand. The two are exact inverses.

So a codex entry names both, and the daemon tries them in order. The primary
is the one that can be confirmed for a closed thread; the fallback runs only
when the primary exits non-zero AND its output says the thread is open
(`active writer`, `thread-store conflict`), and is the one that can be
confirmed for an open one. A primary that fails for any other reason is a
failed wake and gets the retry every failure gets: `codex queue` exits 0 for a
closed thread too, parking the message, and that must not count as a wake.

```toml
[wake.exec.codex]
argv     = ["codex", "exec", "resume", "{thread}", "{message}"]
fallback = ["codex", "queue", "--thread", "{thread}", "--message", "{message}"]
```

Measured on this machine, both directions. A thread open in the desktop app
refused `exec resume`, took `queue`, and its own transcript then carried
"Dibs: check the board." followed by the agent checking in and answering the
two questions it had been sent. That case had been reported as unreachable for
weeks, because only the first command was ever configured. The reverse, a
closed CLI thread, resumed on the primary and parked on the fallback, which is
why the order is not arbitrary.

`fallback` obeys every rule `argv` does: whole-element substitution, no shell,
argv[0] named in this file and never a placeholder.

**A wake runs in the agent's own directory.** It has to, and for a long time it
did not. `codex exec resume` refuses to start outside a trusted directory, and a
daemon started by launchd has `/` as its working directory, so every wake it
attempted exited 1 without reaching anyone.

This was diagnosed twice as launchd putting the daemon in a different security
session with no login keychain. **That was wrong**, and the wrong explanation is
recorded here because it cost two investigations: a probe LaunchAgent in the
identical domain and `ProcessType` as `dibd` read the login keychain without
trouble and ran a complete `claude --resume` turn, exit 0. The security session
was never involved. The working directory was the whole difference.

The daemon now runs each wake in the directory the agent registered from, and
names that directory when a command fails. It still will not print the command's
output, because a wake command runs a whole agent turn and that output is
somebody's decrypted mail; it prints the argv instead, so you can run it
yourself and see what is being withheld.

**Why you want one of these even though the socket route needs no setup.** The
socket is best effort: the receiving session decides whether to accept a peer
message and sends no receipt, and a Claude Code session running in
`bypassPermissions` mode HOLDS peer messages for its human. That is the mode an
unattended fleet runs in, so for those agents the socket route delivers nothing
and nothing reports it. A command is the route the daemon can confirm, because
it sees the exit status. `dibs doctor` tells you which of the two a board has.

The key under `exec` is the harness as agents report it, lowercased: `codex`,
`claude code`. Each takes `argv` and an optional `cooldown`. Check what your
agents actually report before trusting a key to match: the board shows values
like `Codex`, `Claude Code` and `codex-quarters`, and only an exact lowercased
match is a match, so a harness that reports a variant gets no wake and nothing
says so.

**`argv`, never a shell string.** There is no shell anywhere in this path.
`{thread}`, `{agent}`, `{from}`, `{type}` and `{message}` each replace one
whole element and are passed to the command as single arguments, so a message
written by a hostile peer is an argument and not a command. Nothing an agent
sends reaches this: the command comes from this file and there is no tool, op
or admin route that can change it. That is deliberate, because a wake command
is arbitrary code running as you.

**`{thread}` is the harness's thread, and it is the one the harness reported
last.** Dibs fills it with the agent's current session (`current_session` on
the board: the id its harness most recently reported, by alias or by a stated
`session_id`) when that has the shape a resume command accepts. A thread
beats the bridge's own `host-<ppid>`: a call that states its thread and
carries the bridge id as an alias is current on the thread, and the bridge id
re-sent on every later call does not displace it. When the
harness's last report is not a thread, no thread is known for the current
activation and the exec route stands down until one is bound: the threads
the agent held before are the activations it left, and resuming one of those
wakes the wrong session. Only a row with no current session recorded falls
back to its newest thread-shaped alias, and failing that to a thread-shaped
`session_id`. A bridge-derived `session_id` such as `host-92368` names the
harness process, dies with it, and is never resumed. A persistent agent that
has reattached
holds several threads, and a return to an earlier one makes it current: the
activation it is in, not the one it bound last. Resuming any other starts a
real session that is not the one holding the mail, and the board logs a
successful wake for an agent that hears nothing. An
agent that has published no such identifier is never woken, because there would
be nothing to hand the command.

`{message}` is a fixed line telling the agent to check in. **The mail itself is
never put on a command line**: the agent reads it over its authenticated
connection with its own token, which is the same reason the bodies are
encrypted at rest.

Only work somebody is blocked on starts a process: a question, a request, a
handoff, or a **verdict**, which is the answer to something this agent asked and
then stopped for. A notice does not justify starting a process on the operator's
machine.

The other half of the test is whether the agent is *reachable already*, and it
is not `active`. `active` means the idle lease has not lapsed, which is
forty-five minutes by default, so an agent whose turn ended seconds ago is still
`active` and is still not running: treating that as "no wake needed" discards
the one attempt the message gets. What is asked instead is whether the agent has
called Dibs within the wake cooldown, which is real evidence of a live process
rather than an unexpired lease.

### `extend_turn_for`: which news may extend a turn already running

Everything above is about a stopped process. This is the separate question of
what an agent that IS running is told at its next turn boundary, and it has no
power to start anything.


`all` means anything unread wakes its recipient, once, when it arrives. A fleet
that waits for somebody to type before its members hear anything is not
independent, and a time-sensitive request sitting unseen because nobody was at
the keyboard is the failure Dibs exists to prevent.

`urgent` narrows it to work somebody is blocked on: questions, requests,
handoffs, unacknowledged announcements, changes to the agent's own standing.
Choose it if you would rather an FYI never cost a turn.

`none` never extends a turn: at a turn boundary, Dibs injects nothing, and the
human's notification and the `waiting` line on every result are what an agent
sees of its mail until it reads it. It governs turn extension only. The wake
routes are separate settings: `[wake.exec]` runs whatever the operator wrote
there, and the harness session socket is tried where one is published and
`sockets` is on, whatever this says. An operator who wants no unsolicited
activations at all sets `sockets = false` and configures no `[wake.exec]`
entries; there is no third route. The daemon reads `sockets` at start and the
bridge at its own start, which includes the in-place upgrade a running bridge
performs when its binary changes: a self-wake carried across that upgrade is
restored only while the switch is on.

**It is a per-machine setting, and on a fleet that spans machines that matters.**
Each side reads it from the data directory of the process reading it: the
daemon from the board's, and a bridge from its own. A bridge that joined a
remote hub has its own directory, holding the secret it was given and nothing
else, so `sockets = false` set on the hub governs the daemon's peer-socket
route and leaves every remote bridge waking its own session as before. Set it
on each machine that runs a bridge. This used to be described as one switch
covering both routes, which is true on one machine and was silently false
across two.

Each message wakes once either way, so an agent that read something and chose
not to act is not asked again. Work somebody is blocked on comes back on the
announcement retry. See [WAKE-MECHANISMS.md](../WAKE-MECHANISMS.md).

`notices_wake` covers the other half: a **notice** is something that happened
TO an agent and that it could not infer, such as being evicted or another agent
joining a space it is working in.

**A verdict is not one of them.** An answer, an approval, a denial or a decline
is the reply to something this agent asked and then stopped for, so it is
BLOCKING: it is counted separately, it reaches `urgent` delivery, and
`notices_wake = false` does not suppress it. This section named an approved
request as an example of what the setting governs, which is the opposite of what
the code does, and an operator turning it off to save tokens would have expected
to stop hearing the one thing they cannot afford to miss.

On by default, because "an agent is told what happened to it" is a guarantee
Dibs already makes, and a guarantee that holds only for operators who found a
config file is not one.

Turn it **off** to buy the tokens back on the situational half. Extending a turn
revives a thread that may be long and whose prompt cache is cold, and on a fleet
of idle sessions that is a real bill to pay for "somebody joined your space".
Nothing is lost when you do: those notices queue, ride along on any wake that
happens for another reason, and arrive in full at the agent's own `check_in`,
which it makes once per activation anyway. What you give up is latency, not
delivery, and verdicts are unaffected either way.

Mail is unaffected either way, because somebody is blocked on an unanswered
question and nobody is blocked on knowing who joined a space.

```toml
[wake]
extend_turn_for = "urgent"   # an FYI should never cost a turn on this machine
notices_wake = false         # ...and do not spend a turn on situational awareness
```

### `remind_stale_after`: a session that stopped coordinating

An agent registers, declares, and then works for hours without calling Dibs
again. Its lease lapses, the board reports it dormant while it is busy, and
peers writing to it are told "recipient is dormant" and conclude the product
does not deliver. On this project's own board a seven-hour autonomous run with
no `check_in` expired a peer's question, and the operator reported Dibs as
broken. It was not; the agent had stopped participating and nothing said so.

So a session that is demonstrably taking turns (its hooks fire) and has not
touched the board for this long is told, with the corrective call: `check_in`,
then `update` or `declare` if the work has moved on. It never extends a turn.
It rides on a digest that is being delivered for another reason, and it
reaches the person on the ambient line at the end of a turn, which extends
nothing. It repeats no more often than the interval itself.

What it does not reach is the case that motivated it: a single seven-hour turn
has no `Stop` and makes no calls, so there is no event for a reminder to ride.
This closes the common case, an agent taking ordinary turns that forgot.

```toml
[wake]
remind_stale_after = "2h"    # or "off"
```

---

## `[limits]`: coordination timings

| Key | Default | What it decides |
|---|---|---|
| `agent_ttl` | `5m` | How long an agent **that registered a PID** may be silent before its lease lapses. Shorter is faster crash detection; longer suits agents that run long silent steps. |
| `idle_ttl` | `45m` | The same for agents with **no PID**, where silence is the only evidence. This governs the config `dibs mcp-config` prints, so an operator who tunes `agent_ttl` and sees nothing change is hitting this one. |
| `max_persistent_agents` | `64` | How many STANDING identities the board may hold. Unset, it follows `max_agents` when that is set lower. |
| `max_agents` | `64` | How many live agents of any kind. |
| `blob_store_bytes` | *(built-in)* | A hard cap on the attachment store. Over it, eviction drops referenced content rather than exceed the bound, so a recipient can hold a message naming a blob that is gone. |

**`max_persistent_agents` is reached by accumulation, not by concurrency.** A
persistent agent holds its slot while dormant, which is the point of one: its
mailbox and memberships survive the harness restarting. So the ceiling fills up
over days rather than at peak: a fleet of standing roles meets it while the
board holds far fewer live agents than `max_agents` allows. The default is the
same as `max_agents`, and when only `max_agents` is set, the persistent
ceiling follows it down, because a default may not make a configuration
invalid.

Before raising it, read the number as a signal. That ceiling is usually reached
because siblings accumulated: an agent that could not prove it was itself and
registered again, leaving its predecessor holding a mailbox nobody reads.
`adopt_agent` reclaims those, and on a stdio bridge they should stop appearing
at all, because the bridge now keeps each agent's nonce. Raise it when the fleet
genuinely runs that many standing roles.

Raising it above `max_agents` is refused rather than accepted: the lower ceiling
is the one that binds, so the setting would read as applied and do nothing.

```toml
[limits]
agent_ttl = "15m"             # these agents run long builds without saying anything
max_persistent_agents = 48    # this fleet really does run that many standing roles
```

---

## `[match]`: work-overlap detection

Matching is **off until you point it at a repository**, because an index built
from the wrong tree is worse than no index. Nothing here is inferred.

| Key | Default | What it decides |
|---|---|---|
| `repo` | *(empty: off)* | The checkout to index. Setting it is what turns matching on. |
| `join_threshold` | `0` (suggest only) | Score at or above which an agent is joined to a space automatically. |
| `notify_threshold` | *(calibrated)* | Score at or above which an agent is merely told. |
| `history` | *(built-in)* | How many commits the co-change mining reads. |
| `deadline` | `1.5s` | Bound on the scorer. Declaring work never blocks on it. |
| `embed_url` | *(empty)* | A tier-2/3 embedding service; see `contrib/embed-sidecar`. |
| `embed_model` | *(service default)* | Which model to ask it for. |
| `embed_query_prefix` | *(model default)* | Prefix the model wants on queries. |
| `embed_doc_prefix` | *(model default)* | Prefix it wants on documents. |
| `auto_join` | `declared` | `declared` joins only on a shared identifying ref; `always` joins on score alone; `never` only ever suggests. |
| `director_required` | `false` | Every join must be approved by the coordinator. Serialises the fleet behind one approver; off for that reason. |

**The daemon does not need to read your checkouts.** Matching is built from
two bounded things, the tracked file list and recent commit subjects with the
files each touched, and the daemon mines them itself when it can. When it
cannot (on macOS a daemon started by launchd is not granted `~/Desktop`,
`~/Documents` or `~/Downloads`, and `/usr/bin/git` blocks there on a prompt no
background process can show), the agent's own stdio bridge, which runs inside
the checkout with the access the harness already has, ships them instead:
`dibs doctor` names the tree and the agent. An agent may supply the index only
for the tree it is registered in, the daemon keeps its own reading of any tree
it can read, and nothing shipped is file contents. No grant is needed, and
Full Disk Access is the wrong answer to a coordination daemon.

**There is no safe default threshold.** Scores are unitless and relative to the
scorer *and* the repository together: measured across five real repositories the
calibrated value spanned a factor of fifteen. Run `dibs calibrate`, which scores
against that repository's own git history and proposes values, then write them
down.

```toml
[match]
repo = "/Users/you/work/api"
notify_threshold = 0.171      # from `dibs calibrate`, not from a guess
auto_join = "declared"
```

---

## `[supervise]`: spawned-subagent liveness

Whether a subagent somebody spawned is still working, thinking, or stuck. It
reports; it never acts.

| Key | Default | What it decides |
|---|---|---|
| `every` | *(built-in)* | How often the scan runs. |
| `quiet` | *(built-in)* | Silence after which a subagent is called stalled. |
| `frozen` | *(built-in)* | Silence after which it is called frozen. |
| `min_age` | *(built-in)* | Ignore anything younger than this; a process that just started is not stuck. |
| `min_duty` | *(built-in)* | CPU duty below which "running" is not evidence of working. |
| `off` | `false` | Turn supervision off entirely. |

```toml
[supervise]
off = true    # this machine's agents are supervised by something else
```

---

## `[roles]`: standing coordinator and admin

| Key | Default | What it decides |
|---|---|---|
| `coordinator` | *(none)* | Agent names granted the coordinator role at every start. |
| `admin` | *(none)* | Agent names granted admin. **Admin can read every agent's mailbox.** |
| `identity` | *(none)* | Table of name → that agent's 64-hex **fingerprint**. **Required for the first grant.** |

Applied at **every** start, and only for a short window after it. A role granted
by hand disappears with the ledger it lived in; a role declared here comes back
with the daemon, which is what somebody writing it down expects.

**A name authenticates nobody, so you have to say who you mean.** Under
`[roles.identity]`, give each declared name that agent's **fingerprint**: a
64-character hex string derived from its nonce. The daemon records it in
`<data-dir>/roles.pinned`, and from then on the role follows that identity: the
same name later, under a different agent, is refused.

**Not the nonce itself.** A nonce is the agent's whole recovery credential:
anything holding it can reattach *as* that agent, rotate its token and take its
mailbox. Putting one in `dibs.toml` would hand the admin identity to every
process running as you, which is worse than the race it closes.

To get the fingerprint, start the agent: `register` returns it as
`fingerprint`, and the daemon, which cannot grant the role yet, logs the line
to paste:

```
to grant it, pin this agent's identity in dibs.toml
  agent=fleet-lead role=admin
  add=[roles.identity]
      fleet-lead = "9f2b…"
```

Paste it in and restart. Nothing secret is typed, stored or sent.

A row that holds a role, or bears a name declared under `[roles]`, is
recovered by its nonce only. A name plus a session id, neither of them
secret, reattaches an ordinary agent that lost the nonce it was given; for a
privileged one that would be a fresh admin token for anyone who can read a
session id off a hook, so `register` refuses it with `E_NEEDS_NONCE`.

Without `[roles.identity]` the role is **not granted**, and the daemon says so.
That is deliberate. Pinning whoever registered first held every later impostor
to the first one's identity and asked the first one nothing, so an agent that
read this file, or simply guessed that `admin = ["fleet-lead"]` is a likely
line, could register under that name before your own agent came up and be handed
the god view with every agent's mail in it. The fingerprint is derived from a
secret you already choose and already give that agent, so naming it here is what
makes the first grant provable rather than merely recorded, while the secret
itself never enters this file.

Twenty lines above, this document tells you never to write the nonce here. It
used to say the opposite down here, and both sentences were in the same
release: a reader who followed the nearer one put the recovery credential in
`dibs.toml`, where anything running as them could read it, register as that
agent and take its token, its mailbox and its role. The daemon now detects and
refuses that value, so following the old advice bought the exposure and not
even the grant.

If you genuinely mean to hand the role to a different agent, put the new
agent's fingerprint here and restart `dibd`. **The config is sufficient on its
own.** A role this mechanism granted, and can prove it granted through
`roles.pinned`, is withdrawn on the next reconciler pass when the config stops
authorising it: the name gone from `[roles]`, or `[roles.identity]` pointed at
a different fingerprint. The predecessor is demoted to member and its pin is
dropped.

Two things are deliberately NOT withdrawn, and both are recorded in the pin
file's absence or disagreement. A role a person granted by hand was never this
mechanism's to take: there is no pin, so nothing is touched, and a role a
person confirmed by hand during this run stands against the reconciler in both
directions. And an agent that holds a declared name with a credential other
than the pinned one, which the launch claim can produce on a fresh board, keeps
what it holds: the stale pin is dropped rather than used to demote somebody it
never described.

This used to be three manual steps, with `dibs admin member <the old agent>`
first, because the reconciler only ever granted. That command still works and
is still the right tool for a role the config never mentioned. Issue #73.

**One agent, one role.** Naming the same agent under both `coordinator` and
`admin` is refused when you spell it the same way in both lists, and validation
compares strings: a name and an id are two strings for one agent, so
`coordinator = ["fleet-lead"]` beside `admin = ["Fleet Lead"]` gets past it.
The reconciler settles that after resolving, where the aliases are visible, and
`admin` wins because it already includes everything `coordinator` can do.

The grant window closes about two minutes after start. A name that never
appears is reported once and then left alone, rather than standing open for
whoever registers under it later. Restarting the daemon re-opens it, which is a
moment an operator is present for.

This file is not reachable through Dibs: it is the operator's, read by the
daemon as itself.

```toml
[roles]
coordinator = ["fleet-lead"]
admin = ["release"]

[roles.identity]
fleet-lead = "9f2b1c…"   # 64 hex characters, from the daemon's log
release = "4d8e07…"
```

These are hashes, not secrets, so this file is not a credential file. That is
deliberate: an earlier design put the nonces here and it was the wrong trade.

---

## Where else settings come from

- **Flags**: `dibd -h` lists them; a flag beats this file.
- **Environment**: `DIBS_ADDR`, `DIBS_DIR`, `DIBS_TOKEN`, `DIBS_ADMIN=1`,
  `DIBS_HARNESS`, `DIBS_CODESIGN_IDENTITY`.
- **Not here**: the coordination secret and the admin password are credentials
  and live as files in the data directory, never in a config file somebody might
  paste into an issue.

`dibs doctor` reads the running daemon and reports what is actually in effect,
which is the answer to "did that setting take?".
