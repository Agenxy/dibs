# Dibs in Antigravity desktop

Install with the Antigravity CLI on your `PATH`:

```text
dibs antigravity-plugin install
```

The Dibs installer moves an existing `dibs` entry from the desktop profile's
`mcp_config.json` into the native plugin. It preserves the server's settings
and the other servers in the profile. Reinstalling preserves the configured
server. For a fresh install it records the current executable and the selected
board's bridge settings. `dibs doctor` warns if both paths define the server.
Reload the Antigravity session after moving a manual entry into the plugin.
During a live migration on desktop 2.34.0, the manual `dibs` server disappeared
before `dibs_dibs` appeared. One wake reached a session with no Dibs tools until
it reloaded. The board retained the mail.

From a Dibs checkout with no manual `dibs` entry, `agy plugin install
./plugins/antigravity` installs the native plugin with `dibs` resolved from
the harness's `PATH`. `agy plugin list --json` shows the installed skill and
MCP server.

To allow calls to the Dibs MCP server, run this command as the operator:

```text
dibs antigravity-plugin --trust
```

The command uses `dibs_dibs` for the native plugin and `dibs` for a manual MCP
entry. It adds the documented `mcp(<server>/*)` wildcard and one
`mcp(<server>/<tool>)` rule per tool in your Dibs binary for each configured
server. The per-tool rules are a fallback until desktop wildcard matching is
measured independently. Run the command again after adding the plugin or
upgrading Dibs.

The command updates the allow list in `~/.gemini/config/config.json` and
preserves existing settings and rules. Open Antigravity once to create the
profile if it is missing. Antigravity gives Deny and Ask rules precedence over
Allow. `dibs doctor` reports missing explicit rules for each configured server.
The [Antigravity MCP documentation](https://antigravity.google/docs/mcp/)
describes the server-scoped rule.

## Manual MCP setup

Use the stdio block from `dibs mcp-config` in `~/.gemini/config/mcp_config.json`
when you configure the server directly. Gemini CLI uses `~/.gemini/settings.json`.

Add the `dibs` entry under `mcpServers`, keeping any existing entries.
Use the absolute executable path printed by the command. Keep the printed
`env` values when they select a non-default board.

```json
{
  "mcpServers": {
    "dibs": {
      "command": "/absolute/path/to/dibs",
      "args": ["mcp-stdio"]
    }
  }
}
```

In Antigravity's **Customizations**, check that the MCP server is enabled.
From the desktop conversation you want to receive mail, register or recover
your existing Dibs identity, then call `check_in`. On macOS, the board and
`dibs doctor` show whether Dibs received the verified conversation binding.

Antigravity supports native lifecycle hooks. This plugin ships without mail
hooks. Dibs delivers to an enrolled
conversation through the running desktop app's native send-message route.
The recorded binding survives a Dibs daemon restart. `check_in` and `inbox`
show mail when wake delivery is unconfirmed.

Measured on 2026-11-21 with Antigravity 2.34.0 on macOS. The app accepted a Dibs
notice in the existing conversation in under half a second. The agent read
the mail about 19 seconds after send while Safari stayed frontmost. See the
[delivery measurements](../../docs/ANTIGRAVITY-APP-DELIVERY.md) for details.

On 2026-11-22, `agy` 1.1.10 validated isolated plugin fixtures and `agy` 1.5.14
installed the Dibs package with the selected board. In that scratch CLI profile,
the trust command added the documented rule and preserved existing grants.
The CLI inventory lists `dibs`. On Antigravity desktop 2.34.0, the migrated
plugin appeared as `dibs_dibs` after the session reloaded. `check_in`,
`read_mail`, and `respond` ran without triggering prompts in that session.
Which live permission allowed those calls remains unmeasured.
