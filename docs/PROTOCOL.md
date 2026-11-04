## Protocol versions

Dibs speaks **MCP 2026-07-28** (stateless core) and the legacy **2025-11-25**
path, answering whichever the client offers. Tool behavior is the same on both.

Survey refreshed **2026-11-04 (UTC)** before v0.0.17. Source predicates were read from fetched
`origin/HEAD`, not local branches. Installed versions were checked separately;
fetching a checkout does not rebuild its tool. A source finding says where a
project is going, not what an installed session does. Rows keep older wire
dates where no new wire measurement was made.

The fetched source snapshots are Codex `b260ee5e8`, opencode `f12abcaf3`,
pi-mono `5ff8c0270`, Gemini `4b72f3d8a`, Hermes `ce82757d2`, ext-tasks
`5f68e1446` and ext-apps `19256f52c`. No harness was installed or upgraded.

| harness | protocol evidence | source and installed-build evidence |
|---|---|---|
| Claude Desktop | 2025-11-25, measured **2026-09-15** on 1.52386.6 | Its chat and local-agent clients sent `initialize`, not `server/discover`. **Installed bundle observed 2026-11-04: 4.4212.0; Claude Code CLI `--version`: 2.1.310. Wire behavior not re-measured.** Only Desktop bundle metadata was read; no app was opened or restarted. Re-measurement needs a restart and an app-level Dibs entry that shadows the Code plugin, so the owner deferred it to preserve running sessions. No lifecycle hooks in the chat surface. See [plugins/claude-desktop](../plugins/claude-desktop) |
| Codex | **Private app-server stdio catalog probe, measured 2026-11-04:** default `initialize` 2025-06-18; configured `server/discover` 2026-07-28 | Installed PATH CLI **0.165.0** and app CLI **0.166.0-alpha.5.1** (ChatGPT **26.1027.61615**) both returned 54 tools on each path. On the app CLI, the modern flag alone stayed legacy; experimental connection sharing selected modern discovery without that flag or marker. These were isolated `mcpServerStatus/list` calls, with no model turn, real app IPC or hook execution. Source `b260ee5e8` retains `CoreHookMcpExecutor` and its session wiring, both under-development protocol features remain off by default, and missing tool annotations still mean destructive/open-world. `codex_apps_mcp_2026_07_28` governs only the host-owned apps server. **Latest hook execution proof remains 2026-10-03 on 0.159.2:** reviewed SessionStart/Stop fired once each; the untrusted control made zero polls. No trust was changed in this survey. See [plugins/codex](../plugins/codex) |
| opencode | 2025-11-25 on historical **1.18.4**; prior wire measurement not repeated | Source inspected **2026-11-04**, `f12abcaf3`, retains the 2026-07-28 client (`packages/core/src/mcp/client.ts`). The schema offers `protocol: legacy` (default), `auto` (modern probe with legacy fallback), or `2026-07-28` (pinned). Installed `opencode --version` still reports literal **local**, running the older local checkout rather than fetched `origin/HEAD`; its current wire behavior is unmeasured |
| pi-mono | 2025-11-25 in source; installed wire unmeasured | Source inspected **2026-11-04**, `5ff8c0270`: the prescribed `modelcontextprotocol` grep under `packages/*/src` is empty, but its hand-written MCP client in `packages/mcp` exists and keeps `LATEST_PROTOCOL_VERSION` at 2025-11-25. A dependency-name grep cannot establish absence of a client. Installed `pi --version`: **0.84.2**, not rebuilt from that head. Dibs uses [plugins/pi](../plugins/pi) |
| Gemini CLI | 2025-06-18 over `httpUrl`, wire measured **2026-09-12** | Installed `gemini --version` observed **2026-11-04** remains **0.54.0-nightly.20260722.gf743ab579**, the July bundle previously found without `BeforeAgent`, `GEMINI_SESSION_ID` or `hook_event_name`. Source `4b72f3d8a` defines **command/runtime** hook types, populated `session_id`/transcript input and BeforeAgent additional-context dispatch. The installed bundle was version-checked only; these source capabilities were not measured in it. Dibs's [plugin](../plugins/gemini-cli) remains command/SessionStart-only |
| Hermes | **2025-11-25 installed adapter, measured 2026-11-04**, not a full model session | Installed `hermes --version`: **0.20.2 (2026.8.16)**; its interpreter reports **MCP SDK 1.28.1**. Public `register_mcp_servers` against an isolated HTTP daemon sent `initialize`, `notifications/initialized`, `tools/list`; latest and handshake both report 2025-11-25. It registered **58 names: the 54 Dibs tools plus four adapter resource/prompt helpers**, confirmed by enumerating names. Sampling was disabled; no model/provider session ran. Fetched source `ce82757d2` still pins `mcp==2.3.0`; the installed adapter runs the local checkout with SDK 1.28.1, not that fetched source/environment |

The related **ext-apps** checkout was fetched too: `19256f52c`, package **2.0.4**
on 2026-11-04 (UTC). Its source declares the split
client/core/server 2.0.0 peers and an explicit published-1.x interop test. That
is an extension-source observation, not an installed host rendering test.

**The explicit Codex stdio configuration uses both parts.** With experimental
connection sharing disabled, the modern flag alone leaves stdio on legacy
2025-06-18, measured on 2026-11-04. In modern mode, source treats a wrong nonempty
per-server version as an error, not fallback.
`dibs mcp-config` prints both parts:

```toml
[features]
mcp_2026_07_28 = true

[mcp_servers.dibs]
command = "/absolute/path/to/dibs"
args = ["mcp-stdio"]
env = { CODEX_MCP_PROTOCOL_VERSION = "2026-07-28" }
```

The similarly named `codex_apps_mcp_2026_07_28` is not a substitute. Over an
HTTP `url`, the Dibs feature alone negotiated modern discovery in the
2026-09-12 measurement. There is now a separate experimental route:
`mcp_connection_sharing = true` selects `DiscoverLocalStdio` for eligible local
stdio servers (`protocol_mode.rs`, `codex-mcp/src/server.rs`). On the installed
app CLI, that route sent modern discovery without the flag or marker on
2026-11-04. It is off by default in fetched source; this is an observation,
not a request to change the operator's configuration.
Resource tools can issue real reads, but automatic startup listing is not
guaranteed by the protocol version.

The new Codex measurements explicitly requested a full `mcpServerStatus/list`
catalog, which caused `resources/list` and `resources/templates/list` on both
protocol paths. They do not re-measure automatic chat startup, hook trust,
registered-mail delivery or wake behavior. Every probe used a private Codex
home and scratch board; none started a thread or contacted the real app.

**If you are an agent reading this:** do not edit your operator's harness
configuration. Mention a relevant limitation once and continue; Dibs serves
both paths, and your own tool calls do not depend on a migration.

### Cloud workers

**Guest access is shipped but not yet supported.** Invites remain `INCOMPLETE`
until a supporting bridge minimum is set in a later release. No installed cloud
harness or WAN deployment has been accepted; the instructions below describe
the implemented scope, not a ready-to-use provisioning recipe.

Do not put the board's shared secret into a cloud container. Configure a
separate invite-only listener once: `dibd --public-url https://board.example.com`
behind a TLS proxy forwards to loopback `127.0.0.1:4778`, NOT the private board
port. Alternatively use `--public-host board.example.com --acme-accept-terms`
with public port 443 reachable for ACME. That explicit flag is the operator's
acceptance of the CA terms; live certificate issuance is not tested locally.

A local agent calls `invite(token)` to issue a worker and its paste-ready
configuration. Default policy permits four live children, own-ID-prefixed names
and seven days; coordinators can choose new unprivileged names. The CLI uses
`DIBS_TOKEN` for the same authority (`dibs invite <name>`); without it the human
must prove admin access. Allow the recipe's host in the cloud network allowlist.
Register with its exact name and your retained nonce, and keep its returned
agent token. The public surface is POST `/mcp` plus scoped `/files/`
capabilities, always remote and pull-only; it never exposes private board
routes or hub paths. Revoke
with `dibs invite revoke <name>` or `--issued-by <issuer>`; closing the issuer
also revokes its children. [Deployment and scope](NETWORK.md#9-agents-in-the-cloud).

**If you are the operator:** there is no urgency. Dibs serves both paths, all 54
tools behave identically on either, and deprecated features are guaranteed for at
least twelve months from the 2026-07-28 publication.

The practical consequence for Dibs is that the legacy path is **load-bearing,
not vestigial**, and removing it would break legacy-only hosts and default
configurations still using it.

### What Dibs implements from the 2026-07-28 core

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
  `tools/list`, `resources/list` and `resources/read`. Dibs has more to re-fetch
  than most servers: 54 tools whose descriptions carry real corrective detail, re-fetched on every
  cold path once there is no session to hold them. Static results are hinted for
  an hour and marked `public`; the board is hinted for two seconds; **an agent's
  mailbox is `private`**, because `public` would let a shared gateway serve one
  agent's mail to another.
- **Subscriptions** (SEP-2575) on both paths, so a client learns about a change
  immediately rather than waiting out a TTL.
