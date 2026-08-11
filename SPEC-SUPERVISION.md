# SPEC-SUPERVISION: knowing whether a spawned agent is still working

Status: **built.** Process forensics, the harness hooks and their two tools,
attribution including the parent-side stamp, and the sweep that joins them all
ship, and the chain is proven end to end against a real stalled process (§6.2).

Every capability claim below was verified against the harnesses' own source or
against a live process on this machine, and the places where something is
assumed rather than measured say so. Two claims made here earlier turned out to
be false and are corrected in place rather than deleted, because the way each
was reached is the more useful record: a mechanism tested against the one case
where it cannot work, and a test whose subject had already exited.

Target platform is macOS, which is what this is tested and shipped against. The
Linux paths (`/proc/<pid>/environ`, `/proc/<pid>/fd`) are written and never run, out of scope rather than outstanding, and recorded here so nobody reads their
presence as a claim of support.

## 1. The failure

A parent agent spawns an out-of-process subagent, `codex exec`, another
`claude`, an opencode run, and gets exactly one signal back, at the end: an
exit code. Everything before it is silence, and silence has at least four
causes that look identical from outside:

1. the child is mid-turn and will answer in ninety seconds
2. the child is blocked on a permission prompt nobody will answer
3. the child hung on a socket that will never reply
4. the machine went to sleep and nothing was running at all

Measured on this machine, two `codex exec` processes running side by side:

```
  PID 16414   alive 22m      CPU 19.6s    1.5%     busy: healthy, producing
  PID 48620   alive 7h39m    CPU  0.11s   0.0004%  busy: stalled from the start
```

The parent of 48620 was a blocking Bash tool call in a Claude Code session. It
had been waiting seven hours and thirty-nine minutes for a process that never
did anything, and had no way to find out.

## 2. Why the obvious mechanisms do not work

**Process ancestry fails.** PID 16414 had **PPID 1**: it was spawned detached,
so the kernel reparented it to launchd the moment its spawner returned.
Detaching is exactly what a harness does when it wants a child to outlive a
tool call, so the spawning pattern that creates the problem is the one that
destroys the ancestry link.

**Kernel exec events are not available unprivileged.** macOS EndpointSecurity
(`ES_EVENT_TYPE_NOTIFY_EXEC`) requires a signed entitlement from Apple *and*
root. Linux's netlink proc connector needs `CAP_NET_ADMIN`; eBPF needs
comparable privilege. None belongs in a local-first tool a person installs
without sudo.

**A hook cannot report a hang.** This is the load-bearing constraint of the
whole design. Hooks are code the harness runs; a harness that is wedged runs
nothing. Every state a hook *can* report is a state the child was healthy
enough to report. The interesting failure is precisely the one where no hook
fires.

So neither mechanism is sufficient alone, and the design is both.

## 3. Layer one: process forensics (BUILT)

`internal/liveness`. Answers "is it working, thinking, stuck or gone" from
outside, with no cooperation from the child.

Progress is read from the agent's own cumulative token count where the
transcript reports one (codex `token_count` events; Claude Code per-message
`usage`), because a transcript can grow for reasons the model had nothing to do
with. File size is the fallback, never the default.

The distinction that makes it usable is CPU. Measured at 15-second intervals on
a healthy agent:

```
  12:07:00  tokens=2514737  cpu=8.30s   both advanced
  12:07:15  tokens=2514737  cpu=8.36s   NEITHER advanced: and it was healthy
  12:07:30  tokens=2684439  cpu=8.50s   both advanced
```

Flat output for a whole window on a perfectly healthy agent. Any detector that
calls that "stuck" fires on every turn boundary, and a detector that cries wolf
gets switched off. CPU advanced even then, because streaming a response costs
cycles to parse, so output-flat-but-CPU-burning is *thinking*, and only
output-flat-and-CPU-flat past a grace period is *stuck*.

**Sleep is not silence.** Elapsed time is measured monotonically, so time the
machine spent suspended is excluded and reported separately. On this machine
8.45 of the last 80.3 hours since boot were sleep; a wall-clock detector would
have reported every agent alive during them as hung.

**Lifetime idleness convicts from a single sample.** A process alive for hours
on near-zero CPU has done nothing since it started, and that needs no history.
This is what identifies 48620 in four seconds rather than five minutes.

## 4. Layer two: harness hooks (BUILT)

What forensics cannot know: *why*. A blocked-on-permission child and a
hung-on-socket child are both "stuck" from outside and need opposite responses.
The harness knows the difference and will say so.

**Codex exposes eleven hook events** (`codex-rs/hooks/src/lib.rs`):

```
  PreToolUse  PermissionRequest  PostToolUse  PreCompact  PostCompact
  SessionStart  SessionEnd  UserPromptSubmit  SubagentStart  SubagentStop  Stop
```

and loads them from `<plugin>/hooks/hooks.json`: the same layout and shape as
Claude Code, deliberately: a Codex feature flag describes them as "Claude-style
lifecycle hooks loaded from hooks.json files". **One plugin shape serves both
harnesses.**

`SessionStart` carries `session_id`, `transcript_path`, `cwd`, `model`,
`permission_mode`, `source`. That is the whole of what layer one was
*inferring*, handed over directly: most importantly `transcript_path`, which
removes transcript discovery and its guesswork entirely.

`PermissionRequest` carries `agent_id`, `agent_type`, `tool_name`,
`tool_input`, `turn_id` alongside the session fields. This converts the second
cause of silence from an invisible hang into a named, actionable state: *this
child is waiting for a human, and here is what it wants to do.*

`SubagentStart` / `SubagentStop` mean codex models nested agents natively, so a
tree deeper than one level needs no invention.

## 5. Layer three: attribution (PARTLY BUILT)

Joining a child to the parent that spawned it, without either being asked to
register.

The environment is the channel: inherited at fork, and it survives reparenting,
daemonisation and process-group changes. A ladder, most trustworthy first, with
every answer recording which rung produced it: a wrong owner is worse than
none, because it sends a stall report to an agent that cannot act while the one
that can hears nothing.

| rung | source | status |
|---|---|---|
| 1 | `LANES_PARENT` in the environment | built, and now SET by the PreToolUse stamp (§6.1) |
| 2 | harness `session_id`, exported or read off the session directory | built |
| 3 | PPID, while the child is still a descendant | built |
| 4 | nothing, said plainly | built |

Validated live: three codex processes attributed to the session that owns them,
including one that was a *grandchild*, so inheritance carries through the whole
descendant tree rather than one level.

**Environment visibility on macOS has one exception, and it is precise.** The
environment of an Apple-signed PLATFORM binary is hidden. `/bin/sleep`,
`/bin/bash`, and anything they exec. The environment of a user-installed binary
is readable. Every agent harness is the second kind, so rung 1 works for the
processes it exists for; a harness launched through a shell also works, since
the shell hides its own environment while the agent it execs shows the variable
it inherited. Asserted unconditionally against a user-compiled binary.

Two wrong turns are worth recording, because both produced confident-looking
evidence. The mechanism was first tested against `/bin/sleep` (the one case
where it cannot work) and the failure was misdiagnosed as `-o command=`
discarding the environment. It does not: with the `e` flag, `command` includes
it. The second attempt spawned a real user binary but pointed it at a test that
finished in milliseconds, so the read succeeded and the attribution that
followed sampled a process that had already exited, which reads exactly like a
broken regex.

Rung 2 also reads a Claude Desktop session directory off `PATH`, which is
incidental rather than a documented interface. It is one rung of a ladder for
exactly that reason: if the layout changes it yields nothing and the next rung
runs, rather than yielding something wrong.

## 6. The two ends

**6.1 The parent-side stamp.** A `PreToolUse` hook can rewrite the
tool's input before it runs, via `hookSpecificOutput.updatedInput`. Verified in
both harnesses: it is in Codex's generated
`pre-tool-use.command.output.schema.json` and in the shipped Claude Code bundle.

So `lanes hook-spawn` stamps the child at the only moment the relationship is a
FACT rather than a reconstruction (the instant it is spawned) by prefixing
one assignment onto the command. The OS does the rest.

It runs in front of every shell command an agent issues, so its first duty is
to be harmless. It rewrites only when the command actually spawns an agent (the
same executable-basename test the sweep uses, so the two cannot disagree about
what an agent is), the session maps to a lane, the command is not already
stamped, and a leading assignment would not change its meaning: subshells,
groups, leading redirects, expansions and multi-line scripts are all refused.
An agent whose command is mangled by an invisible hook cannot diagnose it.

Only the FIRST command in a line counts: `cd /x && codex exec …` is refused,
because the assignment would bind to `cd` and never reach the agent: a stamp
that silently does nothing is worse than none, since the fallback rungs would
have caught it.

This also required `hook_poll` to name the lane when it has no news. It
returned a bare `{}`, which made "this session has no lane" and "this lane has
no mail" indistinguishable, so the stamp silently never applied: the hook was
correct on every negative case and did nothing on the only positive one. The
digest, which is what a harness injects into a model's context, is unchanged.

The codex plugin ships at `plugins/codex/hooks/hooks.json`, and the two tools
it calls (`hook_session` and `hook_blocked`) are served. It was briefly held
back because those tools did not exist: a hook wired to a nonexistent tool
fails silently at runtime, which is indistinguishable from never having written
it. The test that enforces this now walks every `plugins/*/hooks/hooks.json`
rather than the one hardcoded
path it read before, so the second plugin cannot ship ahead of its tools.

The events it needs are settled:

| event | carries | why Lanes wants it |
|---|---|---|
| `SessionStart` | `session_id`, `transcript_path`, `cwd`, `model` | identity and the exact progress signal, handed over instead of inferred |
| `PermissionRequest` | `agent_id`, `tool_name`, `tool_input`, `turn_id` | turns an invisible hang into "waiting for a human, and here is what it wants to do" |
| `SubagentStart` / `SubagentStop` | `agent_id`, `agent_type` | nested agents, which codex models natively |
| `Stop` / `SessionEnd` | session fields | a finished child distinguished from a dead one |

**6.2 The supervision loop, proven end to end.** A test spawns a
real stalled agent, a user-compiled binary named `codex`, stamped with
`LANES_PARENT`, blocked and burning nothing, sweeps the machine, and asserts
the lane that spawned it was told, once. Each link is fault-injected: removing
the environment read makes attribution fall through to the session-path rung
(the silent degradation this design exists to expose), removing the report
produces silence, and removing the once-only guard produces a duplicate.

Two things that had to be got right for it to mean anything. The stand-in
cannot be `select {}`: Go's runtime calls that a deadlock and kills the
process, which then lingers as an unreaped zombie that still has an elapsed
time, so a naive wait-for-age passes while discovery has quietly stopped
matching. And it cannot be built from system tools: macOS hides the environment
of Apple-signed binaries, so `/bin/sleep` and anything `/bin/bash` runs are
precisely the processes this cannot read.

Verified once more against the shipped daemon rather than in-process: with
`[supervise] every/quiet/frozen` tightened in `lanes.toml`, a lane registered,
and a stamped stand-in agent spawned, `ack_board` returned the notice without
the agent asking for anything,

```
A codex subagent you spawned (pid 78591) has stopped working: no output and no
CPU for 6s: alive, but not doing anything. Lanes has not touched it,
restarting or abandoning it is your call, and `lanes probe --pid 78591` will
show its current state.
```

The thresholds are a `[supervise]` table because there are no universally
correct values and the two errors are not symmetric: a false "stuck" invites a
parent to kill healthy work, while a slow true one costs a few minutes. `off`
disables the sweep without disabling `lanes probe`.

The in-process test compresses the timescale and scales the duty-cycle
threshold with it.
Any process burns some CPU starting up, and over a two-second life that fixed
cost is ~0.5% of it, against the 0.05% that means "did nothing for seven
hours". Same judgement, same shape, different window.

 `lanesd` runs `Discover()` every 20
seconds in its own goroutine, samples and classifies each agent process, and
tells the owning lane when one becomes stuck. Nothing is needed on the agent
side.

Its own goroutine, not the writer loop: a scan forks `ps`, and putting that on
the single writer would stall every agent's coordination behind a process scan.
Sampling and classification happen outside; only the verdict crosses back in.

Reported through the NOTICE path rather than mail. A notice is precisely
"something happened to you that you could not have inferred", it is delivered
on the agent's next `ack_board` or `hook_poll` without it having to ask, and it
carries no ledger op, which is right, because a stall is an observation about
this machine now, not a coordination fact that must survive replay.

Said once per stall, not every 20 seconds, and re-armed if the child recovers
and stalls again: the second stall is news. A child nobody can be shown to own
is reported to NOBODY, which is the deliberate choice: guessing would send the
report to an agent that cannot act while the one that can hears nothing. It
stays visible to a human through `lanes probe`.

**6.3 Resumption: offered, never taken.** A stall report now carries the exact
command that would pick the child up where it stopped:

```
A codex subagent you spawned (pid 12407) has stopped working: … Lanes has not
touched it: restarting or abandoning it is your call … To pick it up where it
stopped rather than starting over: `codex exec resume 019ea7c2-…`.
```

Lanes does not run it, and that is not squeamishness: the parent knows what the
child was for and whether re-running it is safe, and a supervisor that silently
repairs things teaches its operator nothing while hiding a failure that may be
systematic. But WITHHOLDING the command is a different thing from declining to
run it: a parent told "your subagent is stuck" and left to work out the
incantation has been handed a problem instead of a decision.

Only codex exposes one today, and it costs nothing to derive: the session id is
in the transcript filename Lanes already holds, so nothing new is stored and
nothing is asked of the child. A harness without a resume, or a child with no
transcript, is offered nothing rather than a guess.

## 7. Harness coverage

Lanes ships integrations for eight surfaces. Four of them run agents that spawn
other agents, and those are the ones this section is about.

| harness | stamp mechanism | stamp | announces itself | transcript readable |
|---|---|---|---|---|
| Claude Code | `PreToolUse` → `updatedInput` | **built** | yes | yes |
| Codex | `PreToolUse` → `updatedInput` | **built** | yes | yes |
| opencode | `shell.env`, the env map, directly | **built** | **yes** | **yes** (reports its own) |
| pi | `tool_call` → mutate `event.input` | **built** | no | **yes** |

Every one of them can be found as a process regardless, because forensics needs
no cooperation at all.

Three of the four rewrite a command string, because their shell tool has no
environment argument: codex declares `command`, `login`, `shell_command`,
`timeout_ms`, `workdir` (`core/src/tools/handlers/shell_spec.rs`); Claude Code's
Bash tool takes `command`, `description`, `timeout`, `run_in_background`; pi's
takes `command` and `timeout`. A hook can only rewrite fields that exist.

opencode is the exception and the one to copy: `shell.env` hands over the
environment map, so nothing is parsed, nothing can be misparsed, and none of
the shapes the other three must refuse are hazards there.

The rewriting rules are duplicated in Go (`cmd/lanes/hookspawn.go`) and
TypeScript (`plugins/pi/lanes.ts`) because the harnesses are in different
languages. That duplication is a real risk (two copies of a security-adjacent
predicate drift) so the TypeScript is checked against the same twenty cases
the Go tests use.

**pi is now covered as a child.** It writes append-only JSONL to
`~/.pi/agent/sessions/<project>/<stamp>_<id>.jsonl` with
`message.usage.totalTokens` per assistant message, which is the same shape
Claude Code uses. Its field names do not overlap with Claude Code's, so one
parser reads both without either harness contaminating the other's count.
Verified against a real transcript on this machine: 17,463 tokens.

**opencode is covered too, by reporting rather than by being read.** It keeps
sessions in SQLite, where byte growth measures WAL churn, and its only
append-only file is a single `opencode.log` SHARED by every run on the machine,
watching that would make every opencode agent look busy whenever any one of them
was, which is a false signal and worse than none.

So the child counts for itself. `hook_session` takes an optional `progress`: a
monotonic counter in whatever unit the harness likes, whose MOVEMENT is the only
thing used. The opencode plugin increments it on every `chat.message` and reports
fire-and-forget: a supervision signal that can delay a turn is worse than a
missing one. The sweep uses it only when a transcript gave nothing, so a harness
with both keeps the stronger signal.

Two properties this needed, both now enforced:

- **Monotonic.** An event that omits the counter must not reset it (most
  lifecycle events carry none, and reading silence as zero would make a working
  child look frozen at every turn boundary), and one that goes BACKWARDS is a
  restarted process reusing a session rather than work that un-happened.
- **Demonstrated progress outranks a lifetime average.** The duty-cycle rung
  convicts from a single sample, so it used to fire even on a child that was
  visibly working: a process that idled for hours and has NOW started is
  working, and convicting it on the strength of the hours is exactly wrong. That
  check now runs after the progress comparison. It was a real bug, not a
  test artifact.

**On mechanism choice.** Rewriting a command string is a workaround: it forces
`safeToPrefix` to refuse subshells, leading redirects, multi-line scripts and
`cd /x && codex exec …`, and every one of those refusals is a case where
attribution silently drops to a weaker rung. An environment-native path has no
such cases, because nothing is being parsed.

- **opencode** exposes exactly that, and the plugin is built
  (`plugins/opencode/lanes.ts`). The stamp is exact and no command is touched.
- **codex** does NOT expose one. Its runtime carries
  `env: HashMap<String, String>` on `ExecParams`, but that is populated from
  session config, not from tool arguments: the model-facing shell tool declares
  only `command`, `login`, `shell_command`, `timeout_ms` and `workdir`
  (`core/src/tools/handlers/shell_spec.rs`). A hook's `updatedInput` can only
  rewrite fields that exist, so there is nothing to set.
- **Claude Code** likewise. Its Bash tool takes `command`, `description`,
  `timeout` and `run_in_background`.

So for both shipped harnesses, command rewriting IS the most native mechanism
available, and `safeToPrefix` is a necessity rather than a workaround. Its
refusals remain the honest cost: a `cd /x && codex exec …` is attributed by a
weaker rung, and nothing can be done about that short of parsing shell, which
would be a far worse trade.

opencode's is the cleanest of the four and would be the easiest to add:
`"shell.env"(input: {cwd, sessionID}, output: {env})` injects environment
variables into shell commands directly, so the stamp needs no command rewriting
and none of the `safeToPrefix` care that rewriting demands: subshells, leading
redirects and multi-line scripts stop being hazards because nothing is being
parsed. `tool.execute.before` also exposes a mutable `output.args`, which is the
updatedInput equivalent. pi routes `beforeToolCall` through an extension runner
with `tool_call` handlers.

The two axes are independent, which is the point of the layering: the stamp is
a PARENT-side capability and detection is a CHILD-side one, so a Codex parent
supervising an opencode child still attributes it (the stamp is just an
environment variable, and opencode inherits it like any process), while an
opencode parent spawning a Codex child falls back to the inference rungs.

All four are now covered as children as well, by one of two routes. A harness
that writes an append-only transcript is READ: codex, Claude Code and pi, each
with a token count in it. One that does not is asked to REPORT: `hook_session`
takes a monotonic `progress` counter, which is how opencode is covered without
Lanes parsing a store it does not own.

Adding a fifth harness therefore means either a glob in `transcriptGlobs` plus a
parser in `Tokens`, or a plugin that counts its own turns, whichever its
storage makes honest. As a PARENT it means one plugin using the stamp mechanism
named above.

Process forensics underneath needs no cooperation at all, so even a harness with
neither is still found, still convicted by duty cycle and still reported. What
that loses is resolution, not detection: "did nothing at all" is caught and
"produced nothing for six minutes while burning CPU" is not.

**Both harnesses honour `updatedInput`, and this was earned twice.** Codex
documents it in its own generated `pre-tool-use.command.output.schema.json`.
For Claude Code I first claimed it from a grep of the shipped bundle, which was
not proof: the bundle is not reliably greppable, and `session_id`, which the
shipped hooks demonstrably substitute, does not appear in it either. So it was
tested instead: an isolated headless session, a scratch `--settings` file, and
a trivial hook that rewrites any Bash command to `echo REWRITE_TOOK_EFFECT`.
Asked to run `echo ORIGINAL_COMMAND_RAN`, the session printed
`REWRITE_TOOK_EFFECT`. The mechanism is real.

**That experiment also found a shipped bug that broke the session outright.**
Claude Code treats a non-zero `PreToolUse` hook as a REJECTION, so the tool call
is blocked. The plugin called `lanes hook-spawn` bare, against an installed
`lanes` binary predating that subcommand: exit 2, and its usage text printed to
stdout, where hook output is parsed. Every Bash invocation in that session was
refused. Version skew between a plugin and the binary it calls is the normal
state of a half-upgraded install, so the hook now runs the command, emits its
output only if it succeeded AND looks like hook JSON, and exits 0 regardless,
degrading to no stamping rather than to an agent that cannot run anything.
`TestCommandHooksCannotBreakTheToolTheyDecorate` fails any shipped command hook
that can do otherwise.

## 8. Design rules

1. **A hook cannot report a hang.** Forensics is not a fallback for hooks; it
   is the only layer that covers the interesting case. Neither is optional.
2. **A wrong owner is worse than no owner.** Every attribution records its
   rung, and an uncertain rung yields nothing rather than a guess.
3. **A false "stuck" is more expensive than a slow true one.** Thresholds sit
   far from measured healthy behaviour, and the measurements are recorded here
   so a future reader can re-derive them rather than trust them.
4. **Lanes reports; the parent acts.** Including when Lanes could act itself.
