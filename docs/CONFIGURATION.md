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
| `name` | *(none)* | A hostname for this board, such as `dibs` or `board.lab`. [Remap](https://github.com/Agenxy/remap), the Agenxy name plane, routes it to the daemon (`remap set <name> http://127.0.0.1:4777/`; `dibs configure` offers to do this when Remap is installed), and the daemon accepts it as its own origin so the board works at `http://<name>/`. `dibs web` prints that link beside the address, and `dibs doctor` says when the name is set and Remap does not route it. |
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
| `open_app_after_idle` | `10m` | Claude closed-session recovery only: AFK interval, with lock or display sleep qualifying sooner. ChatGPT queued wakes open promptly with a per-thread bound, independently of presence. |
| `resume_after_app_restart` | `0s` (off) | Recent Dibs-activity window for reopening local ChatGPT-app Codex threads after an observed app process restart. Needs an existing `codex queue` wake entry. |
| `restart_open_interval` | `2s` | Minimum gap between the restart sweep's thread opens. |
| `remind_stale_after` | retired | Did nothing since liveness became the daemon's own job. Still parsed so old configs load; delete it. |
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
shaped by: the daemon starts a process, so the process starts here. A command
can care where it starts (the retired `codex exec resume` refused outside a
trusted directory), so when the recorded directory is missing the wake runs
where the daemon does and may fail, with a warning saying so.

Two ways for it to be missing. A removed worktree is the local one. An agent on
ANOTHER COMPUTER is the other, and there nothing you write in this file can
help: the command would have to run over there. On that machine, put the
`[wake.exec]` table in the data directory `dibs mcp-config --board` made for the
board and run `dibs host-bridge` with the same `DIBS_ADDR` and `DIBS_DIR`: it
attaches to the hub for that machine, and when the hub decides one of the
agents there should be woken, the bridge runs that machine's own command with
the same substitutions, and the hub takes its report as the exit status. The hub
never learns or runs the remote command. `dibs host-bridge --service` writes the
unit that keeps that bridge running. `dibs doctor` on the hub counts such an
agent as covered while its bridge is attached, and on the joined machine says
which half is missing. See [NETWORK.md](NETWORK.md) for why the hub decides THAT
an agent is woken and only its own machine can decide how.

**Dibs never hosts an agent.** A wake command must DELIVER a message into the
harness that already runs the agent, and must never start the agent itself.
`dibd` and `dibs host-bridge` both refuse a command that would, and repair the
one recipe this page used to recommend (see below), so a hand-written entry
cannot turn the board into a harness either. `dibs doctor` says when it has
done so.

```toml
[wake.exec.codex]
argv     = ["/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
            "queue", "--thread", "{thread}", "--message", "{message}"]
```

`codex queue --thread <uuid> --message "<text>"` hands the message to the
ChatGPT app, which delivers it into the thread it holds: the app's own
app-server drains the queue and injects it as a user message. That is a
channel into the harness the agent lives in, which is all a wake may be.

The app delivers a queued message only to a thread it has loaded, so when the
app is not holding the thread, Dibs then opens it there with `open -g
codex://threads/<id>`, launching the app if it is closed. That happens only for
an agent whose bridge found the ChatGPT app above it in the process tree; a
Codex in a terminal is never opened in the app. On another machine, `dibs
host-bridge` does the same on that machine.

A loaded thread receives queue-only delivery. An unloaded ChatGPT thread opens
promptly even while you are active. `-g` requests background opening; measured
on the current app, it can still briefly activate ChatGPT before focus returns.
A per-thread memo prevents repeated opens for ten minutes, including when the
ownership probe is unavailable. A new app incarnation or an observed loaded
then unloaded thread can re-arm sooner, subject to a twenty-second rate limit.
Messages themselves do not reset the memo. No decision window is opened.

`open_app_after_idle` now applies only to Claude closed-session recovery. Its
signed helper still checks lock, sleeping displays or measurable HID idle.

App-restart recovery is separate and off by default. Set
`resume_after_app_restart = "1h"` to select local ChatGPT-app threads whose
Dibs activity was observed within the previous hour, whether or not they have
queued mail. Dibs samples their own declaration slot IDs and update serials while
the old app process is running, records those references at a stable replacement, queues
a dated factual notice, and opens selected threads at least
`restart_open_interval` apart. Its next token-authenticated `check_in` or
`inbox` quotes full text for unchanged slots once. A changed slot is labelled
updated since restart, and a cleared slot is identified without stale text.
No declaration text is copied into the restart ledger op. A daemon restart loses the
in-memory observation baseline, so a replacement during daemon downtime may
not be detected. A queued notice or successful open does not prove the model
acted.

```toml
[wake]
open_app_after_idle = "10m"  # Claude recovery only; ChatGPT wakes do not wait
```

The message is queued meanwhile, and only the first wake per thread per app
run needs an open at all: a loaded thread stays loaded and is delivered to
silently. A wait that outlasts a day is dropped; the message is still in the
app for whenever the thread is next opened.

**Why not `codex exec resume`, which this page recommended until 2026-09-30.**
It does not deliver to anybody: it starts a headless Codex and runs the thread
itself, in a process Dibs started, outside the app the operator was using, on
their model allowance, with nobody watching. The operator found their ChatGPT
threads running that way and called it unacceptable, rightly. Every entry that
followed the old recipe had `exec resume` as `argv` and `codex queue` as
`fallback`; the daemon now drops the first and runs the second alone, so those
configurations keep working with no edit. Write the one above.

**Claude Code needs no `[wake.exec]` entry.** A Claude Code session is reached
through its own session socket and its hooks, which deliver into the running
session. The command this page used to give, `claude --resume <id> -p`, started
a headless Claude Code session of Dibs' own, which is the same thing as above
and is refused for the same reason. `dibs doctor` counts Claude Code agents as
covered without a command.

`fallback` obeys every rule `argv` does: whole-element substitution, no shell,
argv[0] named in this file and never a placeholder.

**A wake runs in the agent's own directory.** It has to, and for a long time it
did not. The retired `codex exec resume` refused to start outside a trusted
directory, and a daemon started by launchd has `/` as its working directory, so
every wake it attempted exited 1 without reaching anyone.

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

**You can open the socket route instead, and it is one line.** The hold is a
default rather than a rule: Claude Code holds unattested peer messages only
while a session bypasses permission prompts, and an explicit setting always
beats that default. In `~/.claude/settings.json`:

```json
{ "crossSessionInbound": "accept" }
```

Measured on 2026-09-23 against 2.1.280: with the default, a wake to a
bypassPermissions session is held with cause `no-mode-asserted` and no turn
starts; with `accept`, the same bytes start a turn. The trade is real and it is
yours to make. Permission prompts are what stands between text arriving and an
agent acting on it, and a bypass session has already removed that check, so
`accept` means any local process that can read a session's peer key can put a
line in front of that agent. That is the same boundary `~/.dibs/local.secret`
sits behind, which is why Dibs is willing to name the setting, and it is a
boundary rather than nothing, which is why Dibs will not write it for you.
Still no receipt either way: `accept` makes the route work, it does not make
it confirmable.

`dibs doctor` reads this rather than lecturing about it. Once the setting is in
place it reports the route as open and stops offering the fix; if a managed
policy or a checkout is what holds, it names that file instead, because "set
accept in your own settings" is an afternoon wasted on a machine where somebody
else has already answered.

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

The receiving harness decides how to handle mail during a busy turn. Neither
lease status, recent contact nor a successful-delivery cooldown suppresses a new
item. Arrival bursts are batched for 200 ms; an executing delivery command
reconsiders new arrivals when it exits. Only actual failures get a bounded retry.
The former `[wake.exec.*].cooldown` key is refused, including empty or zero
values: remove that line. `dibs doctor` names it in its corrective hint.

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

### `remind_stale_after`: RETIRED, and the daemon does this itself now

This governed a line telling a session it had not coordinated with the board
for hours and should `check_in`. The setting is still parsed so an existing
`dibs.toml` loads, the daemon says it is retired if you set it, and you can
delete it.

The problem it was written for was real: an agent registers, declares, works
for hours without calling Dibs, its lease lapses, the board reports it dormant
while it is busy, and peers writing to it are told "recipient is dormant". On
this project's own board a seven-hour run expired a peer's question and the
operator reported Dibs as broken.

**The reminder was the wrong half of the fix.** It asked the agent to announce
something the daemon could already see. Every harness lifecycle hook that fires
is proof that session exists and just took a turn, and the daemon was recording
those hooks and then judging staleness on a different clock: it held the
evidence and complained anyway, in a message delivered BY the hook that was the
evidence. The operator's report was a Stop hook reading "you have not
coordinated with the board for 9h33m".

So liveness is derived rather than requested. Any hook stamps it, the sweep and
the board row read the same answer, and an agent taking ordinary turns is
neither swept dormant nor asked to say it is there. `idle_ttl` still governs an
agent nothing has been heard from at all, which is the case where silence is
the only evidence there is.

The reminder was not reworded, because after this it could not be true for
anyone able to receive it: its only delivery route was a hook, so it reached
exactly the population whose liveness is now provable, and its claim that peers
might be told they were dormant was false for all of them.

---

## `[invites]`: autonomous cloud-worker credentials

The `invites` table controls local issuers on the PRIVATE MCP endpoint; invited
agents can never mint further invitations, even after a role grant. The
operator configures the separate public listener once (NETWORK.md §9).

```toml
[invites]
who = "any"
max_live = 4
max_ttl_s = 604800
```

| Setting | Default | Meaning |
|---|---|---|
| `who` | `"any"` | `"human"` allows only proved private human issuance; `"coordinator"` also allows local coordinators; `"any"` permits any local agent. |
| `max_live` | `4` | Live invitations per agent issuer, 1..1024. Coordinators have the same cap; human proofs are not capped by agent policy. |
| `max_ttl_s` | `604800` | Agent invitation lifetime ceiling in seconds, 1s..365d. Omitted lifetime defaults to seven days or this lower ceiling; the human can explicitly choose up to 365d. |

Ordinary agents must use their own immutable ID prefix, and cannot enter
another existing agent's narrower namespace. Coordinators and the human may
name other new unprivileged identities. Permission changes govern NEW issuance,
not existing keys. Only the issuer or human may revoke an invitation (one
`name`, or all `issued_by` that issuer). Signing off/closing/purging an issuer
revokes its children; reopening cannot revive them. Resume, token rotation and
archive do not revoke them. See `invite` and `dibs invite` for configuration
recipes; use `DIBS_TOKEN` in the CLI to act as an agent, not prompt a human.

The standalone direct-IP listener is explicit: `--public-ip <assigned global
IPv6>` with `--ack-unverified-guest-client`; `--public-addr` must name that
same IP and a numeric port (default 4778). It uses a separate constrained
guest CA, never rotates the fleet CA, and currently offers no verified native
client recipe. The acknowledgement does not prove safe harness trust or WAN
reachability. Address stability is operator-asserted; address loss withdraws
recipes. See NETWORK.md §9 for trust material, renewal and re-invitation.

## `[relocate]`: moving an agent to another environment on purpose

A wake reaches an agent in the environment it runs in and nowhere else: a
ChatGPT-app thread in the app, a Claude Code session through its own socket,
and never by starting the agent somewhere of Dibs' choosing. Moving an agent
is a separate act, done on purpose by somebody allowed to, and recorded.

```toml
[relocate.headless]
argv = ["codex", "exec", "resume", "{thread}", "{message}"]
```

Each entry is an environment an agent can be moved to by name. The command
runs the agent's thread there, which is exactly what a wake command is refused
for (see "Dibs never hosts an agent" above): this table is the only place Dibs
reads a command that hosts an agent, and no wake ever consults it. Placeholders
are whole argv elements, as for a wake: `{thread}`, `{agent}`, `{message}`
(a fixed "Dibs: something is waiting." line). `chatgpt-app` is built in and
needs no entry: it opens a Codex thread in the ChatGPT app with the app's own
`codex://threads/<id>` route.

**Who may move an agent.** You, always: `dibs admin relocate <agent>
<environment>`. A coordinator or admin agent, by role, with the `relocate`
tool. Any other agent only once you have granted it, either with `dibs admin
may-relocate <agent>` (and `may-not-relocate` to take it back) or by pressing
Approve on a request it sent carrying `grant: "relocate"`. Every move is
ledgered as `agent.relocated` with who did it, the agent, and where it was and
went to, and the agent's board row shows the last one.

**What is refused.** An agent that is running (starting its thread somewhere
else as well would put two writers on one thread); the environment it already
runs in; an agent on another machine (the command would run on this one); an
agent with no harness thread on record; and your own row, since where you work
is yours to decide.

## `[identity]`: who an unidentified session is taken to be

```toml
[identity]
unidentified = "directory"   # the default
```

A lifecycle hook says which agent it belongs to by passing a session id. When
it cannot, Dibs matches the single live agent working in that directory. This
setting decides whether it may.

| Setting | Default | Meaning |
| --- | --- | --- |
| `unidentified` | `"directory"` | Who a hook with no session id resolves to: `"directory"`, `"strict"`, `"ask"`, `"coordinator"`. |

- `"directory"` resolves to the one live agent working there. Two agents in
  one directory is still refused as ambiguous.
- `"strict"` resolves to nobody. The session registers, or goes without.
- `"ask"` resolves to nobody, and you get a notification naming the directory
  and who it would have been.
- `"coordinator"` resolves to nobody, and the coordinator agent gets a notice
  saying the same.

**Why the default is `directory`.** It is not laxness, it is the only thing
that works for a harness which cannot identify itself. Claude Code and Codex
both interpolate a session id into their hooks. Gemini CLI's hooks are plain
commands with no template variables, so it has nothing to pass: without the
fallback a Gemini agent could never be woken at all.

**When to change it.** Several agents in one monorepo, where "the agent working
in this directory" stops being one agent and you would rather a new session
prove who it is. `ask` and `coordinator` are for the same situation when you
want the decision made rather than skipped.

The three that resolve to nobody lose nothing permanently: the mail stays on
the board and is delivered the moment that session identifies itself. What they
cost is the immediacy a guess would have bought.

**One rule this setting does not reach.** A session id that was SUPPLIED and
matched nothing resolves to nobody under every policy, including `directory`.
That is positive evidence it is a different session, never a hint to look for a
neighbour: without it, an unregistered session in a shared repository was
attributed to whichever agent was registered there and handed that agent's
private mail.

Notifications are throttled per directory, because a harness fires lifecycle
hooks continuously and an unidentified session in a loop would otherwise raise
one at every turn boundary.

## `[hooks]`: what a lifecycle-hook delivery carries

```toml
[hooks]
mail_bodies = true   # the default
```

When a harness lifecycle hook delivers to an agent, the digest injected into
that agent's context carries the message text. Set `mail_bodies` to `false` and it
carries a pointer instead: who is waiting, of what kind, and the `read_mail`
call that fetches it.

**Why the default is on, and why it was off for a while.** A delivery used to
say only that something had arrived, so the recipient spent `check_in`,
`read_mail` and `ack` finding out what, behind its harness's own warning
preamble. A message service whose recipient makes three calls to read one
message is a polling API with extra steps.

The body had been removed on a measured finding: `hook_poll` is authenticated
by nothing, because a harness lifecycle hook has no token to give, so any
process holding this machine's coordination secret can name a peer's working
directory and be answered as that peer. That mechanism is real and unchanged.
The THREAT was wrong. Every agent on a board is the same person's agent,
holding the same secret and already able to call every tool on it, so a
confidentiality boundary between them is not protecting you from anyone.

**What turning it off is actually for.** A machine whose accounts are not all
yours. That is the situation, and it is the only one.

One thing the setting does not reach, because it is structural rather than
configurable: the surfaces a HOST may attach to YOUR turn never carry a body,
on or off. The human notice and the ambient `waiting` line say who and what
kind and nothing else. That rule exists because the opposite shipped three
times through three different channels, each found by an operator watching
their own prompt box fill with mail addressed to an agent.

Quoting is bounded by a budget shared across the whole digest rather than
applied per message: ten messages each trimmed to a generous length is not a
generous digest. What does not fit keeps its `read_mail` pointer.

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
authorising it: the name gone from `[roles]`, its `[roles.identity]` entry
pointed at a different fingerprint, or that entry deleted. The last of those
is withdrawal for the same reason the others are: a declared name with no
fingerprint can never be granted, so a config missing it authorises nobody.
The predecessor is demoted to member and its pin is dropped.

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

## Changing a setting without an editor: `settings`

An agent you have granted **admin** can read and change the settings that take
effect while the board is running, with the `settings` tool. Reading needs
only a token; changing normally needs admin. A coordinator can change only
the two app-restart controls.

It is called `settings` and not `configure` because `dibs configure` is a CLI
wizard that writes `dibs.toml` before a board runs, and two different things
under one verb is how somebody ends up running the wrong one.

```
settings(token)                                    # list everything
settings(token, setting: "wake.sockets", value: "false")
```

The listing gives every settable key, its value now, and who last set it, so
"the board is behaving differently than I expect" is answerable in one line.

The five admin file overrides are `wake.extend_turn_for`,
`wake.notices_wake`, `wake.sockets`, `hooks.mail_bodies` and
`identity.unidentified`. The two restart controls are
`wake.resume_after_app_restart` and `wake.restart_open_interval`; coordinator
or admin changes to those are ledgered with the actor and time. All seven take
effect immediately. An address or a certificate
needs a restart, so `settings` refuses it with `E_NO_SETTING` rather than
reporting success and doing nothing until somebody happens to restart the
daemon.

**Admin for the five general settings.** A coordinator runs the fleet: evicting, adopting
and force-releasing are all visible on the board and undoable from it.
Changing a setting changes how the board behaves for every agent on it,
including the ones that will never look at this file, so it is the grant a
person makes deliberately.

### Where a change is written

Not into `dibs.toml`. That file is mostly your comments and your reasoning,
and a daemon that rewrites TOML destroys them: it would hand back a file a
machine can read and a person cannot.

The five admin overrides go to `overrides.json` beside it, layered on top at
boot. The two restart controls go into the append-only ledger and override
their `dibs.toml` values on replay:

```json
{ "set": { "wake.sockets": { "value": "false", "by": "coordinator",
                             "at": "2026-09-24T14:02:11Z" } } }
```

The hand-written file stays exactly as written. Deleting `overrides.json`
reverts only the five general overrides; it cannot erase ledgered restart
control changes. Use `settings` to revise those and see their setter and time.

If the file cannot be read at boot, the daemon says so loudly and runs what
`dibs.toml` says: refusing to start would let an agent take the board down by
writing a bad byte, and ignoring it silently would leave settings that are
written down and not applied.

## Where else settings come from

- **Flags**: `dibd -h` lists them; a flag beats this file.
- **Environment**: `DIBS_ADDR`, `DIBS_DIR`, `DIBS_TOKEN`, `DIBS_BOARD_PEER` (the
  hub as a Supgang peer; the bridge dials the address Supgang has signed for
  it now, keeping the scheme and port of `DIBS_ADDR`), `DIBS_ADMIN=1`,
  `DIBS_HARNESS`, `DIBS_CODESIGN_IDENTITY`.
- **Not here**: the coordination secret and the admin password are credentials
  and live as files in the data directory, never in a config file somebody might
  paste into an issue.

`dibs doctor` reads the running daemon and reports what is actually in effect,
which is the answer to "did that setting take?".
