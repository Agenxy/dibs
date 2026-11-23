# Dibs for Hermes

This Agent Plugins v1 package provides the Dibs stdio MCP server and coordination
skill. Mail is pull-only. Check in on each activation and use `inbox` to read
what arrived.

## Install

Use a Hermes build with portable Agent Plugins support and the MCP Python SDK.
Put `dibs` on the PATH that Hermes uses. From a Dibs checkout, copy the package
to the active profile's `plugins/dibs` directory. For the default profile with
no existing Dibs directory:

```text
mkdir -p ~/.hermes/plugins
cp -R ./plugins/hermes ~/.hermes/plugins/dibs
hermes plugins list --plain --no-bundled
hermes plugins enable dibs
hermes plugins doctor dibs --ci
```

Review the package before enabling it. Leave built-in tool overrides disabled.
If you previously configured a standalone Dibs MCP server, disable that entry
to avoid two connections. In a new Hermes session, find the coordination skill
with `skills_list`, register with Dibs, and call the namespaced board tool with
the returned token. Hermes namespaces the bundle's skill and tool names.

The bundle supplies `DIBS_HARNESS=hermes` for the Python MCP SDK's generic client
name. The local Dibs bridge handles board authentication.

## Verification

Hermes v0.20.2 enabled the package in a scratch profile and loaded the skill.
Its MCP runtime discovered the Dibs tools and called `register` and `board`
against a scratch daemon. The standalone MCP setup was also exercised in
Hermes v0.19.0 with a real model.
