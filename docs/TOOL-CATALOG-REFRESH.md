# Tool catalogs in running chats

Measured 2026-10-30 for request 379595 after a running ChatGPT chat kept the
pre-recovery Dibs tool list despite making calls through an upgraded bridge.
The implementation already emitted `notifications/tools/list_changed` at
re-exec. Emission and host adoption are separate facts.

## Installed-client measurement

The finite synthetic fixture served `version_one`, changed its catalog, then
sent the standard notification. It logged every MCP request and checked setup
before drawing conclusions. No model turn, live-chat reload, configuration
write, or production service change was requested. All fixture state was private.

| Installed client | Notification result |
| --- | --- |
| Claude Code 2.1.310 | A second `tools/list` on the same server PID, followed by `mcp_status` showing `version_two`. |
| ChatGPT 26.1027.30824 bundled Codex 0.166.0-alpha.5.1, default features | No second list request; the same thread's catalog stayed `version_one`. |
| Same packaged Codex with only `mcp_tool_list_changed` enabled in the fixture | A second list request on the same server PID before the next status read; catalog became `version_two`. |

This measures installed **packaged client** behavior in an isolated app-server,
not a successful refresh in the already running app. The live implementation
chat's advertised inventory independently had 53 Dibs tools and no
`recover_agent`; no supported session-only refresh was exercised there.
The first Claude fixture was invalid: buffered stdin stranded a setup request.
It was corrected, and the valid run asserted the initial `tools/list` before
changing anything. Only that corrected run supports the result above.

Reproduce with `uv run tools/toolcatalogprobe.py codex-default`,
`uv run tools/toolcatalogprobe.py codex-enabled`, and
`uv run tools/toolcatalogprobe.py claude`. Each invocation prints its receipt
and private fixture paths, exits nonzero on invalid setup, and stops its own
client/process group at the end. These are opt-in measurements, not CI model
jobs or a product watcher.

Fresh fetched Codex `origin/HEAD` at
`67f0994b532b8740f14b5b738af7b7b2a915b1d6` has the same off-by-default feature.
`app-server/src/request_processors/mcp_processor.rs` accepts no selector for
`config/mcpServer/reload`; `app-server/src/mcp_refresh.rs` loops loaded threads.
The installed app archive also invokes that unscoped method. No supported
thread-and-server-scoped reload was identified. Dibs does not call the global
method, write client settings, restart chats, or claim that source capability
proves a live-app operation. A person may choose their client's own refresh or
a new chat. Bounded recovery also works with the tools the stale chat has.

## Evidence carried by the bridge

The historical handoff `tools_hash` identifies the binary's tool schema for
notification emission. It does not identify what the harness requested.
Separate additive `served_tools_hash` and `served_tools_revision` fields carry
the complete actual daemon reply successfully written to stdout. Errors,
partial pages, mismatched IDs and failed writes cannot advance that evidence.
An older image's binary hash never becomes served-list evidence by inference.

The existing metadata worker posts completion after a list is served, even if
the host makes no later call. Requests carry the same process-incarnation and
catalog observation; a delayed older revision cannot erase newer evidence.
The daemon accepts only local, same-host, non-invited metadata and keeps at most
512 observations. It grants no identity or session authority, consumes no mail,
and advances no coordination/contact clock. Doctor uses one process inventory
to exclude exited/reused PIDs; remote PIDs are never treated as local evidence.
At most eight authenticated agent labels identify each observed bridge.

The diagnostic means **served catalog unknown/different**, not a chronological
ordering of opaque hashes and not proof of the client's current internal list.
A matching served hash is also not an adoption receipt. Inventory is derived;
after daemon restart it is rebuilt by real bridge events, without a poller.
