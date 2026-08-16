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

`all` means anything unread wakes its recipient, once, when it arrives. A fleet
that waits for somebody to type before its members hear anything is not
independent, and a time-sensitive request sitting unseen because nobody was at
the keyboard is the failure Dibs exists to prevent.

`urgent` narrows it to work somebody is blocked on: questions, requests,
handoffs, unacknowledged announcements, changes to the agent's own standing.
Choose it if you would rather an FYI never cost a turn.

`none` never extends a turn. Dibs becomes strictly pull-shaped, with the
human's notification and the `waiting` line on every result as the only signals.

Each message wakes once either way, so an agent that read something and chose
not to act is not asked again. Work somebody is blocked on comes back on the
announcement retry. See [WAKE-MECHANISMS.md](../WAKE-MECHANISMS.md).

```toml
[wake]
extend_turn_for = "urgent"   # an FYI should never cost a turn on this machine
```

---

## `[limits]`: coordination timings

| Key | Default | What it decides |
|---|---|---|
| `agent_ttl` | `5m` | How long an agent **that registered a PID** may be silent before its lease lapses. Shorter is faster crash detection; longer suits agents that run long silent steps. |
| `idle_ttl` | `45m` | The same for agents with **no PID**, where silence is the only evidence. This governs the config `dibs mcp-config` prints, so an operator who tunes `agent_ttl` and sees nothing change is hitting this one. |
| `blob_store_bytes` | *(built-in)* | A hard cap on the attachment store. Over it, eviction drops referenced content rather than exceed the bound, so a recipient can hold a message naming a blob that is gone. |

```toml
[limits]
agent_ttl = "15m"    # these agents run long builds without saying anything
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

Applied on **every** start, not once. A role granted by hand disappears with the
ledger it lived in; a role declared here comes back with the daemon, which is
what somebody writing it down expects.

No agent can promote itself, and this file is not reachable through Dibs: it is
the operator's, read by the daemon as itself.

```toml
[roles]
coordinator = ["fleet-lead"]
```

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
