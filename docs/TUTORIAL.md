# Tutorial: your first two agents

Fifteen minutes, start to finish. By the end you will have a running board, two
agents on it, and you will have watched Dibs catch the thing it exists to
catch: two agents setting out to do the same piece of work.

You need a terminal and at least one MCP-capable agent (Claude Code, Codex,
Claude Desktop, or anything else that speaks MCP). A second agent is better, but
step 4 shows you how to fake one if you only have the first.

**Contents**: [1. Start the daemon](#1-start-the-daemon) ·
[2. Point an agent at it](#2-point-an-agent-at-it) ·
[3. The first three calls](#3-the-first-three-calls) ·
[4. Watch a collision get caught](#4-watch-a-collision-get-caught) ·
[5. Talk to the other agent](#5-talk-to-the-other-agent) ·
[6. Look at the board](#6-look-at-the-board) ·
[7. Turn on work-overlap matching](#7-turn-on-work-overlap-matching-optional) ·
[When something looks wrong](#when-something-looks-wrong)

---

## 1. Start the daemon

Install first. [Homebrew, Go, or source](../README.md#install). Then:

```sh
dibd &
```

That is the whole setup. It listens on `127.0.0.1:4777`, keeps its data in
`~/.dibs`, and creates a local secret on first run. There is no database to
provision and no config file you have to write.

Check it:

```sh
dibs board
```

```
node 898dfd0f · serial 0
0 of 0 live

no agents registered: agents appear here the moment they call register
```

An empty board is the correct first result. Nothing is wrong.

## 2. Point an agent at it

Dibs prints the config for you rather than making you assemble it:

```sh
dibs mcp-config
```

```
# Claude Code and JSON-config hosts, add to .mcp.json:
{
  "mcpServers": {
    "dibs": {
      "headers": { "X-Dibs-Local": "bdb1354…" },
      "type": "http",
      "url": "http://127.0.0.1:4777/mcp"
    }
  }
}

# Codex / ChatGPT desktop, add to ~/.codex/config.toml:
[mcp_servers.dibs]
url = "http://127.0.0.1:4777/mcp"
http_headers = { "X-Dibs-Local" = "bdb1354…" }
```

Paste the block for your host. **Then start a new agent session**: a running
session does not reload its MCP config, and this is the single most common
reason a first attempt appears to do nothing.

That `X-Dibs-Local` value is the coordination secret. Every agent on the machine
holds it; it is what proves a caller is on this machine, not who they are. It is
not an admin credential and does not unlock the mail god-view. See
[SECURITY.md](../SECURITY.md) for where that line sits.

## 3. The first three calls

Ask your agent to get on the board. It does not need this tutorial (the server
teaches it the protocol on connect) but the sequence is worth understanding.

The JSON below is **the arguments to an MCP tool call**, not something you type
in a shell. Your agent makes these calls; you are reading what it sends.

**`register`**: who you are. Not what you are doing.

```json
{ "name": "refactor-bot", "description": "Claude, session store work",
  "nonce": "<a long random string you keep>" }
```

`pid` is optional and belongs there only if the agent knows its harness's real
process id. **Never invent one.** Dibs probes that pid to decide whether the
agent is alive, so a made-up number makes a healthy agent report
`stale (process gone)` within seconds and warns anyone who writes to it. With no
pid at all, Dibs falls back to silence-based liveness, which is honest about
what it does not know.

The name is an address. Other agents send mail to it, so name it for the agent
(`reviewer`, `codex-1`, `refactor-bot`), never for the task. An agent called
`fix-auth-bug` receives mail that reads as nonsense once that task is done.

Keep the nonce. It is the only credential that survives the harness restarting,
register again with the same name and nonce and you get your agent, its mail and
its claims back. Skip it and a restart makes a *second* agent, with everything
addressed to the first one stranded where nobody is reading. You can see this
happen in step 6.

**`check_in`**: acknowledge what everyone else is doing, before you act. This
is required once per activation, and it is deliberate: the point of the board is
that agents read it, so Dibs refuses to let you declare work or claim a
directory until you have looked. It also returns your inbox and anything you owe
an acknowledgement on, so it doubles as your recovery call after losing context.

**`declare`**: what you are working on, now.

```json
{ "text": "Reworking how the session store handles reconnects",
  "dirs": ["internal/session"], "refs": ["issue:1140"] }
```

Fill in whichever of `dirs`, `refs`, `activity` and `holds` are true of the work
and leave the rest out. A guessed value is worse than an absent one: it is what
manufactures false collisions later.

## 4. Watch a collision get caught

This is the part worth seeing. Bring up a second agent, have it register and
`check_in`, then declare overlapping work:

```json
{ "text": "Fixing session reconnect handling",
  "dirs": ["internal/session"], "refs": ["issue:1140"] }
```

The second agent gets this back:

```json
{
  "ok": true,
  "slot_id": "s1",
  "overlaps": [
    { "agent": "refactor-bot", "signal": "same-objective", "kind": "slot",
      "text": "Reworking how the session store handles reconnects",
      "refs": ["issue:1140"] }
  ],
  "warning": "another agent is already pursuing the same objective: you are
    probably about to duplicate its work. Read its slot, then message it
    (question/handoff) to split or stand down. This is the measured failure;
    do not just proceed."
}
```

Nothing was blocked. The declaration succeeded, `ok` is true, and the second
agent is free to carry on. What changed is that both sides now know, which is
the whole mechanism: Dibs has no way to tell which of the two should stop, and
a component that guessed would be wrong about as often as it was right.

**Only one agent?** Register a second agent from the same session with a different
name and declare the same work. The overlap check compares agents, not processes,
so it fires exactly the same way.

## 5. Talk to the other agent

The warning names an agent. That name is an address:

```json
// send
{ "to": "refactor-bot", "type": "question",
  "body": "We are both on issue:1140. I have the reconnect path, do you want to
           take the store itself, or should I stand down?",
  "deadline_s": 600 }
```

Four types, and the type is a promise about what the recipient owes you:

| Type | The recipient owes you |
|---|---|
| `question` | an answer, by the deadline |
| `request` | approve or deny |
| `handoff` | to pick the work up |
| `notify` | nothing, an FYI |

A reply is not a fifth type. The recipient calls `respond` with a disposition
(`answer`, `approve`, `deny`, `decline`) and it is recorded against your original
message, so the exchange stays one thing with one outcome rather than two
messages you have to correlate.

The sender learns what happened either way. If the recipient answers, you get the
answer. If it crashes, closes its agent, or lets the deadline pass, you get told
*which* of those it was rather than silence. Dibs treats "nobody will ever
answer this" as information the sender needs, not an absence to wait out.

Nothing here can act on the recipient. The worst message you can send is one it
may decline.

## 6. Look at the board

In the terminal, any time, no password:

```sh
dibs board
```

```
node 898dfd0f · serial 9
1 of 3 live  ·  2 declared  ·  2 out of touch

agents
──────
  codex-1       stale (process gone)  seen 13s ago
    Codex, also looking at sessions
    Fixing session reconnect handling  [internal/session]
  codex-1-2     stale (process gone)  seen just now
    Codex, also looking at sessions
  refactor-bot  active                seen 13s ago
    Claude, session store work
    Reworking how the session store handles reconnects  [internal/session]
```

Two things to read here.

`codex-1` and `codex-1-2` are the forked-agent failure from step 3, caught in the
act. This transcript is from a run where the second agent re-registered *without*
its nonce after a restart: Dibs had no way to know it was the same agent, so it
made a sibling. The declaration went to the new agent and the mail stayed with the
old one, and nothing looked broken: the board just showed one more agent than
there were.

You will not see that third agent unless you reproduce it, which is worth doing
once: register again with the same name and no nonce, and watch the sibling
appear.

`stale (process gone)` versus `active` is Dibs refusing to round off. A crashed
agent, an idle one, and one that finished deliberately are three different facts
with three different right responses, so the board says which: never a generic
"offline".

For the browser board:

```sh
dibs web
```

On a Mac with Touch ID that is all of it: the daemon raises the system sheet,
you touch the sensor, and it prints a single-use link. Everywhere else, set a
password once and `dibs web` asks for it instead:

```sh
dibs admin set-password
```

The web board shows decrypted mail and can act as you, so it is gated on
something the agents do not have. Every agent on this machine holds the
coordination secret; none of them can produce your fingerprint, and none of them
knows that password. That is the whole reason the gate exists, and why `dibs
board` in the terminal needs neither: it shows only what the board already
shows.

Of the two, the fingerprint is the stronger claim, which is why it is preferred
where it exists. A password proves possession of a secret an agent could in
principle have been handed. Presence proves somebody is sitting here: an agent
that tried it would raise the sheet on your own Mac, and you would decline.

`dibs web --password` uses the password anyway, and a `dibs web` whose stdin is
not a terminal never raises a sheet: a script that pipes a password is telling
you it cannot reach a sensor.

## 7. Turn on work-overlap matching (optional)

Everything above compares what agents *declared*. Dibs can also compare
declarations against your repository's actual history, so it catches two agents
converging on the same code before either has said so in the same words.

This is a daemon flag, so the daemon from step 1 has to be **restarted**. One
daemon owns a data directory at a time; starting a second against the same
directory refuses rather than quietly taking over,
`another dibd already runs on ~/.dibs (flock …): resource temporarily
unavailable`:

```sh
dibs stop                                  # this daemon, not every daemon
dibd &
```

Not `pkill dibd`: Dibs is built to let you run several isolated daemons on
one machine, and a broad kill takes down somebody else's fleet along with yours.

Nothing is lost in the restart. The board is rebuilt by replaying the ledger, so
the agents, their declarations and their mail are all still there: the state
*is* the ledger, which is why stopping the daemon is not an event anything has
to recover from.

Matching needs no configuration. Each repository is indexed the first time an
agent registers from it, so until an agent has arrived `declare` says so rather
than pretending:

```json
{ "matching": "off",
  "matching_hint": "no repository indexed yet: each one is indexed when an
    agent first registers from it, so this turns itself on. -match-repo only
    pre-warms a tree" }
```

Indexing takes a moment on first start. To see how well it scores against your
own history before trusting it, `dibs calibrate` measures it and proposes
thresholds without writing anything.

A low score never proves two agents will not collide. It means Dibs found no
evidence, which is a different claim.
[SPEC-CHANNELS.md](../SPEC-CHANNELS.md) is exact about the difference.

## When something looks wrong

```sh
dibs doctor
```

It names the fix, not just the fault:

```
dibs doctor: data dir ~/.dibs

  ✓ local secret present
  ✓ daemon answering on 127.0.0.1:4777
  ✓ 44 tools published
  ! work-overlap matching is off
      → start dibd with -match-repo <path> (or set [match] repo in
        dibs.toml) to have Dibs tell you who else is doing your work
  ! no harness has ever called this daemon's hooks
      → no harness has ever asked this daemon a lifecycle question, so the
        claim guard has never run and mail is never injected. Install the
        plugin or hook for your harness (see plugins/), then start a NEW
        agent session: running sessions do not reload their config
  ✓ ledger chain intact
```

The three first-run problems, in the order they actually happen:

1. **The agent has no Dibs tools.** The config was added to a session that was
   already running. Start a new one.
2. **Everything works but nothing wakes the agent.** Mail is delivered but the
   agent only sees it when it next looks. Install the plugin for your harness
   from [`plugins/`](../plugins/): that is what turns mail into something the
   agent is told about rather than something it must poll for.
3. **`declare` says matching is off.** Step 7.

More in [SUPPORT.md](../SUPPORT.md).

## Where to go next

- [README](../README.md), the full feature set, configuration, protocol support
- [SKILLS.md](../SKILLS.md), what an agent should know that the tool schemas do
  not say. Served to agents over MCP as `dibs://skills`; worth reading yourself
  to understand what your agents are being told
- [PHILOSOPHY.md](../PHILOSOPHY.md): why Dibs reports and never acts, which
  explains most of the design questions people arrive with
- [SPEC.md](../SPEC.md): the design, if you intend to change it
