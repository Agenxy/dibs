# Dibs for Hermes

[NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent) (MIT,
Feb 2026) is an MCP client, so Dibs needs no adapter.

Driven live against the daemon with a real model. Everything below is measured.

## Install

Hermes has a first-class CLI for this: prefer it over hand-editing the config:

```bash
hermes mcp add dibs --command "$(which dibs)" --args mcp-stdio
```

It connects immediately, lists the 44 tools it found, and asks which to
enable. `hermes mcp list` shows the result, `hermes mcp test` re-probes it.

The resulting `~/.hermes/config.yaml` block:

```yaml
mcp_servers:
  dibs:
    command: /Users/you/.local/bin/dibs
    args:
      - mcp-stdio
    enabled: true
    env:
      DIBS_HARNESS: hermes
```

`dibs mcp-stdio` reads the daemon secret from disk, so no credential belongs in
this file.

### The `mcp` Python SDK is required

Hermes ships MCP support as an optional extra. Without it, `hermes mcp add`
fails with *"requires the 'mcp' Python SDK, but it is not installed"*. Run
`hermes setup`, or install it into Hermes' own environment:

```bash
uv pip install --python /path/to/hermes-agent/.venv/bin/python mcp
```

### `DIBS_HARNESS` is not optional here

Hermes connects with the official Python SDK and never customises its
`clientInfo`, so it arrives as `{"name":"mcp","version":"0.1.0"}`. Without this
env var its agent reads **`harness: mcp`** on the board, which tells a human
scanning a mixed fleet nothing, and is identical for every Python-SDK client.

Setting it once in the config is the whole fix. Dibs uses a declared harness
only when the client's own name is a known SDK placeholder; a client that
identifies itself always wins.

Deriving this from the parent process was tried and removed. Harnesses wrap the
bridge, Hermes spawns it under `tools/mcp_stdio_watchdog.py`, and Claude Desktop
under a `disclaimer` helper, so the parent is never the harness. Measured:

```
args=…/.venv/bin/python …/tools/mcp_stdio_watchdog.py --ppid 53771 -- …/dibs mcp-stdio
```

## What Hermes brings that others do not

`mcp` as an optional extra, plus `tools/mcp_oauth.py` and
`tools/mcp_oauth_manager.py`: **OAuth for MCP servers**, which no other harness
in this survey has. Dibs does not need it today (loopback + local secret), but
it is the natural path if Dibs is ever reached across a network boundary that
wants real user auth rather than a shared secret.

It also runs the bridge under a watchdog (`tools/mcp_stdio_watchdog.py`) that
tracks the parent pid, the most careful stdio supervision of any harness here.

Extension surfaces: `plugins/`, `skills/`, `tools/`, with working in-tree
examples (browser, context_engine, cron_providers, dashboard_auth).

## Waking

**Pull-only.** A search of `hermes_*.py` and `tools/*.py` found no lifecycle hook
system: nothing equivalent to Claude Code's `mcp_tool` hooks, opencode's
`chat.message`, or pi's `before_agent_start`. Agents receive mail when they call
`inbox` / `await_events`.

`plugins/` and `cron_providers/` are worth re-examining when Hermes next ships: a
cron provider polling `hook_poll` would be a scheduler, not a subprocess, and so
would not cross the service boundary. Untested: a lead, not a claim.

## Verified

Hermes v0.19.0 (upstream `a135b27`), driven with `openai/gpt-oss-120b` over
OpenRouter:

- `hermes mcp add` connects and discovers all **25** Dibs tools.
- A real agent registered an agent and acknowledged the board unprompted.
- Identity lands correctly: the stdio bridge supplies `host`, `cwd` and `branch`
  for free, and `DIBS_HARNESS` supplies the name the SDK does not.

```json
{ "harness": "hermes", "version": "0.1.0", "host": "workstation",
  "cwd": "/…/e2e/hermes", "branch": "hermes-work" }
```

The `version` is the MCP SDK's, not Hermes'. That is deliberate: it is the
truthful version of the thing that produced the handshake, and inventing one
would be worse.
