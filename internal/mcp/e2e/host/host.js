/**
 * The host side of the panel test, running in a real browser.
 *
 * This is the actual `AppBridge` from @modelcontextprotocol/ext-apps: the same
 * implementation shipping hosts embed: driving the panel in a real iframe, so
 * the panel gets real layout, real CSS and a real postMessage boundary.
 * Bundled by `bun build` and served same-origin so the two frames may talk.
 *
 * Everything a test needs is hung off `window.__probe`; Playwright reads that
 * rather than reaching into the bridge's internals.
 */
import { AppBridge, PostMessageTransport } from "@modelcontextprotocol/ext-apps/app-bridge"

const probe = {
  initialized: false,
  appInfo: null,
  appCapabilities: undefined,
  sizes: [],
  modelContexts: [],
  toolCalls: [],
  frames: [],
  errors: [],
}
window.__probe = probe

// AppBridge wants an MCP Client. A full in-browser SDK client would need the
// daemon to send CORS headers, which it deliberately does not, so tool calls
// go through this page's own origin, which proxies them to the real daemon with
// the local secret attached. Everything below /rpc is genuinely the daemon.
// AppBridge wants an MCP Client, and on connect() it installs its OWN proxying
// handlers that call `client.request(...)`: overwriting anything the host set
// beforehand. So the shim must implement `request`, not just `callTool`;
// implementing only the latter made every panel action die silently, which is
// exactly the contract detail a hand-written host mock cannot teach you.
//
// A real in-browser SDK client would need the daemon to send CORS headers,
// which it deliberately does not, so requests go through this page's own origin
// and are proxied to the daemon with the local secret attached. Everything
// below /rpc is genuinely the daemon.
const client = {
  getServerCapabilities: () => window.__serverCapabilities,
  setNotificationHandler() {},
  async request({ method, params }) {
    if (method === "tools/call") probe.toolCalls.push(params)
    const res = await fetch("/rpc", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ method, params }),
    })
    const body = await res.json()
    if (body.error) throw new Error(String(body.error))
    return body.result
  },
  callTool(params) { return this.request({ method: "tools/call", params }) },
  listTools(params = {}) { return this.request({ method: "tools/list", params }) },
}

// The iframe is created empty and connected FIRST, then the panel is written
// into that same window with document.write.
//
// Order is load-bearing twice over. A real host is listening before its app
// starts, and the panel sends ui/initialize the instant its script parses: set
// a src and the handshake is gone before the transport exists. Writing into the
// already-connected about:blank window also preserves `contentWindow` identity,
// which is what lets the transport keep its event-source check rather than
// disabling it: navigation would swap the window out from under it.
const iframe = document.createElement("iframe")
iframe.id = "panel"
iframe.style.cssText = "width:900px;height:1400px;border:0;display:block"
document.body.appendChild(iframe)

const bridge = new AppBridge(
  client,
  { name: "lanes-panel-e2e", version: "1" },
  {
    openLinks: {},
    serverTools: window.__serverCapabilities?.tools,
    serverResources: window.__serverCapabilities?.resources,
    updateModelContext: { text: {} },
  },
  {
    hostContext: {
      // The HOST is authoritative for theme in MCP Apps: the panel reads
      // ctx.theme and never a media query, so a page-level colour scheme
      // cannot reach it. A test host that can only say "dark" makes the light
      // theme unobservable, which is how this board once shipped one that was
      // unreadable while every check passed. Default stays dark; the inspector
      // sets this global to look at the other one.
      theme: window.__hostTheme ?? "dark",
      platform: "web",
      containerDimensions: { width: 900, maxHeight: 6000 },
      displayMode: "inline",
      availableDisplayModes: ["inline", "fullscreen"],
    },
  },
)

bridge.addEventListener("initialized", () => {
  probe.initialized = true
  probe.appInfo = bridge.getAppVersion?.() ?? null
  probe.appCapabilities = bridge.getAppCapabilities()
})
bridge.addEventListener("sizechange", (e) => {
  const h = e?.detail?.height ?? e?.height
  if (h) probe.sizes.push(h)
})
bridge.onupdatemodelcontext = (req) => { probe.modelContexts.push(req); return {} }

// No oncalltool here on purpose: connect() installs AppBridge's own proxy when
// a client is present, and setting one beforehand is silently replaced. Letting
// the SDK's real proxying path run is also the more faithful test.
bridge.onerror = (err) => probe.errors.push(String(err?.message ?? err))

await bridge.connect(
  new PostMessageTransport(iframe.contentWindow, iframe.contentWindow),
)

const panelHTML = await (await fetch("/panel.html")).text()
iframe.contentDocument.open()
iframe.contentDocument.write(panelHTML)
iframe.contentDocument.close()

// Raw wire tap, so a failure can be diagnosed at the frame level rather than
// guessed at from missing side effects.
window.addEventListener("message", (e) => {
  if (e.data?.jsonrpc === "2.0") probe.frames.push(e.data)
})

window.__bridge = bridge
window.__deliver = (result) => bridge.sendToolResult({ toolName: "show_board", ...result })
window.__panelDoc = () => iframe.contentDocument
window.__ready = true
