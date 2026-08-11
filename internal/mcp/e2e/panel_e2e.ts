/**
 * End-to-end test for the MCP Apps board panel. Bun, Playwright, real Chrome.
 *
 * The whole stack is real except nothing:
 *
 *   panel (fetched over MCP with resources/read: the artifact that ships)
 *     ↕ postMessage, across a real iframe boundary
 *   AppBridge + PostMessageTransport   (the real @modelcontextprotocol/ext-apps
 *                                       SDK that shipping hosts embed)
 *     ↕ HTTP
 *   dibd                             (a real daemon on a scratch data dir)
 *
 * Using the real AppBridge is the point. A rejected handshake is SILENT: the
 * SDK validates every frame with zod and validates ui/initialize against
 * McpUiInitializeRequestSchema, and a panel sending the wrong shape simply
 * never comes up, with no error anywhere. Dibs shipped exactly that bug once
 * (clientInfo/capabilities where the spec wants appInfo/appCapabilities) and it
 * cost days to find by eye. Here it fails on the first assertion.
 *
 * A real browser rather than a DOM shim matters for the same reason: the first
 * version of this ran on jsdom, which has no layout, so the size assertion had
 * to fake getBoundingClientRect and was therefore vacuous, and a zero-height
 * iframe is one of the bugs this panel actually shipped. Chrome measures.
 *
 * Run: task test:panel
 */
import { chromium, type Browser } from "playwright"
// node:fs and node:os only for mkdtemp/rm/tmpdir, which Bun has no native
// equivalent for; everything else below uses Bun's own APIs. These specifiers
// are the cross-runtime standard and Bun implements them natively: they do not
// pull in Node.
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const HERE = import.meta.dir
const DAEMON = `127.0.0.1:${process.env.PORT ?? 4931}`
const SERVE = Number(process.env.SERVE_PORT ?? 4941)

let failures = 0
let checks = 0
function check(name: string, cond: boolean, detail = "") {
  checks++
  if (cond) console.log(`  \x1b[32m✓\x1b[0m ${name}`)
  else { failures++; console.log(`  \x1b[31m✗\x1b[0m ${name}${detail ? ". " + detail : ""}`) }
}

const dir = mkdtempSync(join(tmpdir(), "agents-e2e-"))
const dibd = process.env.DIBD ?? `${process.env.HOME}/.local/bin/dibd`
const daemon = Bun.spawn({
  cmd: [dibd, "-dir", dir, "-addr", DAEMON],
  stdout: "ignore", stderr: "ignore",
})

let browser: Browser | undefined
let server: ReturnType<typeof Bun.serve> | undefined
function cleanup() {
  try { browser?.close() } catch {}
  try { server?.stop(true) } catch {}
  daemon.kill()
  try { rmSync(dir, { recursive: true, force: true }) } catch {}
}
process.on("exit", cleanup)

let rpcId = 0
let secret = ""
async function rpc(method: string, params: unknown): Promise<any> {
  const res = await fetch(`http://${DAEMON}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method, params }),
  })
  const body = (await res.json()) as any
  if (body.error) throw new Error(method + ": " + JSON.stringify(body.error))
  return body.result
}
const tool = (name: string, args: unknown) => rpc("tools/call", { name, arguments: args })
const textOf = (r: any) => JSON.parse(r.content[0].text)

try {
  for (let i = 0; ; i++) {
    try { await fetch(`http://${DAEMON}/`); break } catch {
      if (i > 60) throw new Error("daemon never came up")
      await Bun.sleep(200)
    }
  }
  secret = (await Bun.file(join(dir, "local.secret")).text()).trim()

  // ── seed a board worth rendering ─────────────────────────────────────────
  const me = textOf(await tool("register", {
    name: "reviewer", description: "reviewing the panel", session_id: "e2e-1" }))
  await tool("check_in", { token: me.token })
  await tool("declare", { token: me.token, text: "running the panel e2e",
    refs: ["board_app.html"], dirs: ["internal/mcp"] })
  const peer = textOf(await tool("register", {
    name: "peer", description: "sends mail", session_id: "e2e-2" }))
  await tool("check_in", { token: peer.token })

  const mail: Record<string, any> = {}
  for (const [key, type, body] of [
    ["q", "question", "Does the panel render this?"],
    ["r", "request", "Approve the redesign?"],
    ["n", "notify", "For your information."],
  ] as const) {
    mail[key] = textOf(await tool("send", { token: peer.token, to: me.lane_id,
      type, body, op_id: "e2e-" + key, deadline_s: 600 }))
  }

  // A space the reading agent is a member of, so the panel can mark it as
  // the viewer's own: the one thing this surface knows that the operator
  // board deliberately does not.
  await tool("open_space", { token: me.token, agent: "panel-work", topic: "rendering the panel" })
  await tool("join_space", { token: peer.token, agent: "panel-work", score: 0.64, threshold: 0.33,
    scorer_id: "lexical+cochange", evidence: ["internal/mcp/board_app.html"], auto: true })

  // ── the template, fetched the way a host fetches it ──────────────────────
  // The panel URI carries the template's content hash so a changed panel cannot
  // be served from a host's cache. Read it from the tool result rather than
  // pinning the literal: a test that hardcodes the version would have to be
  // edited on every panel change, which is the opposite of what it is for.
  const panelURI: string = (await tool("board", { token: me.token }))
    ?._meta?.ui?.resourceUri
  const read = await rpc("resources/read", { uri: panelURI })
  const html: string = read.contents[0].text
  check("resources/read returns the template", html.length > 5000, `${html.length} bytes`)
  check("declares the MCP Apps mime type",
    read.contents[0].mimeType === "text/html;profile=mcp-app")
  check("no external origin in the template", !html.includes("https://fonts."))
  check("fonts are inlined", html.includes("data:font/woff2;base64,"))

  const boardResult = await tool("board", { token: me.token })
  check("board attaches the UI resource",
    typeof boardResult?._meta?.ui?.resourceUri === "string" &&
      /^ui:\/\/agents\/board\/[0-9a-f]{12}$/.test(boardResult._meta.ui.resourceUri),
    boardResult?._meta?.ui?.resourceUri ?? "(none)")
  // Cache-busting is only real if the version tracks the bytes AND the panel
  // says which build it is: a host serving a cached panel is otherwise
  // indistinguishable from a server that never shipped the fix.
  check("the panel prints the build the URI names",
    html.includes("panel " + panelURI.split("/").pop()),
    panelURI)
  check("board sends a private panel payload",
    !!boardResult?._meta?.["com.dibs/panel"])
  // structuredContent is model-facing, so the board must not be in it. What may
  // be is the panel's bootstrap: the view, the agent id, and the caller's own
  // token, which is how the panel reaches the board on a host that drops _meta.
  // Asserting "undefined" instead of "carries no board" is what made the repair
  // for that host look like a contract violation.
  const boardBoot = boardResult?.structuredContent ?? {}
  check("board does not duplicate the board into model context",
    boardBoot.board === undefined && boardBoot.inbox === undefined,
    JSON.stringify(boardBoot).slice(0, 200))
  check("board bootstraps the panel with the caller's own token",
    boardBoot.act_token === me.token)

  const caps = (await rpc("initialize", {
    protocolVersion: "2025-11-25", capabilities: {},
    clientInfo: { name: "agents-panel-e2e", version: "1" },
  })).capabilities

  // ── bundle the host and serve everything same-origin ─────────────────────
  const built = await Bun.build({
    entrypoints: [join(HERE, "host", "host.js")],
    target: "browser",
    minify: false,
  })
  if (!built.success) throw new Error("host bundle failed: " + built.logs.join("\n"))
  const hostJS = await built.outputs[0].text()

  const page404 = new Response("not found", { status: 404 })
  server = Bun.serve({
    port: SERVE,
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
      // Chrome always asks for this; an unanswered favicon would fail the
      // "no uncaught errors" assertion for a reason that is not about Dibs.
      if (url.pathname === "/favicon.ico") return new Response(null, { status: 204 })
      if (url.pathname === "/panel.html")
        return new Response(html, { headers: { "content-type": "text/html" } })
      if (url.pathname === "/host.js")
        return new Response(hostJS, { headers: { "content-type": "text/javascript" } })
      if (url.pathname === "/")
        return new Response(
          `<!doctype html><meta charset=utf-8><title>panel e2e</title>` +
          `<body style="margin:0;background:#0E0F11">` +
          `<script>window.__serverCapabilities=${JSON.stringify(caps ?? {})}</script>` +
          `<script type="module" src="/host.js"></script>`,
          { headers: { "content-type": "text/html" } })
      return page404
    },
  })

  // ── a real browser ───────────────────────────────────────────────────────
  // Locally this drives the system Chrome, so nothing has to be downloaded.
  // CI sets PW_CHANNEL empty to use Playwright's own chromium instead.
  const space = process.env.PW_CHANNEL === undefined ? "chrome" : process.env.PW_CHANNEL
  browser = await chromium.launch({ ...(space ? { space } : {}), headless: true })
  const page = await browser.newPage({ viewport: { width: 1000, height: 900 } })
  const consoleErrors: string[] = []
  page.on("pageerror", (e) => consoleErrors.push(String(e)))
  page.on("console", (m) => { if (m.type() === "error") consoleErrors.push(m.text()) })

  await page.goto(`http://127.0.0.1:${SERVE}/`, { waitUntil: "load" })
  await page.waitForFunction("window.__ready === true", null, { timeout: 15000 })

  const panel = page.frameLocator("#panel")

  // First paint must not wait on the host. An earlier version drew only after
  // ui/initialize settled, so a silent host left the panel blank for the whole
  // timeout, and a host that sizes to content then showed the human nothing.
  await panel.locator(".metric").first().waitFor({ timeout: 10000 })
  check("paints before any board data arrives", true)

  await page.waitForFunction("window.__probe.initialized === true", null, { timeout: 10000 })
    .then(() => check("the real AppBridge accepted our ui/initialize", true))
    .catch(() => check("the real AppBridge accepted our ui/initialize", false,
      "a rejected handshake is silent: this is the assertion that catches it"))

  const probe1 = await page.evaluate("window.__probe") as any
  check("AppBridge parsed our appInfo", probe1.appInfo?.name === "agents-board",
    JSON.stringify(probe1.appInfo))
  check("AppBridge parsed our appCapabilities", probe1.appCapabilities !== undefined)
  check("the SDK reported no protocol errors", probe1.errors.length === 0,
    probe1.errors.join("; "))

  // Real Chrome, real layout: this height is measured, not stubbed.
  await page.waitForFunction("window.__probe.sizes.length > 0", null, { timeout: 10000 }).catch(() => {})

  const sizes = (await page.evaluate("window.__probe.sizes")) as number[]
  check("panel reports a real measured height", (sizes.at(-1) ?? 0) > 200,
    `heights=${sizes.join(",")}`)

  // ── deliver the payload through the SDK ──────────────────────────────────
  await page.evaluate((r) => (window as any).__deliver(r), boardResult)
  // ── spaces in the panel ────────────────────────────────────────────────
  {
    await panel.locator('.views button[data-view="agents"]').click()
    const pane = panel.locator("#pane-agents")
    await pane.locator(".space").first().waitFor({ timeout: 8000 })
    const text = (await pane.textContent()) ?? ""
    check("the panel lists the space", text.includes("panel-work"), text.slice(0, 160))
    check("with its topic", text.includes("rendering the panel"), text.slice(0, 160))
    // The panel knows WHICH agent is reading; the operator board does not, and
    // passes selfId: null precisely so it cannot invent one.
    check("the reading agent's own membership is marked",
      (await pane.locator(".member.self").count()) >= 1)
    const peer = pane.locator(".member[data-why]").first()
    const why = (await peer.getAttribute("data-why")) ?? ""
    check("an auto-joined peer carries its score", /0\.6/.test(why), why || "(no explanation)")
    // And a person can actually get to it. The evidence for an automatic join
    // is the answer to "why is this agent in my agent": the question the whole
    // space model has to be able to answer, and it used to be a `title`,
    // which shows nothing on a touch host and nothing to a keyboard.
    await peer.focus()
    // Located from the frame, not the pane: the popover lives in the top layer
    // on <body>, which is the entire reason it is never clipped by the
    // scrolling pane the mark sits in.
    const tip = await panel.locator("#agents-why").evaluate((el) => ({
      open: el.matches(":popover-open"), text: el.textContent ?? "",
    }))
    check("and the evidence is reachable without a mouse",
      tip.open && /0\.6/.test(tip.text), JSON.stringify(tip))
    // An announcement that exhausted its retries and was never acknowledged.
    // Driving that through the real timers would take five 120-second retry
    // windows, so the payload is injected here: this check is about RENDERING,
    // and the state machine itself is pinned in TestAnAbandonedAnnouncementStaysOnTheBoard.
    await page.evaluate((r) => {
      const b = structuredClone(r)
      const ch = b._meta["com.dibs/panel"].board.spaces
        .find((c: any) => c.id === "panel-work")
      ch.abandoned_announcements = 2
      ch.unacked_announcements = 1
      ;(window as any).__deliver(b)
    }, boardResult)
    await pane.locator(".pill.abandoned").first().waitFor({ timeout: 5000 })
    const pills = (await pane.textContent()) ?? ""
    check("an abandoned announcement is drawn, and separately from a pending one",
      /2 unanswered/.test(pills) && /1 awaiting ack/.test(pills), pills.slice(0, 200))
    // Nothing else on the board is filled: this is the one state a person must
    // act on, because nothing will resolve it on its own.
    const filled = await pane.locator(".pill.abandoned").evaluate(
      (el) => getComputedStyle(el).backgroundColor)
    check("and it is the only filled mark on the board",
      filled !== "rgba(0, 0, 0, 0)" && filled !== "transparent", filled)

    // Measure the actual foreground/background pair in both themes. Palette
    // checks against the page background cannot catch a bad token pairing on a
    // filled pill, which is the exact contrast regression this state had.
    const pillContrast = async (theme: "dark" | "light") => {
      await page.evaluate((t) => (window as any).__bridge.sendHostContextChange({ theme: t }), theme)
      await panel.locator("html").evaluate((el, t) => {
        if (el.getAttribute("data-theme") !== t)
          throw new Error(`theme stayed ${el.getAttribute("data-theme")}`)
      }, theme)
      return await pane.locator(".pill.abandoned").evaluate((el) => {
        // Paint each colour and read the pixel back, rather than parsing the
        // computed string. The old version pulled the first three numbers out
        // of `rgb(r, g, b)` with a regex; once the palette moved to OKLCH it
        // read `oklch(0.685 0.165 36)` as an RGB triplet and reported 2.46:1
        // in BOTH themes: a confident, precise, entirely invented number for
        // a pill that renders at 5.3:1. A contrast check must measure light,
        // and the only light here is the pixel.
        const c = document.createElement("canvas")
        c.width = c.height = 1
        const ctx = c.getContext("2d", { willReadFrequently: true })!
        const lum = (value: string) => {
          ctx.clearRect(0, 0, 1, 1)
          ctx.fillStyle = value
          ctx.fillRect(0, 0, 1, 1)
          const [r, g, b] = Array.from(ctx.getImageData(0, 0, 1, 1).data)
            .slice(0, 3)
            .map((n) => {
              const v = n / 255
              return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4)
            })
          return 0.2126 * r + 0.7152 * g + 0.0722 * b
        }
        const s = getComputedStyle(el)
        const a = lum(s.color), b = lum(s.backgroundColor)
        return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
      })
    }
    const darkContrast = await pillContrast("dark")
    const lightContrast = await pillContrast("light")
    check("the abandoned alarm clears AA body contrast in both themes",
      darkContrast >= 4.5 && lightContrast >= 4.5,
      `dark=${darkContrast.toFixed(2)} light=${lightContrast.toFixed(2)}`)
    const nativeScheme = await panel.locator("html").evaluate(
      (el) => getComputedStyle(el).colorScheme)
    check("host theme also reaches native controls",
      nativeScheme === "light", nativeScheme)
    await page.evaluate(() => (window as any).__bridge.sendHostContextChange({ theme: "dark" }))

    // A third announcement state, and the reason it is a third: "still asking"
    // and "asking somebody who is not there" look identical on a board and are
    // not the same problem. Redelivery is driven by the agent POLLING, so an
    // announcement owed only by sleeping or crashed agents never spends its
    // retry budget and never reaches `unanswered`: it waits forever, looking
    // healthy. Pinned in the state machine by
    // TestAnAnnouncementOwedOnlyByAbsenteesSaysSo; this is about RENDERING.
    await page.evaluate((r) => {
      const b = structuredClone(r)
      const ch = b._meta["com.dibs/panel"].board.spaces
        .find((c: any) => c.id === "panel-work")
      ch.unacked_announcements = 1
      ch.blocked_announcements = 1
      ;(window as any).__deliver(b)
    }, boardResult)
    await pane.locator(".pill.blocked").first().waitFor({ timeout: 5000 })
    check("an announcement nobody present can answer is drawn as blocked",
      /1 blocked/.test((await pane.textContent()) ?? ""), (await pane.textContent())?.slice(0, 200))
    // It must NOT borrow the filled treatment. That mark means "nothing will
    // resolve this without a person", and a blocked announcement resolves the
    // moment a sleeping agent wakes.
    const blockedFill = await pane.locator(".pill.blocked").evaluate(
      (el) => getComputedStyle(el).backgroundColor)
    check("and does not claim the weight reserved for 'needs a person'",
      blockedFill === "rgba(0, 0, 0, 0)" || blockedFill === "transparent", blockedFill)

    // Leave the panel where we found it: the checks after this one assert on
    // the roster, which is hidden while another tab is selected.
    await panel.locator('.views button[data-view="board"]').click()
    await pane.waitFor({ state: "hidden", timeout: 5000 })
  }

  await panel.locator(".entry").first().waitFor({ timeout: 10000 })

  const names = await panel.locator(".entry .name").allTextContents()
  check("renders every agent", names.includes("reviewer") && names.includes("peer"), names.join(","))
  check("groups the roster", (await panel.locator(".band").count()) >= 1)
  check("marks the caller's own agent", (await panel.locator(".entry.self").count()) === 1)
  check("renders the declared task",
    (await panel.locator(".task").first().textContent())?.includes("running the panel e2e") ?? false)
  check("renders slot paths", (await panel.locator(".path").allTextContents()).join(",").includes("board_app.html"))
  check("summary agrees with the board",
    ((await panel.locator(".metric .figure").first().textContent()) ?? "").includes("2"))
  const capsText = (await panel.locator("#ctx-caps").textContent()) ?? ""
  check("model-context capability is described without claiming it wakes the agent",
    capsText.includes("can add agent context") && !/wake/i.test(capsText), capsText)
  check("the context line has no orphaned literal separator",
    (await panel.locator(".context > .dot").count()) === 0)
  await page.evaluate(() => (window as any).__bridge.sendHostContextChange({
    styles: { css: { fonts: "/* e2e host font contract */" } },
  }))
  check("host-provided font faces are installed inside the iframe",
    (await panel.locator("#mcp-host-fonts").textContent()) === "/* e2e host font contract */")

  // Re-delivering an unchanged snapshot must not mutate a role=status summary.
  // A screen reader observes DOM mutations, not our intent; rebuilding the same
  // four figures on every host push makes an unchanged board sound busy.
  await panel.locator("#summary").evaluate((el) => {
    ;(window as any).__summaryMutations = 0
    ;(window as any).__summaryObserver = new MutationObserver((records) => {
      ;(window as any).__summaryMutations += records.length
    })
    ;(window as any).__summaryObserver.observe(el, {
      attributes: true, childList: true, subtree: true, characterData: true,
    })
  })
  await page.evaluate((r) => {
    const b = structuredClone(r)
    delete b.view
    if (b._meta?.["com.dibs/panel"]) delete b._meta["com.dibs/panel"].view
    ;(window as any).__deliver(b)
  }, boardResult)
  await Bun.sleep(100)
  const summaryMutations = await panel.locator("body").evaluate(() => {
    ;(window as any).__summaryObserver?.disconnect()
    return (window as any).__summaryMutations
  })
  check("an unchanged push does not re-announce the summary",
    summaryMutations === 0, String(summaryMutations))


  // An agent going out of touch is marked, once, on the way in.
  //
  // This is the board's most consequential silent change: an agent stopped
  // answering and work may be queued behind it, and until now it restyled
  // between two frames, which nobody sees unless they are already looking at
  // that row. The two halves are tested together because the second is what
  // keeps the first honest: marking an agent that was ALREADY stale when we first
  // saw it would spend the operator's attention on history rather than news, and
  // would fire on every push forever.
  {
    const laneOf = (b: any) => b._meta?.["com.dibs/panel"]?.board?.agents ?? []
    const withStatus = (r: any, id: string, status: string) => {
      const b = structuredClone(r)
      for (const l of laneOf(b)) if (l.id === id) l.status = status
      return b
    }
    const target = laneOf(boardResult).find((l: any) => l.status === "active")?.id
    check("the fixture has an active agent to take out of touch", Boolean(target), String(target))
    if (target) {
      // Seen active first, so the next push is a real transition.
      await page.evaluate((r) => (window as any).__deliver(r), withStatus(boardResult, target, "active"))
      await Bun.sleep(80)
      await page.evaluate((r) => (window as any).__deliver(r), withStatus(boardResult, target, "stale"))
      await Bun.sleep(80)
      const marked = await panel.locator(`.entry[data-agent="${target}"]`).first()
        .evaluate((el) => el.classList.contains("went-quiet"))
      check("an agent that goes out of touch is marked", marked,
        "the transition happened with no signal at all")

      // The gesture must be on the pseudo-element that RENDERS.
      //
      // This check previously read animationName off ::before and passed for a
      // week while nothing moved: the rail marker is ::after, ::before has
      // `content: none` and never paints, and getComputedStyle happily reports
      // the animation from a rule attached to a box that does not exist. So the
      // content is asserted first: a named animation on an unrendered
      // pseudo-element is precisely the shape of the bug this now catches.
      const gesture = await panel.locator(`.entry[data-agent="${target}"]`).first()
        .evaluate((el) => {
          const pick = (which: string) => {
            const cs = getComputedStyle(el, which)
            return { content: cs.content, animation: cs.animationName }
          }
          return { before: pick("::before"), after: pick("::after") }
        })
      const painted = Object.values(gesture)
        .filter((g) => g.content !== "none" && g.content !== "")
      check("the mark animates a pseudo-element that actually renders",
        painted.some((g) => g.animation === "panel-went-quiet"),
        `rendered pseudo-elements carry ${JSON.stringify(painted.map((g) => g.animation))}; ` +
        `full state ${JSON.stringify(gesture)}`)
      const ghost = Object.entries(gesture)
        .filter(([, g]) => g.animation === "panel-went-quiet" &&
          (g.content === "none" || g.content === ""))
      check("no went-quiet animation is attached to a pseudo-element that never paints",
        ghost.length === 0, `${JSON.stringify(ghost)}`)

      // A band change also runs a view transition, so the row TRAVELS to its new
      // group instead of vanishing here and reappearing there. The transition
      // kind is stamped on <html> for the duration, which is what the CSS keys
      // off, so that is the honest thing to observe, and it must be the
      // band kind specifically, not whatever the previous push left behind.
      // Put the agent in a KNOWN band first, and let it settle.
      //
      // This measurement used to rely on whatever the preceding blocks happened
      // to leave behind, and failed about three runs in four: if the agent was
      // already active, delivering active again is not a band change, so no
      // transition fires and the check reports an empty list. A view transition
      // in flight from an earlier push can also swallow the next one.
      //
      // Establishing the starting band here makes the check independent of
      // everything above it. The settle is generous because the transition it is
      // about runs for .62s, and measuring the next one before this one finishes
      // is what made the result depend on scheduling.
      await page.evaluate((r) => (window as any).__deliver(r),
        withStatus(boardResult, target, "stale"))
      await Bun.sleep(900)

      // Time it as well as name it.
      //
      // Observing that a transition STARTED says nothing about whether anything
      // was visible. Forcing ::view-transition-group(*) to .001ms left every
      // other assertion here green while the row teleported: the motion was
      // gone and the checks could not tell. So the interval the marker is
      // present for is measured, which is the interval the browser is animating.
      const travel = await page.evaluate(async (r) => {
        const doc = (window as any).__panelDoc()
        const seen: string[] = []
        let started = 0, ended = 0
        const obs = new MutationObserver(() => {
          const k = doc.documentElement.getAttribute("data-transition")
          if (k) {
            if (!seen.includes(k)) seen.push(k)
            if (!started) started = performance.now()
          } else if (started && !ended) {
            ended = performance.now()
          }
        })
        obs.observe(doc.documentElement, { attributes: true, attributeFilter: ["data-transition"] })
        ;(window as any).__deliver(r)
        // Sample what the browser is ACTUALLY running, mid-flight. The marker's
        // lifetime is set by the panel's own script, so timing it measures the
        // script and not the motion: collapsing the CSS duration to .001ms left
        // that measurement completely unchanged. getAnimations() reports the
        // resolved timing of the view-transition pseudo-elements themselves,
        // which is the thing a person would or would not see.
        let longest = 0
        for (let i = 0; i < 40; i++) {
          await new Promise((res) => setTimeout(res, 15))
          for (const a of doc.getAnimations()) {
            // ONLY the view-transition pseudo-elements. A name-based fallback was
            // here and quietly matched panel-went-quiet (1.05s), so the check
            // measured an unrelated animation and passed with the travel
            // collapsed to nothing: the same shape of bug it was added to catch.
            // The LANE group specifically. "any view-transition animation" was
            // still too loose: the pane transitions run at .34s in the same
            // frame, so they satisfied the floor while the agent group itself was
            // collapsed to .001ms. Agent idents are panel-agent-<encoded>.
            const pseudo = String((a.effect as any)?.pseudoElement ?? "")
            if (!pseudo.includes("view-transition") || !pseudo.includes("panel-agent-")) continue
            const d = Number(a.effect?.getTiming?.().duration ?? 0)
            if (d > longest) longest = d
          }
          if (longest) break
        }
        await new Promise((res) => setTimeout(res, 1200))
        obs.disconnect()
        return { seen, ms: started && ended ? ended - started : 0, animMs: longest }
      }, withStatus(boardResult, target, "active"))
      const kindSeen = travel.seen
      check("an agent changing band runs the travel transition",
        kindSeen.includes("agent-band"),
        `transitions seen: ${JSON.stringify(kindSeen)}`)
      // The declared duration is .62s. A generous floor: enough to catch a
      // transition collapsed to nothing, loose enough not to flake on a loaded
      // CI box where the settle callback lands late.
      check("and the travel lasts long enough to be seen",
        travel.animMs >= 250,
        `the longest view-transition animation resolved to ${Math.round(travel.animMs)}ms ` +
        `(marker window ${Math.round(travel.ms)}ms): anything near zero is a teleport ` +
        `wearing a transition's name`)

      // The marker is not the movement.
      //
      // Watching data-transition only proves the panel DECIDED to animate. The
      // browser can only carry a row from one group to the other if that row has
      // a stable view-transition-name across both frames: without one it is part
      // of the root cross-fade and simply blinks to its new position, which is
      // the thing this feature exists to stop. So the name is asserted, and
      // asserted to be the SAME name before and after, which is what makes it a
      // travel rather than two unrelated elements.
      const nameOf = () => panel.locator(`.entry[data-agent="${target}"]`).first()
        .evaluate((el) => getComputedStyle(el).viewTransitionName)
      const nameNow = await nameOf()
      check("the travelling agent carries a view-transition name",
        typeof nameNow === "string" && nameNow !== "none" && nameNow !== "",
        `view-transition-name=${nameNow}`)

      // And it really does change group: the row must end up under a different
      // band heading than it started, or nothing travelled anywhere.
      const bandOf = () => panel.locator(`.entry[data-agent="${target}"]`).first()
        .evaluate((el) => {
          let n: Element | null = el
          while (n) {
            const prev = n.previousElementSibling
            if (prev?.classList.contains("band")) return prev.textContent?.trim() ?? "?"
            n = prev ?? n.parentElement
            if (n && n.classList?.contains("view")) break
          }
          return "?"
        })
      const bandBefore = await bandOf()
      await page.evaluate((r) => (window as any).__deliver(r),
        withStatus(boardResult, target, "stale"))
      await Bun.sleep(300)
      const bandAfter = await bandOf()
      const nameAfter = await nameOf()
      check("the agent actually moves to a different status band",
        bandBefore !== bandAfter && bandBefore !== "?" && bandAfter !== "?",
        `band went ${JSON.stringify(bandBefore)} -> ${JSON.stringify(bandAfter)}`)
      // Equality alone is not enough: with names removed entirely both sides read
      // "none" and the check passed on none === none, which is the same
      // vacuity this whole round is about. The name has to be real AND stable.
      check("and keeps the same transition name across the move, so it is one row travelling",
        nameAfter === nameNow && nameAfter !== "none" && nameAfter !== "",
        `${nameNow} -> ${nameAfter}`)

      // Put the agent back where the next check expects to find it. Measuring the
      // move required driving one, and leaving it driven made the following
      // "coming back" case deliver a state the panel was already in: no change,
      // so no transition, so a real feature reported as broken. A check that
      // mutates shared state owes the next one its starting conditions.
      await page.evaluate((r) => (window as any).__deliver(r),
        withStatus(boardResult, target, "active"))
      await Bun.sleep(200)

      // Waking up travels too. A treatment that only animated the way into
      // trouble would quietly teach that recovery is less worth seeing.
      const backAgain = await page.evaluate(async (r) => {
        const doc = (window as any).__panelDoc()
        const seen: string[] = []
        const obs = new MutationObserver(() => {
          const k = doc.documentElement.getAttribute("data-transition")
          if (k && !seen.includes(k)) seen.push(k)
        })
        obs.observe(doc.documentElement, { attributes: true, attributeFilter: ["data-transition"] })
        ;(window as any).__deliver(r)
        await new Promise((res) => setTimeout(res, 700))
        obs.disconnect()
        return seen
      }, withStatus(boardResult, target, "stale"))
      check("and so does an agent coming back",
        backAgain.includes("agent-band"), `transitions seen: ${JSON.stringify(backAgain)}`)

      // Already stale on arrival is not a change.
      await page.evaluate((r) => (window as any).__deliver(r), withStatus(boardResult, target, "active"))
      await Bun.sleep(80)
      const fresh = structuredClone(boardResult)
      for (const l of laneOf(fresh)) if (l.id === target) l.status = "stale"
      // Drop it from the previous frame entirely, so it ARRIVES stale.
      const without = structuredClone(boardResult)
      if (without._meta?.["com.dibs/panel"]?.board) {
        without._meta["com.dibs/panel"].board.agents =
          laneOf(without).filter((l: any) => l.id !== target)
      }
      await page.evaluate((r) => (window as any).__deliver(r), without)
      await Bun.sleep(80)
      await page.evaluate((r) => (window as any).__deliver(r), fresh)
      await Bun.sleep(80)
      const remarked = await panel.locator(`.entry[data-agent="${target}"]`).first()
        .evaluate((el) => el.classList.contains("went-quiet"))
      check("an agent that arrives already stale is not marked", !remarked,
        "history was announced as news")
    }
  }

  // Real CSS: the live pip must actually be the live colour, not merely classed.
  const pipColour = await panel.locator(".entry.active .pip").first()
    .evaluate((el) => getComputedStyle(el).backgroundColor)
  // WHY an agent stopped counting as live has to survive the panel's field
  // allowlist (trimBoard). It is dropped silently if it does not: a field
  // added to the board simply never reaches the panel, and nothing says so,
  // which is exactly what happened the first three times this was checked.
  {
    await page.evaluate((r) => {
      const b = structuredClone(r)
      const l = b._meta["com.dibs/panel"].board.agents
        .find((x: any) => x.id === "peer")
      l.status = "stale"
      l.stale_reason = "process_exited"
      ;(window as any).__deliver(b)
    }, boardResult)
    const dead = panel.locator(".entry", { has: panel.locator('.name:text-is("peer")') })
    await dead.locator(".tag.why").waitFor({ timeout: 5000 })
    check("a crashed agent's row says WHY it is out of touch",
      (await dead.locator(".tag.why").innerText()).toLowerCase().includes("process gone"),
      await dead.innerText())
    check("and the panel's field allowlist did not drop it",
      (await dead.locator(".tag.why.process_exited").count()) === 1)
    // Put the real board back before the checks that follow.
    await page.evaluate((r) => (window as any).__deliver(r), boardResult)
    await panel.locator(".entry.self").first().waitFor({ timeout: 5000 })
  }

  check("an active agent's pip is rendered in the live colour",
    pipColour !== "rgba(0, 0, 0, 0)" && pipColour !== "",
    pipColour)

  // ── mail ─────────────────────────────────────────────────────────────────
  await panel.locator('.views button[data-view="mail"]').click()
  await panel.locator(".msg").first().waitFor({ timeout: 5000 })
  check("mail lists every message", (await panel.locator(".msg").count()) === 3,
    String(await panel.locator(".msg").count()))
  check("unanswered mail is marked open", (await panel.locator(".msg.open").count()) === 3)

  const actionsFor = async (s: number) =>
    (await panel.locator(`.act[data-serial="${s}"]`).allTextContents()).join(",")
  check("a question offers Answer/Decline", (await actionsFor(mail.q.msg_serial)) === "Answer,Decline",
    await actionsFor(mail.q.msg_serial))
  check("a request offers Approve/Deny", (await actionsFor(mail.r.msg_serial)) === "Approve,Deny",
    await actionsFor(mail.r.msg_serial))
  check("a notify offers Acknowledge", (await actionsFor(mail.n.msg_serial)) === "Acknowledge",
    await actionsFor(mail.n.msg_serial))

  // ── actions, through the SDK, to the ledger ──────────────────────────────
  await panel.locator(`.act[data-serial="${mail.q.msg_serial}"]`, { hasText: "Answer" }).click()
  const box = panel.locator(`#reply-${mail.q.msg_serial}`)
  await box.waitFor({ timeout: 5000 })
  check("Answer opens an inline composer, not a modal", true)

  // The shared operator composer is a horizontal row, while this is a reply
  // card. The selector was narrowed to `.compose .act`, but this surface also
  // uses `.compose`; scoping the name did not scope the surface.
  const sendReply = panel.locator(
    `.act[data-serial="${mail.q.msg_serial}"][data-act="send"]`)
  const replyCraft = await sendReply.evaluate((el) => {
    const s = getComputedStyle(el)
    const textarea = el.closest(".compose")?.querySelector("textarea")
    const ts = textarea ? getComputedStyle(textarea) : null
    return {
      composeDisplay: getComputedStyle(el.closest(".compose")!).display,
      height: el.getBoundingClientRect().height,
      paddingLeft: parseFloat(s.paddingLeft),
      sameFaceAsTextarea: !!ts && s.fontFamily === ts.fontFamily,
      textTransform: s.textTransform,
      position: s.position,
    }
  })
  check("the reply composer remains a vertical card",
    replyCraft.composeDisplay === "block", JSON.stringify(replyCraft))
  check("reply actions honor the panel density and shared face",
    replyCraft.height >= 34 && replyCraft.paddingLeft === 12 &&
      replyCraft.sameFaceAsTextarea && replyCraft.textTransform === "none" &&
      replyCraft.position === "relative",
    JSON.stringify(replyCraft))

  // An empty draft still has a focus position. The old truthiness guard
  // preserved a non-empty value and dropped focus when nothing had been typed.
  await box.focus()
  await page.evaluate((r) => {
    const b = structuredClone(r)
    delete b.view
    if (b._meta?.["com.dibs/panel"]) delete b._meta["com.dibs/panel"].view
    ;(window as any).__deliver(b)
  }, boardResult)
  check("a redraw preserves focus in an empty reply",
    (await box.inputValue()) === "" &&
      (await box.evaluate((el) => document.activeElement === el)))

  // Reproduce the forced-colours path in the browser. The compose-specific
  // outline used to beat the system Highlight rule and shrink this to 1px.
  await page.emulateMedia({ forcedColors: "active" })
  await box.focus()
  const forcedRing = await box.evaluate((el) => {
    const s = getComputedStyle(el)
    return { style: s.outlineStyle, width: parseFloat(s.outlineWidth) }
  })
  check("the shared focus ring remains authoritative in forced colours",
    forcedRing.style !== "none" && forcedRing.width >= 2, JSON.stringify(forcedRing))
  await page.emulateMedia({ forcedColors: "none" })

  // Exercise the real narrow iframe, not merely a narrow outer page.
  await page.locator("#panel").evaluate(
    (el: HTMLIFrameElement) => { el.style.width = "390px" })
  await Bun.sleep(100)
  const narrow = await panel.locator("html").evaluate((el) => ({
    client: el.clientWidth, scroll: el.scrollWidth,
  }))
  check("the open reply does not overflow a 390px host container",
    narrow.scroll <= narrow.client, JSON.stringify(narrow))
  await page.locator("#panel").evaluate(
    (el: HTMLIFrameElement) => { el.style.width = "900px" })

  await box.fill("Yes: it renders.")
  // A redraw must not eat a reply that is half-written.
  //
  // The host pushes a board update whenever this agent does anything, and every
  // push calls draw(), which replaces the mail pane with innerHTML: destroying
  // this textarea, its contents, the focus and the caret. The same defect on
  // the web board wiped a human's message mid-sentence.
  await box.focus()
  await page.evaluate((r) => {
    // Strip `view`: the host uses it to switch tabs, and switching away from
    // mail would hide the pane this check is about. A real background push
    // carries the same board data without asking for a tab change.
    const b = structuredClone(r)
    delete b.view
    if (b._meta?.["com.dibs/panel"]) delete b._meta["com.dibs/panel"].view
    ;(window as any).__deliver(b)
  }, boardResult)
  check("a redraw does not eat a half-written reply",
    (await box.inputValue()) === "Yes: it renders.", await box.inputValue())
  // And the keyboard path must survive it too. The handler is an element
  // property, so the NEW textarea has none. ⌘+↵ silently stops working while
  // the panel goes on printing "⌘+↵ to send" underneath it. The send below is
  // the assertion: it only passes if the redraw re-armed the handler.
  // Evaluated in the PANEL frame, not the host page: the panel is an iframe,
  // and the host document has no textarea to find.
  const armed = await box.evaluate((el: any) => typeof el.onkeydown)
  check("and the ⌘+↵ hint it prints is still true afterwards",
    armed === "function", String(armed))
  // The keyboard path is the one a person actually uses.
  await box.press("Meta+Enter")
  await Bun.sleep(900)

  await panel.locator(`.act[data-serial="${mail.r.msg_serial}"]`, { hasText: "Approve" }).click()
  await Bun.sleep(700)
  await panel.locator(`.act[data-serial="${mail.n.msg_serial}"]`, { hasText: "Acknowledge" }).click()
  await Bun.sleep(700)

  const stateOf = async (s: number) =>
    textOf(await tool("read_mail", { token: me.token, msg_serial: s })).message
  const answered = await stateOf(mail.q.msg_serial)

  // A redraw must not eat a reply that is half-written. The host pushes a board
  // update whenever this agent does anything, and every push calls draw(),
  // which replaces the mail pane with innerHTML. The same defect on the web
  // board wiped a human's message mid-sentence.

  check("⌘+Enter sent the answer to the ledger", answered.state === "answered", answered.state)
  check("the answer text survived the whole path",
    answered.response === "Yes: it renders.", String(answered.response))
  check("Approve reached the ledger", (await stateOf(mail.r.msg_serial)).state === "approved")
  check("Acknowledge reached the ledger", (await stateOf(mail.n.msg_serial)).state === "acked")
  await panel.locator("#pane-mail .empty").waitFor({ timeout: 5000 }).catch(() => {})
  check("the final action refreshes to an empty mailbox without a second panel payload",
    (await panel.locator(".msg").count()) === 0 &&
      ((await panel.locator("#pane-mail").textContent()) ?? "").includes("Nothing waiting"),
    (await panel.locator("#pane-mail").textContent())?.slice(0, 500) ?? "")

  // ── settled mail shows its disposition ───────────────────────────────────
  // `response` is a STRING and the verdict lives in `state`; reading .body off
  // the string rendered an empty block for every answered message once.
  await page.evaluate(() => (window as any).__deliver({
    structuredContent: { view: "mail", inbox: { messages: [
      { serial: 91, type: "question", from: "peer", to: "reviewer",
        body: "settled?", response: "the stored answer", state: "answered" },
      { serial: 92, type: "request", from: "peer", to: "reviewer",
        body: "refused?", response: null, state: "denied" },
    ] } },
  }))
  await panel.locator(".msg .verdict").first().waitFor({ timeout: 5000 })
  const verdicts = await panel.locator(".msg .verdict").allTextContents()
  check("an answered message shows its verdict", verdicts.includes("Answered"), verdicts.join(","))
  check("a denied message shows its verdict", verdicts.includes("Denied"), verdicts.join(","))
  check("the stored answer is rendered",
    (await panel.locator(".msg .reply").first().textContent())?.includes("the stored answer") ?? false)
  check("a settled message offers no actions", (await panel.locator(".act").count()) === 0)

  // ── honest failures ─────────────────────────────────────────────────────
  // Tool refusals are MCP isError RESULTS, not rejected JSON-RPC calls.
  await page.evaluate((r) => (window as any).__deliver({
    structuredContent: {
      view: "mail",
      lane_id: r._meta["com.dibs/panel"].lane_id,
      act_token: r._meta["com.dibs/panel"].act_token,
      inbox: { messages: [{
        serial: 999999, type: "request", from: "peer",
        to: r._meta["com.dibs/panel"].lane_id,
        body: "This serial does not exist.", state: "open",
      }] },
    },
  }), boardResult)
  await panel.locator('.act[data-serial="999999"][data-act="approve"]').click()
  const refusal = panel.locator('.msg[data-serial="999999"] .action-error')
  await refusal.waitFor({ timeout: 5000 })
  const refusalText = (await refusal.textContent()) ?? ""
  check("an MCP tool refusal is shown as a refusal, never as sent",
    /refused/i.test(refusalText) && /nothing changed/i.test(refusalText),
    refusalText)
  check("the refused action remains available to correct and retry",
    (await panel.locator('.act[data-serial="999999"][data-act="approve"]').count()) === 1)

  // A mutation may succeed and the follow-up inbox refresh may fail. The action
  // is still sent; calling that a send failure invites a duplicate.
  const refreshMail = textOf(await tool("send", {
    token: peer.token, to: me.lane_id, type: "request",
    body: "Refresh-failure probe", op_id: "e2e-refresh-failure", deadline_s: 600,
  }))
  const refreshedInbox = await tool("inbox", { token: me.token })
  await page.evaluate((r) => (window as any).__deliver(r), refreshedInbox)
  let blockedRefreshes = 0
  await page.route("**/rpc", async (route) => {
    const request = route.request().postDataJSON() as any
    if (request?.method === "tools/call" && request?.params?.name === "inbox") {
      blockedRefreshes++
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ error: "injected inbox refresh failure" }),
      })
      return
    }
    await route.continue()
  })
  await panel.locator(
    `.act[data-serial="${refreshMail.msg_serial}"][data-act="approve"]`).click()
  const staleStatus = panel.locator(
    `.msg[data-serial="${refreshMail.msg_serial}"] .action-status.stale`)
  await staleStatus.waitFor({ timeout: 5000 })
  const staleText = (await staleStatus.textContent()) ?? ""
  check("a successful action stays sent when only mailbox refresh fails",
    /sent/i.test(staleText) && /could not refresh/i.test(staleText), staleText)
  check("the stale view cannot submit the confirmed action twice",
    (await panel.locator(`.act[data-serial="${refreshMail.msg_serial}"]`).count()) === 0)
  check("the action really reached the ledger before refresh failed",
    (await stateOf(refreshMail.msg_serial)).state === "approved")
  check("the refresh failure was actually injected", blockedRefreshes === 1,
    String(blockedRefreshes))
  await page.unroute("**/rpc")
  const healedInbox = await tool("inbox", { token: me.token })
  const healedPlain = textOf(healedInbox)
  await page.evaluate((messages) => (window as any).__deliver({
    structuredContent: { inbox: { messages } },
  }), healedPlain.messages)
  const staleAfterPush = await panel.locator(
    `.msg[data-serial="${refreshMail.msg_serial}"] .action-status`)
  check("the next authoritative inbox push clears the stale refresh notice",
    (await staleAfterPush.count()) === 0,
    (await panel.locator("#pane-mail").textContent())?.slice(0, 400) ?? "")

  // ── model context ────────────────────────────────────────────────────────
  const probe2 = (await page.evaluate("window.__probe")) as any
  check("the host saw the panel's tool calls", probe2.toolCalls.length >= 3,
    `${probe2.toolCalls.length} calls`)
  // Every one of them carries the panel marker, THROUGH the real bridge.
  //
  // The marker is what lets check_in stop duplicating its checkpoint into
  // structuredContent, and it is only worth anything if it survives the trip:
  // panel → postMessage → AppBridge → host. A flag the panel sets and the SDK
  // or the host strips would leave the duplicate on forever, and the only
  // symptom would be a bill nobody reads. These are calls the panel made on its
  // own during this run, not ones the test injected.
  check("every panel tool call carries the panel marker across the bridge",
    probe2.toolCalls.length > 0 &&
      probe2.toolCalls.every((c: any) => c?._meta?.["com.dibs/panel-call"] === true),
    `_meta on each: ${JSON.stringify(probe2.toolCalls.map((c: any) => c?._meta ?? null))}`)
  check("offers unread mail to the agent's context", probe2.modelContexts.length > 0)
  check("model context is framed as data, not instruction",
    JSON.stringify(probe2.modelContexts).includes("may act on it or decline"))
  check("model context names the future-turn limit instead of claiming a wake-up",
    JSON.stringify(probe2.modelContexts).includes("future turn") &&
      !/wake/i.test(JSON.stringify(probe2.modelContexts)))

  // Two identical pushes while the host is still accepting the first context
  // update must produce one request, not two concurrent copies.
  const contextsBefore = await page.evaluate(
    () => (window as any).__probe.modelContexts.length) as number
  await page.evaluate(() => {
    const probe = (window as any).__probe
    ;(window as any).__bridge.onupdatemodelcontext = async (req: any) => {
      probe.modelContexts.push(req)
      await new Promise((resolve) => setTimeout(resolve, 180))
      return {}
    }
    const payload = {
      structuredContent: {
        lane_id: "reviewer",
        inbox: { messages: [{
          serial: 700001, type: "notify", from: "peer", to: "reviewer",
          body: "one context update", state: "open",
        }] },
      },
    }
    ;(window as any).__deliver(payload)
    ;(window as any).__deliver(payload)
  })
  await Bun.sleep(350)
  const contextsAfter = await page.evaluate(
    () => (window as any).__probe.modelContexts.length) as number
  check("concurrent identical mail pushes share model context once",
    contextsAfter - contextsBefore === 1, `${contextsBefore} -> ${contextsAfter}`)

  // The host can tear the resource down cleanly; this also exercises pending
  // timer cleanup in the hand-written transport.
  const teardown = await page.evaluate(
    () => (window as any).__bridge.teardownResource({}))
  check("resource teardown is acknowledged", !!teardown && typeof teardown === "object",
    JSON.stringify(teardown))

  // ── hosts that withhold a carrier ────────────────────────────────────────
  //
  // This is the failure that shipped. Panel data travels in tool-result _meta,
  // which is where the spec puts it and which keeps the board out of model
  // context. A host that forwards none of it left the panel reading "awaiting
  // board · No agents yet" while the daemon held a full board, and nothing
  // failed anywhere: every assertion above passed, `content` carried a correct
  // summary throughout, and the only way to see it was to look at the panel.
  //
  // Both checks reload first, because they are about a panel that has NOTHING.
  // Asserting against the panel built up above would pass on state it already
  // held and prove nothing, which is the exact shape of the original mistake.
  // Last in the file so the reload disturbs no earlier state.
  const freshPanel = async () => {
    await page.reload({ waitUntil: "load" })
    await page.waitForFunction("window.__ready === true", null, { timeout: 15000 })
    return page.frameLocator("#panel")
  }

  // Route one: the host proxies a tool call, so the panel fetches for itself.
  {
    const fresh = await freshPanel()
    await page.evaluate((r) => (window as any).__deliver({
      content: r.content, structuredContent: r.structuredContent,
    }), boardResult)
    const filled = await fresh.locator("#pane-board .entry").first()
      .waitFor({ timeout: 10000 }).then(() => true).catch(() => false)
    const asked = (await page.evaluate(() => (window as any).__probe.toolCalls
      .some((c: any) => c?.name === "board"))) as boolean
    check("a host that drops _meta still fills the panel: it fetches the board",
      filled && asked, `filled=${filled} asked=${asked}`)
  }

  // Route two: no proxy needed at all. check_in already puts the board and
  // mailbox in ordinary content for the AGENT, so the state the panel is
  // waiting for has usually arrived in the one field every host forwards.
  {
    const fresh = await freshPanel()
    const ack = await tool("check_in", { token: me.token })
    await page.evaluate((r) => (window as any).__deliver({ content: r.content }), ack)
    const filled = await fresh.locator("#pane-board .entry").first()
      .waitFor({ timeout: 10000 }).then(() => true).catch(() => false)
    check("a result carrying the board in content fills the panel with no _meta at all",
      filled, (await fresh.locator("#pane-board").textContent())?.slice(0, 200) ?? "")
  }

  check("no uncaught errors in the page", consoleErrors.length === 0, consoleErrors.slice(0, 2).join(" | "))
} catch (err) {
  failures++
  console.log("  \x1b[31m✗\x1b[0m threw:", err)
}

console.log(`\n${checks - failures}/${checks} checks passed`)
process.exit(failures ? 1 : 0)
