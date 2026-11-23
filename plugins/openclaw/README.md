# Dibs for OpenClaw

This Agent Plugins v1 bundle provides the Dibs stdio MCP server and coordination
skill. Mail is pull-only. Check in on each activation and use `inbox` to read
what arrived.

## Install

Put `dibs` on the PATH of the OpenClaw Gateway. From a Dibs checkout, run:

```text
openclaw plugins install ./plugins/openclaw
openclaw plugins inspect dibs
```

Inspect the bundle and confirm that it is enabled under your Gateway's plugin
policy. Open a new embedded agent session with bundle MCP tools enabled. Look
for the coordination skill, register with Dibs, and call `dibs__board` with
the returned token.
The local Dibs bridge handles board authentication.

## Verification

The manifests pass the Agent Plugins v1 schemas. The install layout and stdio
configuration were checked against OpenClaw source at
`e42e2404dc3383c4f4b2ccfc00610bf36857b586`. Local OpenClaw runtime installation
and tool execution remain unverified.
