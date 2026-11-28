# Antigravity desktop wake measurements

Measured on macOS 2026-11-21 with Antigravity.app 2.34.0 and language server
2.34.0. The test target was an existing disposable conversation in the desktop
app. The test created that conversation separately from the wake.

Antigravity supports native plugin lifecycle hooks. Dibs currently uses the
verified desktop send-message route for delivery. On 2026-11-22, `agy` 1.5.14
installed the native MCP plugin in an isolated home and preserved its existing
permission grants. An isolated runtime probe with `agy` 1.1.10 reached the
account prompt, leaving lifecycle output and permission matching unmeasured.

## Native delivery

The app-owned `language_server agentapi send-message` command accepted a
factual Dibs notice for the scratch conversation over the live descriptor's
loopback HTTP port. The reply echoed the exact recipient ID and notice.
The notice then appeared in the same conversation's transcript and a model
step followed in 2.09 seconds. The frontmost app was unchanged. The transcript
check and receipt are separate evidence. An accepted API call alone establishes
input acceptance.

The test passed the hub credential in the child environment. The command
arguments carried a title, the target conversation ID and a factual notice.
No mail body was used. `get-conversation-metadata` returned data for the same
ID on the same live hub. A future wake sends only to an existing conversation
and treats a timeout or ambiguous reply as unknown.

## Command approval contacts

On 2026-11-27, Antigravity 2.35.2's `agentapi get-conversation-metadata`
returned workspace metadata for two conversations awaiting command approval.
Its reply contained no step state, and the installed `agentapi` help listed
no trajectory command. The two conversation databases each had a
`run_command` step in `WAITING` (`step_type=132`, `status=9`). One had a newer
pending command after the waiting step.

When a later wake finds previously accepted mail still unread, Dibs can
read the step type and status for the bound conversation with system SQLite.
It uses a bounded `mode=ro&immutable=1` query and checks for an active journal
or a changed database. A pending or queued suffix leaves the last waiting
step actionable. A more recent running or completed step, a missing or
unreadable database, or an ambiguous snapshot retains the general contact
wording. The observed cause is recorded as `antigravity_command_approval`
in the contact escalation and in `diagnose_wake.contact_escalation.reason`.
Approval and rejection stay in the Antigravity conversation.

## Binding evidence

The app did not discover a temporary MCP server after a configuration edit,
including when a scratch conversation was created. Both probes restored the
original MCP configuration. Those trials leave per-call MCP metadata
unmeasured. The app exposes its server controls under Customizations.
[Antigravity MCP documentation](https://antigravity.google/docs/mcp?app=antigravity).

The scratch conversation's terminal tool inherited `ANTIGRAVITY_AGENT=1` and
the exact `ANTIGRAVITY_CONVERSATION_ID`. A same-host observer saw its live
process descend directly from the active bundled language server, whose parent
was Antigravity.app. A first probe tried to run `ps` inside the terminal
environment and the harness refused it. The successful probe recorded the
terminal's own identifiers and an outside observer checked that specific
process while it was alive.

The observed ancestry and environment are operational evidence. The shared
language server can launch terminals for multiple conversations, and a caller
can supply its own environment. An automatic Dibs binding needs evidence that
the app associated the specific call with the exact conversation.
[Session attestation boundary](PROCESS-REFRESH-DESIGN.md#attestation-threat-review-request-18594).

## Measurement build

The existing Antigravity stdio bridge reports whether a valid conversation ID
appears in the app's per-call MCP metadata. It logs presence only, once for
each observed conversation and once for a missing or invalid field. The daemon
requires its local Unix socket peer to be the actual MCP child. A native
process inspection checks the installed desktop language server, its parent
app and both stdio pipe peers. One bridge may serve several conversations.
Each call names its own conversation, and each conversation has its own
recovery credential cache.

A successful authenticated check-in records the verified binding for the agent's
current session. The board and doctor show its binding status. Before each
wake the daemon checks that the exact conversation still answers, compares
the workspace if the row has a working directory, and checks the hub again
before offering input. The offer carries a factual event kind. An ambiguous
result leaves the mail available without a fallback or an automatic retry.

The measurement build confirmed the per-call ID on Antigravity 2.34.0. On
2026-11-21 the app accepted a Dibs mail wake into the existing
Antigravity conversation in under half a second. The recipient took a turn
and read the mail about 19 seconds after send, while Safari stayed frontmost.

A verified check-in on a persistence-capable daemon enrolls the conversation
for subsequent daemon restarts. Earlier versions stored enrollment in
memory. The daemon resolves the current live desktop hub and rechecks the
recorded conversation and workspace on each wake. It compares the hub's
process and credential again before sending. The ledger stores the
conversation attestation without hub process details or credentials.
Changing or releasing the current session, changing hosts, or relocating
the agent invalidates the attestation.
