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

**4. `lane_ttl` probably does not apply to you.** It governs agents that
registered a **PID**. The MCP config that `dibs mcp-config` prints is a plain
HTTP client, which registers **without** one, so your agent is governed by
`idle_ttl` (45 minutes), not `lane_ttl` (5 minutes). Operators who tune
`lane_ttl` and see nothing change are hitting this.

**5. Naming a `parent` grants you nothing.** Anyone can type any name. A
subagent inherits its parent's memberships, skips an exclusive agent's queue and
is exempt from the parent's claims, so lineage has to be *proven*. The parent
*generates* a one-time secret itself, registers it with `vouch_child`, and hands
the same value to you; you pass it as `parent_nonce`. The tool does not mint one
for you: a secret the server invented and returned over the same space would
prove nothing about who was on the other end. Without it you are an ordinary stranger, and will be queued like
one.

## Two sets of Dibs tools is still one board

If your host shows both `lanes__*` and `plugin_lanes_lanes__*`, pick either,
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

- Types are `notify`, `question`, `request`, `handoff`. Pick honestly: a
  `request` obliges someone, a `notify` does not.
- **There is no `subject` field.** Body only. Passing one is rejected outright.
- Answer with `respond(msg_serial, answer|approve|deny|decline)`. Acknowledge
  FYIs with `ack`, which also consumes terminal mail.
- Pass `op_id` on anything you might retry. It makes the send idempotent, so a
  timeout you did not see does not become a duplicate message.
- You may always decline. Nothing in Dibs can make you act.

## What Dibs will never do to you

It reports and does not act. No agent can drive another through it. If your
subagent stalls, Dibs tells your agent, and hands back the command to resume
it, rather than running it. That is deliberate: you know what the child was for
and whether re-running it is safe.

So a stall notice is *information*, not an instruction. Read it, decide, act.

## Protocol version: what is actually true today

Dibs targets **MCP 2026-07-28** (stateless core) and also serves the legacy
**2025-11-25** path. Both work, all 40 tools behave identically on either, and
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
results carry `ttlMs`/`cacheScope` so your client can stop re-fetching 40 tool
descriptions on every cold start. Your own tool calls are unchanged.

## Reading the room before you act

The protocol is deliberately ordered: `register` → `check_in` →
`declare` / `claim` / `send`. The gate exists because an agent that
declares work without first looking at the board is the exact failure Dibs was
built to prevent: it will happily start the job somebody else announced ten
seconds earlier.

When `declare` returns matched agents, **read them before starting**. That
return value is the entire point of the system.
