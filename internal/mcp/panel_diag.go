package mcp

import "os"

// panelMinimal serves a stripped template instead of the real one when
// DIBS_PANEL_MINIMAL=1.
//
// It exists because a host rendered an EMPTY container: the widget was created,
// our HTML drew nothing, and no amount of local simulation reproduced it. The
// real template carries three things a host might reject. ~68 KB of base64
// font in a data: URI, an inline <script>, and an inline <style>, and a blank
// result cannot distinguish between them.
//
// This template has none: no JavaScript, no fonts, no data: URIs, no external
// anything. Static markup and a handful of inline styles. If THIS renders, the
// host is fine and the fault is in what the real template loads. If this is
// also blank, the host is not rendering server HTML at all and nothing we ship
// will change that.
const minimalPanelHTML = `<!doctype html>
<meta charset="utf-8">
<title>Dibs</title>
<div style="font:600 14px system-ui;padding:20px;color:#0a0;border:2px solid #0a0;border-radius:10px;margin:12px">
  LANES PANEL: RENDERED
  <div style="font:400 12px system-ui;color:#666;margin-top:8px">
    Static HTML only: no scripts, no fonts, no external resources.
    If you can read this, the host renders MCP Apps and the fault is in the
    full template's assets or its postMessage handshake.
  </div>
</div>`

var panelMinimal = os.Getenv("DIBS_PANEL_MINIMAL") == "1"
