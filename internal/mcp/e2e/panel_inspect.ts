/**
 * Panel inspector: see what an MCP App actually renders, without a screenshot.
 *
 * The panel e2e proves the panel works. It cannot tell you what the panel LOOKS
 * like right now, against the board you actually have, on a host that behaves
 * the way yours does, and that gap is expensive. A regression that blanked the
 * board panel survived a green suite and three rounds of reasoning because the
 * only instrument anyone had was a human describing what they saw.
 *
 * This is the instrument. It mounts the real template (fetched from the running
 * daemon with resources/read, so it is the artifact that ships) inside the real
 * AppBridge from @modelcontextprotocol/ext-apps, in a real browser, against your
 * real board. Then it mirrors what the panel drew into the TOP document as plain
 * text, so it can be read by anything that can read a page: including an agent
 * with no eyes.
 *
 * The carrier switches are the point. A tool result reaches an app by three
 * routes, _meta, structuredContent, and ordinary content, and hosts disagree
 * about which they forward. Dropping them here reproduces a specific host's
 * behaviour on purpose, which is how you find out that a panel depends on a
 * carrier the host does not send.
 *
 *   task panel:inspect                      # live daemon, nothing dropped
 *   task panel:inspect -- --drop meta       # a host that forwards no _meta
 *   task panel:inspect -- --tool check_in
 *
 * It prints a URL and stays up. Open it, click a tool, read the dump.
 */
import { chromium, type Browser } from "playwright"
import { homedir } from "node:os"
import { join } from "node:path"

const HERE = import.meta.dir
const DAEMON = process.env.DIBS_ADDR ?? "127.0.0.1:4777"
const DIBS_DIR = process.env.DIBS_DIR ?? join(homedir(), ".agents")
const PORT = Number(process.env.INSPECT_PORT ?? 4942)

const argv = process.argv.slice(2)
const argOf = (flag: string, fallback: string) => {
  const i = argv.indexOf(flag)
  return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback
}
const drops = argv.flatMap((a, i) => (a === "--drop" ? [argv[i + 1]] : [])).filter(Boolean)
const defaultTool = argOf("--tool", "board")
// Headed is the default: a human opening this wants to see it. --headless is for
// running it as an instrument, where the dump is read over CDP instead.
const headless = argv.includes("--headless")

let rpcId = 0
let secret = ""
async function rpc(method: string, params: unknown): Promise<any> {
  const res = await fetch(`http://${DAEMON}/mcp`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      accept: "application/json, text/event-stream",
      "X-Dibs-Local": secret,
    },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method, params }),
  })
  const body = (await res.json()) as any
  if (body.error) throw new Error(method + ": " + JSON.stringify(body.error))
  return body.result
}

let browser: Browser | undefined
let server: ReturnType<typeof Bun.serve> | undefined
process.on("exit", () => {
  try { browser?.close() } catch {}
  try { server?.stop(true) } catch {}
})

secret = (await Bun.file(join(DIBS_DIR, "local.secret")).text()).trim()

// A token to look at the board WITH. Reusing one you already hold shows you
// exactly what that agent's panel shows; otherwise register an inspector agent,
// which is honest: it really is another agent on this machine, and it will
// appear on the board it is inspecting.
let token = argOf("--token", "")
if (!token) {
  const reg = await rpc("tools/call", {
    name: "register",
    arguments: {
      name: "panel-inspect",
      description: "Looking at the board panel: an instrument, not a worker",
      session_id: "panel-inspect",
    },
  })
  token = JSON.parse(reg.content[0].text).token
  await rpc("tools/call", { name: "check_in", arguments: { token } })
}

const read = await rpc("resources/read", { uri: "ui://agents/board" })
const panelHTML: string = read.contents[0].text
const caps = (await rpc("initialize", {
  protocolVersion: "2025-11-25",
  capabilities: {},
  clientInfo: { name: "agents-panel-inspect", version: "1" },
})).capabilities

const built = await Bun.build({
  entrypoints: [join(HERE, "host", "host.js")],
  target: "browser",
  minify: false,
})
if (!built.success) throw new Error("host bundle failed: " + built.logs.join("\n"))
const hostJS = await built.outputs[0].text()

// The inspector chrome, in the TOP document. Deliberately not inside the panel
// iframe: the panel must stay the shipping artifact, unmodified, or this
// instrument would be measuring itself.
const inspectJS = `
const DROPS = new Set(${JSON.stringify(drops)})
const TOKEN = ${JSON.stringify(token)}

function strip(result) {
  const out = { ...result }
  if (DROPS.has("meta")) delete out._meta
  if (DROPS.has("structured")) delete out.structuredContent
  if (DROPS.has("content")) delete out.content
  return out
}

async function callTool(name, args) {
  const res = await fetch("/rpc", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ method: "tools/call", params: { name, arguments: args } }),
  })
  const body = await res.json()
  if (body.error) throw new Error(String(body.error))
  return body.result
}

async function deliver(name) {
  const args = { token: TOKEN }
  if (name === "board") args.view = "board"
  const result = await callTool(name, args)
  const sent = strip(result)
  window.__lastResult = { tool: name, dropped: [...DROPS], sent }
  window.__deliver(sent)
  setTimeout(dump, 400)
}

// What the panel DREW, as text. This is the whole reason the inspector exists:
// an iframe's rendered state is not reachable by anything that reads the page,
// and a screenshot cannot be diffed or grepped.
function dump() {
  const doc = window.__panelDoc()
  const text = (sel) => (doc.querySelector(sel)?.textContent ?? "").trim().replace(/\\s+/g, " ")
  const tabs = ["board", "agents", "mail", "activity"]
  const lines = []
  lines.push("── panel ──────────────────────────────────────────")
  lines.push("context:  " + text("#ctx-agent") + "  ·  " + text("#ctx-caps"))
  // Whether the person at the keyboard has proved they are here, and what the
  // affordance is offering them. Locked is the resting state, not a fault.
  const lock = doc.querySelector("#human-lock")
  // Present is not the same as visible, and the difference is the whole reason
  // this instrument exists. "human: locked" was reported as healthy from an
  // element that measured 0x0 on screen, so the dump agreed with the code and
  // disagreed with the person looking at the panel, which is the exact failure
  // a text instrument is supposed to make impossible. Measure it.
  let lockGeom = ""
  if (lock) {
    const r = lock.getBoundingClientRect()
    const cs = getComputedStyle(lock)
    const painted = r.width > 0 && r.height > 0 &&
      cs.visibility !== "hidden" && cs.display !== "none" && Number(cs.opacity) > 0
    lockGeom = painted
      ? " [" + Math.round(r.width) + "x" + Math.round(r.height) +
        " at " + Math.round(r.x) + "," + Math.round(r.y) + "]"
      : " [NOT PAINTED. " + Math.round(r.width) + "x" + Math.round(r.height) +
        ", display:" + cs.display + " visibility:" + cs.visibility +
        " opacity:" + cs.opacity + "]"
  }
  lines.push("human:    " + (lock
    ? (lock.getAttribute("data-state") || "?") + ". " + text("#human-lock-label") + lockGeom
    : "(no affordance in this build)"))
  // WHICH BUILD of the panel is on screen. Hosts cache the template by URI, so
  // a stale panel and a server that never shipped the fix look identical: this
  // is the line that tells them apart, here and in any screenshot.
  lines.push("build:    " + (text("#foot-build") || "(none: a pre-build-id panel)")
    + "   node " + text("#foot-node") + "   host " + text("#foot-host"))
  // The four figures a person reads first. The selector here was ".stat,.
  // tally"; ".stat" matches nothing in the shipping panel, so this only ever
  // printed the three tab tallies, and a panel drawing "0/8 live" beside eight
  // entries would have looked correct in this dump.
  const summary = [...doc.querySelectorAll("#pane-summary .metric, .metric")]
    .map((n) => n.textContent.trim().replace(/\\s+/g, " "))
  if (summary.length) lines.push("figures:  " + summary.join("   "))
  const tallies = [...doc.querySelectorAll(".tally")]
    .map((n) => n.textContent.trim().replace(/\\s+/g, " "))
  if (tallies.length) lines.push("tallies:  " + tallies.join("   "))
  for (const t of tabs) {
    const pane = doc.querySelector("#pane-" + t)
    if (!pane) continue
    const body = (pane.textContent ?? "").trim().replace(/\\s+/g, " ")
    // Panes are display:none when inactive but their text is still there, which
    // is what makes one dump enough to see every tab.
    lines.push("")
    lines.push("[" + t + "] " + (body || "(empty)"))
  }
  lines.push("")
  lines.push("── entries drawn ──────────────────────────────────")
  const entries = [...doc.querySelectorAll(".entry")].map((n) =>
    (n.textContent ?? "").trim().replace(/\\s+/g, " "))
  lines.push(entries.length ? entries.join("\\n") : "(none)")
  lines.push("")
  lines.push("── what the host sent the app ─────────────────────")
  lines.push(JSON.stringify(window.__lastResult ?? null, null, 2)?.slice(0, 4000) ?? "null")
  lines.push("")
  lines.push("── the panel's own calls back to Dibs ────────────")
  lines.push(JSON.stringify(window.__probe.toolCalls.map((c) => c.name)) +
    (window.__probe.toolCalls.length ? "" : "  (none: it asked for nothing)"))
  lines.push("")
  lines.push("── page errors ────────────────────────────────────")
  lines.push(window.__probe.errors.length ? window.__probe.errors.join("\\n") : "(none)")
  document.getElementById("dump").textContent = lines.join("\\n")
}
window.__dump = dump

for (const name of ["board", "check_in", "inbox"]) {
  const b = document.createElement("button")
  b.textContent = name
  b.onclick = () => deliver(name)
  document.getElementById("bar").appendChild(b)
}
const refresh = document.createElement("button")
refresh.textContent = "re-read panel"
refresh.onclick = dump
document.getElementById("bar").appendChild(refresh)

document.getElementById("dropped").textContent =
  DROPS.size ? "dropping: " + [...DROPS].join(", ") : "dropping nothing"

// Deliver once on load so the page is useful the moment it opens.
deliver(${JSON.stringify(defaultTool)})
`

const page404 = new Response("not found", { status: 404 })
server = Bun.serve({
  port: PORT,
  async fetch(req) {
    const url = new URL(req.url)
    if (url.pathname === "/rpc") {
      const body = (await req.json()) as any
      try {
        return Response.json({ result: await rpc(body.method, body.params) })
      } catch (e) {
        return Response.json({ error: String(e) })
      }
    }
    if (url.pathname === "/favicon.ico") return new Response(null, { status: 204 })
    if (url.pathname === "/panel.html")
      return new Response(panelHTML, { headers: { "content-type": "text/html" } })
    if (url.pathname === "/host.js")
      return new Response(hostJS, { headers: { "content-type": "text/javascript" } })
    if (url.pathname === "/inspect.js")
      return new Response(inspectJS, { headers: { "content-type": "text/javascript" } })
    if (url.pathname === "/")
      return new Response(
        `<!doctype html><meta charset=utf-8><title>Dibs panel inspector</title>` +
        `<body style="margin:0;background:#0E0F11;color:#c8ccd2;` +
        `font:13px ui-monospace,SFMono-Regular,Menlo,monospace">` +
        `<div id="bar" style="padding:8px;display:flex;gap:8px;align-items:center"></div>` +
        `<div id="dropped" style="padding:0 8px 8px;color:#8a9098"></div>` +
        `<script>window.__serverCapabilities=${JSON.stringify(caps ?? {})};` +
        `window.__hostTheme=${JSON.stringify(argv.includes("--light") ? "light" : "dark")}</script>` +
        `<script type="module" src="/host.js"></script>` +
        `<script type="module">` +
        `await new Promise((r) => { const t = setInterval(() => {` +
        `  if (window.__ready) { clearInterval(t); r() } }, 50) });` +
        `await import("/inspect.js")</script>` +
        `<pre id="dump" style="padding:8px;white-space:pre-wrap;` +
        `border-top:1px solid #23262b;margin:0">loading…</pre>`,
        { headers: { "content-type": "text/html" } })
    return page404
  },
})

const space = process.env.PW_CHANNEL === undefined ? "chrome" : process.env.PW_CHANNEL
browser = await chromium.launch({ ...(space ? { space } : {}), headless })
// --light renders in the light theme.
//
// Worth a flag rather than a one-off check: this board once shipped a light
// theme that was literally unreadable while 155 end-to-end checks passed on it,
// because every assertion was about structure and none about whether the
// foreground and background differed. Dark is the default anyone looks at, so
// light is the one that rots unobserved.
const page = await browser.newPage({
  viewport: { width: 1100, height: 1200 },
  colorScheme: argv.includes("--light") ? "light" : "dark",
})
page.on("pageerror", (e) => console.log("page error:", String(e)))
await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: "load" })
await page.waitForFunction("window.__ready === true", null, { timeout: 20000 })
await page.waitForFunction(
  "document.getElementById('dump').textContent !== 'loading…'", null, { timeout: 20000 })

// Print the dump to stdout too, so the instrument is useful without a browser
// pane at all: this is the form an agent reads.
console.log(await page.textContent("#dump"))

// --unlock drives the human lock and re-reads the panel.
//
// Every other affordance here can be reached by delivering a tool result, but
// the human actions cannot: they are gated behind a fingerprint, so until now
// the only way to see the unlocked panel was for a person to put their finger on
// a sensor while somebody watched. That made the whole surface: the composers,
// the join-first state, the two refusal sentences: unobservable to anything
// that reads text.
//
// Point this at a daemon built with `-tags dibdev` and DIBS_PRESENCE_MOCK
// set, and the click goes all the way through: panel → human_unlock → verdict →
// re-render. Against an ordinary daemon it raises a real system sheet, which is
// correct, and is why this is a flag rather than something the inspector does on
// its own.
if (argv.includes("--unlock")) {
  const clicked = await page.evaluate(`(() => {
    const el = window.__panelDoc().querySelector("#human-lock")
    if (!el) return "no #human-lock in the panel"
    el.click()
    return "clicked"
  })()`)
  if (clicked !== "clicked") {
    console.log(`\n  unlock: ${clicked}`)
  } else {
    // The unlock is a round trip to the daemon, so give it one before re-reading
    // rather than reporting the pre-click state as the result.
    await page.waitForTimeout(1500)
    await page.evaluate("window.__dump()")
    console.log("\n── after clicking the human lock ───────────────────")
    console.log(await page.textContent("#dump"))
  }
}

// --shot <path> writes a PNG of the panel itself, cropped to the iframe.
//
// The text dump is the right instrument for "is the data there", and it is the
// wrong one for "does this look designed". Reading a board as one paragraph of
// run-together text says nothing about hierarchy, density, rhythm or weight,
// the things a person actually reacts to, so design work on this panel was
// being done blind, against a description of the page rather than the page.
if (argv.includes("--shot")) {
  const out = argOf("--shot", "panel.png")
  const frame = page.locator("iframe").first()
  await frame.screenshot({ path: out })
  console.log(`\nwrote ${out}`)
}

console.log(`\ninspector: http://127.0.0.1:${PORT}/   (drops: ${drops.join(",") || "none"})`)

if (argv.includes("--once")) process.exit(0)
console.log("staying up: ctrl-c to stop")
await new Promise(() => {})
