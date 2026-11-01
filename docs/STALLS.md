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
dibs probe --pid 48620
pid 48620: stuck: alive 7h40m and has used 100ms of CPU in all of it
(0.0004% busy): it has done nothing since it started
```

Or be told. `dibd` sweeps every 20 seconds and sends the agent that spawned a
subagent a notice when it stalls, delivered on that agent's next `check_in`
without it having to ask. Attribution happens at spawn time, where the harness
allows it: in Claude Code a `PreToolUse` hook stamps the command with its
parent's agent, and the OS carries that into every descendant, through
detaching, daemonisation and reparenting, which is where process ancestry
gives up. Codex has no hook Dibs can use without spawning a subprocess, which
it will not do, so there a child should call `vouch_child` and register with
the nonce instead.

**It reports and never acts.** `codex exec resume` exists and Dibs will not call
it: the parent knows what the child was for and whether re-running it is safe. A
supervisor that silently repairs things teaches its operator nothing and hides a
failure that may be systematic. Dibs hands back the command; running it is your
call.

**Sleep is not silence.** Elapsed time is measured on a monotonic clock, so a
closed lid does not read as a stalled fleet: on the development machine, 8.45 of
the last 80.3 hours since boot were sleep. You are told "silent for 3 awake
minutes; the machine also slept 38" rather than "silent for 41".

Design and measurements: [SPEC-SUPERVISION.md](../SPEC-SUPERVISION.md).
