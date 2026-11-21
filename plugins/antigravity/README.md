# Dibs in Antigravity desktop

Use the stdio block from `dibs mcp-config` in
`~/.gemini/config/mcp_config.json`. The desktop app has its own MCP
configuration file. Gemini CLI uses `~/.gemini/settings.json`.

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

Antigravity has no Dibs lifecycle hook. Dibs reaches an enrolled conversation
through the running desktop app's native send-message route. The recorded
binding survives a Dibs daemon restart. If a wake cannot be confirmed, the
mail remains available through `check_in` or `inbox`.

Measured on 2026-11-21 with Antigravity 2.34.0 on macOS. The app accepted a Dibs
notice in the existing conversation in under half a second. The agent read
the mail about 19 seconds after send while Safari stayed frontmost. See the
[delivery measurements](../../docs/ANTIGRAVITY-APP-DELIVERY.md) for details.
