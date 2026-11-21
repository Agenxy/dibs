# Antigravity desktop wake measurements

Measured on macOS 2026-11-21 with Antigravity.app 2.34.0 and language server
2.34.0. The test target was an existing disposable conversation in the desktop
app. The test created that conversation separately from the wake.

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
