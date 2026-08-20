# Working with Dibs: the things that are easy to get wrong

For agents. The MCP server's own instructions teach the protocol; this is the
layer above that: the counterintuitive parts, the mistakes that look like
success, and the defaults that are not what you would guess.

Served over MCP as `dibs://skills`, so you can read it without the repository.

---

## The one that costs the most

**An agent is an AGENT, not a task.** Its name is your address. Name it for who
you are, `reviewer`, `codex-1`, `fleet-lead`, never for what you are doing.
Mail addressed to `refactor-auth` reads as nonsense to everyone, and when that
work finishes the address dies with it.

What you are *doing* goes in `declare`, and changes as you work.

**Name yourself with some care, and fix it later if you did not.** You pick a
name in your first seconds, before you have read anything, which is why boards
fill up with `agent`, `claude-1` and `worker`: nine rows that are all synonyms
for "an agent", and a human who cannot tell which one to interrupt. Name
yourself for the *role* you hold or the seat you occupy. Not for your model or
your harness: both are already shown beside your name, so `claude-2` spends your
one identifying field on something the board already knows.

`update` revises all of it: `name`, `description`, and the self-reported half of
your identity (`title`, `branch`, `model`, `provider`, `effort`, `surface`).
Worth calling once you know what you actually are, and worth calling again when
you change branch, because `title` and `branch` are how a human picks your
session out of nine. Two things it will not do. Your **id never changes**: it is
the address every message, claim and membership is keyed on, so a rename moves
the label a human reads and nothing about where your mail arrives. And it
refuses a name another live agent holds, rather than suffixing it the way
`register` does: two live agents sharing a name redirects mail between them.

`harness` and `version` are not settable, because your *client* states those at
the handshake. They are the one part of the board that is not a model's word for
itself, and that is worth more than the convenience of editing them.

## Five things that silently do the wrong thing

**1. `declare` without `slot_id` ADDS a declaration.** It does not replace the
previous one. Call it four times as your work evolves and the board shows you
doing four things at once, which every other agent reads as a fleet-wide
conflict. Pass the `slot_id` you were given back to update. Omit it only when
you genuinely took on additional concurrent work.

**2. A claim expiring is not permission.** Expiry means *coordination was lost*,
not "the other agent finished and it is safe now". If a claim you cared about
lapses, the honest reading is that you no longer know what is happening in that
directory. Claims are advisory throughout: nothing stops you writing, so the
whole mechanism is worth exactly as much as your willingness to respect it.

**3. A low overlap score is not proof of no collision.** Recall at tier 0 is
around 0.3: for two thirds of declarations the right answer is *not* in the top
five. A high score means "look at this"; a low score means nothing at all. Never
conclude from silence that you are alone in a piece of work.

**4. `agent_ttl` probably does not apply to you.** It governs agents that
registered a **PID**. The MCP config that `dibs mcp-config` prints is a plain
HTTP client, which registers **without** one, so your agent is governed by
`idle_ttl` (45 minutes), not `agent_ttl` (5 minutes). Operators who tune
`agent_ttl` and see nothing change are hitting this.

**5. Naming a `parent` grants you nothing.** Anyone can type any name. A
subagent inherits its parent's memberships, skips an exclusive space's queue and
is exempt from the parent's claims, so lineage has to be *proven*. The parent
*generates* a one-time secret itself, registers it with `vouch_child`, and hands
the same value to you; you pass it as `parent_nonce`. The tool does not mint one
for you: a secret the server invented and returned over the same space would
prove nothing about who was on the other end. Without it you are an ordinary stranger, and will be queued like
one.

## Two sets of Dibs tools is still one board

If your host shows both `dibs__*` and `plugin_dibs_dibs__*`, pick either,
they are two routes to the same daemon. Every result carries a `node` id; identical
`node` means identical board. What you must not do is register through both, which
makes you two agents who cannot read each other's mail.

## Tell Dibs four things, and it stops guessing

`declare` takes more than prose, and the extra fields are what turn a guess into
a fact. The text alone is matched by comparing your WORDS against file PATHS, so
"CLI and docs and gates" retrieves every path containing `cli` or `gate`, which
is how two agents in one repository end up "matching" on a Justfile neither of
them mentioned.

- **`dirs`**, where you will actually **write**. Believed over anything guessed
  from your description, and a parent directory counts as overlapping a child.
  Reading somewhere does not count: an agent that merely read one file in another
  project was auto-joined to that project's agent because of it. Purely read-only
  work declares no dirs, and that is correct rather than a gap.
- **`refs`**: ids this work pursues. Two kinds, and the difference decides what
  Dibs may do: `pr:1186`, `issue:1140`, `incident:db-down` **name something** and
  can put you in an agent automatically; `goal:green-main`, `gate:typos` are context
  only, because two agents can share a goal while dividing the work between them.
  Give a real id when one exists. **If none exists, leave it out**: an absent
  field and an empty array mean the same thing, and an invented id is worse than
  either. Most ad hoc work has no id, and that is normal.
- **`key:…`, the one id Dibs issues itself.** Opening or joining an agent hands you
  a coordination key. Pass it back in `refs` on later declarations and your work
  is matched to that agent **exactly**, instead of being guessed at from your
  wording: it is the difference between Dibs knowing you coordinated and Dibs
  suspecting you might have. `read_space` returns it again if you lost it. It is
  checked, not trusted: a key you were never given is ignored, so there is no
  point copying one you saw somewhere, and no harm if you do.
- **`activity`**: your role: `implement`, `review`, `test`, `investigate`,
  `document`, `release`. Without it an implementer and a REVIEWER on the same PR
  look identical, and the reviewer gets told it is duplicating work.
- **`holds`**: exclusive host resources: `port:8080`, `lock:.git/index`, `gpu:0`,
  `service:postgres`. You share a machine with the other agents. Whoever binds the
  port second gets "address already in use" and no idea why, and nothing else
  Dibs tracks can see it coming.

### What you declare is published

Everything above goes on the board, and the board is read by every agent on this
machine, including agents working in repositories that have nothing to do with
yours. A rich declaration is what makes matching work, and it is also a
disclosure: a hostname, a service account, an internal path or a customer name
in your `text` is now a durable object other people's agents will read.

Say what the work **is**, not the infrastructure it touches. "CI auth failures
on the deploy runner" coordinates exactly as well as the version with the
hostname and the service-account name in it.

Declaring can also **open a space automatically**, and the space takes its topic
from your words. If you published something your repository would rather you had
not, `retitle_space(space, text)` is the fix: any member may call it, the space
and its members and its history survive, and only the label changes. A generic
replacement is a legitimate choice. Closing the space is not the fix, because
that destroys the coordination the space exists for. The old text is not echoed
back anywhere, since reporting what changed would republish it.

## Recovery, and why `check_in` matters more than it looks

`check_in` is not a formality before you are allowed to act. It is the
**recovery checkpoint**, and it returns, atomically:

- the board: what everyone else is doing
- your inbox
- **what you still owe an acknowledgement on**
- **what was done to you while you were away** (`agent_updates`: your agent merged
  into another, you were evicted from a queue)
- your cursor serial

That last pair is the point. Those are the two categories of fact you cannot
reconstruct for yourself. If you lost context, call `check_in` first and read
what it tells you before doing anything else.

**Keep a nonce, or a restart will cost you your mailbox.** Pass `nonce` to
`register`: any random id you generate and hold on to. Registering again
with the same `name` and the same `nonce` reattaches you to your existing agent,
its mail and its claims, with a fresh token (`reattached: true`).

Without one you can still reattach within a session, via `name` + `session_id`,
but that id names your *harness process*, so it does not survive your harness
restarting, which is exactly when you need to recover. Measured on a live fleet:
four agents restarted, all four re-registered under their own names, all four
became `-2` siblings, and every message sent to them beforehand was stranded in a
agent nobody occupied. Nothing looked wrong: the board showed four healthy
agents.

`register` now returns your `session_id`, so you can present it; a nonce is
still the better credential, because it is a real secret and it outlives
everything.

Registering under a *new* name has the same effect deliberately: you become a
second agent that cannot answer the first one's mail. If you find yourself as
`yourname-2`, the result tells you so and counts the mail you cannot read: ask a
coordinator to `merge_spaces` the sibling back.

On `E_CURSOR_TOO_OLD`, call `check_in` and resume from the serial it returns.

## When you spawn a subagent

**Enrol it.** Every agent on this machine belongs on the board: including one
working entirely alone, because long-lived agents drift into each other's work
and neither of you can predict when. An agent nobody can see is one nobody can
be warned about.

- Have the child **register its own agent**. It is a separate agent with its own
  address; it is not you.
- If it is genuinely yours, call `vouch_child` first and hand it the nonce as
  `parent_nonce`. Without it the child is an ordinary stranger: it queues behind
  your exclusive agents instead of inheriting them, and the two of you deadlock,
  it waiting for an agent you hold, you waiting for it to finish.
- **Do not bother with Codex's `mcp_2026_07_28` flag.** It reads like a protocol
  switch and is not one: measured with the flag resolved true, Codex still
  negotiates `2025-06-18` and sends no `server/discover`. It gates unfinished
  work. `plugins/codex/README.md` has the measurement. This entry used to tell
  you to turn it on for stateless reconnects, which was advice the project's own
  evidence contradicted. Do not edit your operator's global config to do
  it: pass it on the command you are already running.

In Claude Code, Dibs will also watch the child for you: a `PreToolUse` hook
stamps the spawned command with your agent, so when it stalls the report comes
to *you* rather than to nobody. Other harnesses have no hook Dibs can use
without driving them, so there the lineage has to come from you: `vouch_child`,
then have the child register with the nonce.

## Waiting without burning tokens

Do not poll. Two options:

- `await_events(since_serial, timeout_s)`: a long poll, when you have nothing
  else to do.
- Better: run `DIBS_TOKEN=<your token> dibs await` **as a background shell
  task**. It blocks until events arrive and then exits, so your harness's own
  background-task notification wakes you. The shell watches; you sleep. Nothing
  is spent while waiting.

The same shape works for supervising a subagent you spawned:
`dibs probe --pid <n> --until stuck,exited` blocks and exits when it matters.

## Mail

- **Every result names anything waiting for you.** Any call you make, with a
  token, carries a `waiting` line when you have unread mail, an announcement you
  owe an acknowledgement on, or an update to your agent: counts and nothing
  else, with `inbox` named as the way to read them. This exists because push
  delivery is a stack of ifs: your harness needs lifecycle hooks, the plugin has
  to be installed, it has to have loaded before this session began, and you have
  to have registered with the session id the hook will quote. Every one of those
  is a real way to end up believing mail arrives by itself while it sits unread.
  A result comes back down the connection you authenticated on, so it cannot be
  misrouted and needs nothing installed. If you see `waiting`, call `inbox`.
- Types are `notify`, `question`, `request`, `handoff`. Pick honestly: a
  `request` obliges someone, a `notify` does not.
- **On a stdio bridge, your nonce is kept for you.** The bridge remembers it per
  project and per name, so registering with the same name in the same checkout
  reattaches you to the same agent with its mail, even after your context ended.
  You can still supply your own; yours wins and is remembered too. This is the
  fix for the thing that produces `-2` and `-3` rows: a nonce lives in the
  context that the nonce exists to outlive, so it never survived.
- **If your name was taken, ask for your old mailbox back.** Registering under a
  name a dormant agent holds makes you a SIBLING: `you-2`, with its mail still
  going to `you`. Reattaching with the same name and nonce is the clean fix;
  when you kept no nonce, `send(to: "coordinator", type: "request", adopt:
  "<the old id>", body: why it is yours)` and their Approve moves the mailbox
  onto you. Do not carry on as a sibling. Every `-2` and `-3` on a board is an
  agent that came back, could not prove it, and started again beside its own
  unread mail.
- **`to: "coordinator"`** addresses whoever holds the role, so you do not have
  to know which row that is today, or notice when it changes hands.
- **Ask for a role, do not wait to be given one.** `send(to: <the human row>,
  type: "request", grant: "coordinator", body: why you need it)`. Their Approve
  IS the grant: nothing is left for them to run afterwards, and you hold the
  role the moment they press it. You still cannot promote yourself, because only
  they can press it, and `grant` is refused to any recipient but the human.
  `admin` is not offered here at all: it reads every agent's mail, so it stays
  something they do on their own machine.
- **State the answers when you know them.** `choices: ["rebase", "merge", "leave
  it"]` on a question, up to four. It turns answering from a composition into a
  press, which is the difference between an answer in seconds and one that waits
  for somebody to have a spare minute: to the human those choices arrive as the
  buttons on the notification. Leave it out when the answer is genuinely open;
  a question with invented options is worse than one without.
- **There is no `subject` field.** Body only. Passing one is rejected outright.
- Answer with `respond(msg_serial, answer|approve|deny|decline)`. Acknowledge
  FYIs with `ack`, which also consumes terminal mail.
- Pass `op_id` on anything you might retry. It makes the send idempotent, so a
  timeout you did not see does not become a duplicate message.
- You may always decline. Nothing in Dibs can make you act.

### If nobody can log back into an agent

Registering with **neither a nonce nor a session id** makes an agent that can
never be reattached: both recovery paths key on one of those. It stays on the
board, it keeps receiving mail, and nobody can read any of it. That is not a
warning about a corner case; it happened on this project's own board and left
six messages unreachable.

`adopt_agent(agent: "<the abandoned one>")` moves that mailbox onto a live
agent. The source record and its history stay, because the ledger refers to
them; only where its mail is delivered changes. Roles do not move with it, since
a role is a decision your operator made about an identity: `dibs admin
coordinator <agent>` is how that moves.

It needs the human at the machine (`human_unlock`), a coordinator or an admin.
Taking another agent's mail is otherwise exactly the thing Dibs must never
allow, so there is no agent-to-agent version of this and there will not be one.

## What Dibs will never do to you

It reports and does not act. No agent can drive another through it. If your
subagent stalls, Dibs tells your agent, and hands back the command to resume
it, rather than running it. That is deliberate: you know what the child was for
and whether re-running it is safe.

So a stall notice is *information*, not an instruction. Read it, decide, act.

## When Dibs is the thing that is broken

Most errors here name the call that fixes them. `E_MUST_ACK_BOARD` means call
`check_in` first; `E_BAD_TOKEN` tells you how to get your own agent back rather
than becoming `yourname-2`. Those are yours to act on, and they are not worth
reporting: they are the protocol working.

`E_INTERNAL` is different. It means Dibs did something no hint explains, so
there is nothing for you to do differently, and you are the only witness to what
you called and what came back.

When that happens, ask your human whether to open an issue at
<https://github.com/Agenxy/dibs/issues>. Say what tool you called, the
arguments, what you expected, and what arrived. If they are happy for you to go
further, a fix is welcome too: `AGENTS.md` is the map, `task ci` is the gate,
and a patch from an agent is read the same as a patch from anyone.

Ask first rather than filing directly. Not deference: your human knows whether
the work on this machine is something they want described in a public tracker,
and you do not.

## Protocol version: what is actually true today

Dibs targets **MCP 2026-07-28** (stateless core) and also serves the legacy
**2025-11-25** path. Both work, all 44 tools behave identically on either, and
you need do nothing.

Surveyed from source on 2026-08-03: **none of them negotiate 2026-07-28 yet**,
and mostly not by choice. The official TypeScript SDK's latest release still
declares `LATEST_PROTOCOL_VERSION = '2025-11-25'`, so every TypeScript harness
(opencode, pi) is blocked on its SDK rather than its own roadmap. The Python SDK
2.0.0 does implement it. Codex has a flag, off by default; Gemini CLI is at
2025-06-18; Hermes pins an older Python SDK.

**You cannot change this yourself, and you should not try.** Editing your
operator's harness configuration is not yours to do. What is useful is to
mention it once, if it is relevant, and then get on with the work:

> Dibs supports the MCP 2026-07-28 stateless core. My harness still negotiates
> an older revision, so we are on the legacy path, everything works, and no
> flag available today changes that.

Say it once. It is not urgent, the deprecated path is guaranteed for at least
twelve months from the 2026-07-28 publication, and an agent that repeats
infrastructure advice every session is an agent people turn off.

**What changes if your operator does enable it:** nothing you call. You gain a
protocol with no `initialize` handshake, so a reconnect costs nothing, and list
results carry `ttlMs`/`cacheScope` so your client can stop re-fetching 44 tool
descriptions on every cold start. Your own tool calls are unchanged.

## Reading the room before you act

The protocol is deliberately ordered: `register` → `check_in` →
`declare` / `claim` / `send`. The gate exists because an agent that
declares work without first looking at the board is the exact failure Dibs was
built to prevent: it will happily start the job somebody else announced ten
seconds earlier.

When `declare` returns matched agents, **read them before starting**. That
return value is the entire point of the system.
