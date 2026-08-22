/**
 * End-to-end test for the web board.
 *
 * The web board is gated behind an admin password, which is why it went
 * unverified for a long stretch: checking it appeared to need the operator's
 * own secret. It does not. A scratch data directory with a password this test
 * sets itself exercises the whole path: set-password, the single-use link,
 * the redirect that trades it for a session cookie, and the rendered board,
 * without touching a real board or knowing anything private.
 *
 * It also guards the shared font contract from the other side. Both surfaces
 * inline their faces from internal/assets; when the vendored serif was removed
 * nothing would have caught the web board silently falling back to a system
 * face, because nothing here was automated.
 *
 * Run: task test:web   (or: bun internal/mcp/e2e/web_e2e.ts)
 */
// node:fs and node:os only for mkdtemp/rm/tmpdir, which Bun has no native
// equivalent for. These specifiers are the cross-runtime standard and Bun
// implements them natively: they do not pull in Node.
import { chromium, type Browser } from "playwright"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const ADDR = `127.0.0.1:${process.env.PORT ?? 4932}`
const PASSWORD = "e2e-scratch-password"
const sleep = (ms: number) => Bun.sleep(ms)

let failures = 0
let checks = 0
function check(name: string, cond: boolean, detail = "") {
  checks++
  if (cond) console.log(`  [32m✓[0m ${name}`)
  else { failures++; console.log(`  [31m✗[0m ${name}${detail ? ". " + detail : ""}`) }
}

const dir = mkdtempSync(join(tmpdir(), "agents-web-e2e-"))
const home = process.env.HOME
const dibd = process.env.DIBD ?? `${home}/.local/bin/dibd`
const agents = process.env.DIBS ?? `${home}/.local/bin/dibs`
const daemon = Bun.spawn({
  cmd: [dibd, "-dir", dir, "-addr", ADDR],
  stdout: "ignore", stderr: "ignore",
})
let browser: Browser | undefined
const cleanup = () => {
  try { browser?.close() } catch {}
  daemon.kill()
  try { rmSync(dir, { recursive: true, force: true }) } catch {}
}
process.on("exit", cleanup)

// DIBS_ADMIN=1 is the documented escape from the interactive-terminal gate;
// the password itself is piped, because readPassword reads stdin byte by byte
// rather than requiring a tty.
const cli = (args: string[], input: string) => {
  const r = Bun.spawnSync({
    cmd: [agents, ...args],
    stdin: new TextEncoder().encode(input),
    env: { ...process.env, DIBS_DIR: dir, DIBS_ADDR: ADDR, DIBS_ADMIN: "1" },
  })
  return r.stdout.toString() + r.stderr.toString()
}

let secret = ""
let rpcId = 0
const tool = async (name: string, args: unknown) => {
  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call",
                           params: { name, arguments: args } }),
  })
  const body = await res.json()
  if (body.error) throw new Error(name + ": " + JSON.stringify(body.error))
  return JSON.parse(body.result.content[0].text)
}

/**
 * board is the one tool whose `content` is a prose summary for the model
 * and whose panel data lives in tool-result metadata: the MCP Apps private
 * backchannel. Parsing content[0].text as JSON fails on
 * "Dibs board: 3 agent(s)…".
 */
const boardOf = async (token: string) => {
  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call",
                           params: { name: "board", arguments: { token } } }),
  })
  const body = await res.json()
  if (body.error) throw new Error("board: " + JSON.stringify(body.error))
  return body.result?._meta?.["com.dibs/panel"] ?? {}
}

/**
 * Wait for an EFFECT to appear on the board, rather than for a UI toast.
 *
 * The board's flash message clears itself after six seconds, so any assertion
 * that waits for it is racing a timer: measured at roughly one failure in
 * three. A flaky gate is worse than a failing one: it teaches you to re-run
 * instead of to look.
 *
 * The budget is a CEILING, not a delay: the poll returns the moment the event
 * lands, so a large number costs nothing when things work.
 *
 * It was raised to 20s after a `task ci` failure that looked like load. It was
 * not: the post genuinely never happened, because a redraw landed between
 * filling the composer and clicking post and replaced both. Raising a timeout
 * was the wrong answer to a real bug, and the only reason it was found is that
 * 20s is far too long for an event that should be instant: a number that large
 * timing out is evidence of a fault, not of a busy machine.
 */
async function expectEvent(token: string, type: string, match: (e: any) => boolean, budgetMs = 20000) {
  const deadline = Date.now() + budgetMs
  while (Date.now() < deadline) {
    const evs = await tool("events_since", { token, since_serial: 0 })
    if ((evs.events ?? []).some((e: any) => e.type === type && match(e))) return
    await Bun.sleep(200)
  }
  throw new Error(`no ${type} event matched within ${budgetMs}ms`)
}

try {
  for (let i = 0; ; i++) {
    try { await fetch(`http://${ADDR}/`); break } catch {
      if (i > 60) throw new Error("daemon never came up")
      await sleep(200)
    }
  }
  secret = (await Bun.file(join(dir, "local.secret")).text()).trim()

  // ── seed something worth drawing ─────────────────────────────────────────
  const a = await tool("register", { name: "builder", description: "on the web board", session_id: "w1" })
  await tool("check_in", { token: a.token })
  await tool("declare", { token: a.token, text: "rendering the web board", dirs: ["internal/web"] })
  const b = await tool("register", { name: "checker", description: "peer", session_id: "w2" })
  await tool("check_in", { token: b.token })
  await tool("send", { token: b.token, to: a.agent_id, type: "question",
    body: "Does the web board render?", op_id: "w-q", deadline_s: 600 })
  // A message WITH an attachment, because the server has always carried them
  // and neither human surface drew them: a message saying "see the attached
  // evidence" rendered identically to one with nothing attached, and an
  // operator could not learn that a blob existed, let alone its handle.
  const blob = await tool("put_blob", { token: b.token,
    data: Buffer.from("evidence for the web board").toString("base64"),
    mime: "text/plain" })
  await tool("send", { token: b.token, to: a.agent_id, type: "notify",
    body: "Evidence attached.", op_id: "w-att",
    attachments: [{ blob: blob.blob ?? blob.id }] })
  // Two spaces, because one cannot hold every state worth drawing: an
  // exclusive space QUEUES a second agent rather than admitting it, so an agent
  // with a scored member and an agent with a queue have to be different agents.
  await tool("open_space", { token: a.token, space: "web-render", topic: "drawing the operator board" })
  await tool("join_space", { token: b.token, space: "web-render", score: 0.71, threshold: 0.33,
    scorer_id: "lexical+cochange", evidence: ["internal/web/web.go"], auto: true })
  await tool("open_space", { token: a.token, space: "web-locked", topic: "single-writer work", exclusive: true })
  await tool("join_space", { token: b.token, space: "web-locked", score: 0.42 })
  // A subagent and a coordinator, so the board has the two facts that change
  // how a row should be read.
  const sub = await tool("register", { name: "helper", session_id: "w3", parent: a.agent_id })
  await tool("check_in", { token: sub.token })

  // An agent that CRASHED, so the board has to draw the state a fleet actually
  // spends its time in. The board used to render this identically to a working
  // agent. "out of touch" beside a last-contact time of "now", which reads as
  // a broken board rather than a dead agent, and its agent listed it as a plain
  // member, so "who is on this agent" was answered wrongly.
  //
  // A real death: a process that existed and exited, detected by the sweep's
  // pid probe rather than asserted here.
  const doomed = Bun.spawn({ cmd: ["sleep", "300"], stdout: "ignore", stderr: "ignore" })
  const ghost = await tool("register", {
    name: "ghost", description: "was refactoring auth", session_id: "w4", pid: doomed.pid,
  })
  await tool("check_in", { token: ghost.token })
  await tool("join_space", { token: ghost.token, space: "web-render", score: 0.55 })
  doomed.kill()
  await doomed.exited


  // ── the admin path, end to end ───────────────────────────────────────────
  const setOut = cli(["admin", "set-password"], `${PASSWORD}\n${PASSWORD}\n`)
  check("admin password can be set non-interactively", setOut.includes("admin password set"))
  // Only possible AFTER the password exists: promotion travels the human's
  // path, which is the whole point of the role.
  const grantOut = cli(["admin", "coordinator", b.agent_id], `${PASSWORD}\n`)
  check("a coordinator can be promoted from the CLI",
    !/error/i.test(grantOut), grantOut.trim().slice(0, 120))

  const linkOut = cli(["web"], `${PASSWORD}\n`)
  const link = (linkOut.match(/http:\/\/\S+/) || [])[0]
  check("dibs web mints a link", !!link, linkOut.split("\n")[0])
  check("the secret is not in the URL", !!link && !link.includes(PASSWORD))

  // The link is single-use and trades itself for a session cookie via a 303.
  const first = await fetch(link, { redirect: "manual" })
  check("the link redirects rather than serving directly", first.status === 303,
    String(first.status))
  const cookie = (first.headers.get("set-cookie") || "").split(";")[0]
  check("it sets a session cookie", cookie.length > 0)

  // The cookie is only half of it. The other half rides in the redirect's
  // FRAGMENT, which a browser never sends to a server: that is what makes it
  // unreachable by anything replaying the cookie, and cookies are host-scoped
  // so every local port is handed one the moment the operator visits it.
  const loc = first.headers.get("location") || ""
  const pageKey = loc.includes("#k=") ? loc.slice(loc.indexOf("#k=") + 3) : ""
  check("the redirect carries a page key in its fragment", pageKey.length > 0, loc)
  const keyed = { cookie, "X-Dibs-Board-Key": pageKey }

  const served = await fetch(`http://${ADDR}/`, { headers: { cookie } })
  check("the board serves with the cookie", served.status === 200, String(served.status))

  // ── what a stolen cookie can reach ──────────────────────────────────────
  // A second server on 127.0.0.1 receives dibs_session as soon as the operator
  // visits it, and can replay it from outside a browser, where it writes its
  // own headers and can declare any Origin it likes. Everything that matters
  // must refuse it. Round five demanded an Origin header and called this
  // closed; the forged header passed, and the test written with it asserted
  // that the forged request SHOULD pass.
  const forged = { cookie, origin: `http://${ADDR}` }
  for (const path of ["/api/messages", "/api/me"]) {
    const r = await fetch(`http://${ADDR}${path}`, { headers: forged })
    // 401 or 403: which one is the gate's business, and either refuses. What
    // matters here is that it is not 200.
    check(`a replayed cookie cannot read ${path}`,
      r.status === 401 || r.status === 403, String(r.status))
  }
  const roleReplay = await fetch(`http://${ADDR}/api/admin/role`, {
    method: "POST", headers: { ...forged, "content-type": "application/json" },
    body: JSON.stringify({ agent: "nobody", role: "admin" }),
  })
  check("a replayed cookie cannot grant a role",
    roleReplay.status === 401 || roleReplay.status === 403, String(roleReplay.status))
  const withKey = await fetch(`http://${ADDR}/api/me`, { headers: keyed })
  check("the board's own page still reads /api/me", withKey.status === 200,
    String(withKey.status))

  // ── the stream's id must be a resume point ──────────────────────────────
  // A snapshot frame has to advertise the serial it REPRESENTS. The initial
  // frame and the 30-second refresh both sent the connect-time `since`, so on
  // an idle board a client's Last-Event-ID never advanced and on a busy one it
  // was reset backwards every 30 seconds. The board itself was never wrong,
  // every frame is a full snapshot, but the id was useless as the one thing it
  // is for, and a reconnect from a stale point replays into a 256-slot space
  // whose overflow is dropped without a word.
  {
    const firstFrame = async (headers: Record<string, string>) => {
      const ctl = new AbortController()
      const res = await fetch(`http://${ADDR}/events`, { headers, signal: ctl.signal })
      const reader = res.body!.getReader()
      let buf = ""
      const dec = new TextDecoder()
      while (!buf.includes("\n\n")) {
        const { value, done } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })
      }
      ctl.abort()
      const id = /^id: (\d+)/m.exec(buf)?.[1]
      const data = /^data: (.*)$/m.exec(buf)?.[1]
      return { id: Number(id), payload: data ? JSON.parse(data) : null }
    }
    const f1 = await firstFrame({ cookie })
    check("the stream's first frame carries the board's own serial",
      f1.id > 0 && f1.id === f1.payload?.board?.serial,
      `id=${f1.id} board.serial=${f1.payload?.board?.serial}`)

    // Something happens while the client is away, then it resumes from that id.
    await tool("declare", { token: a.token, text: "changed while disconnected" })
    const f2 = await firstFrame({ cookie, "Last-Event-ID": String(f1.id) })
    check("resuming from that id advances rather than repeating it",
      f2.id > f1.id, `${f1.id} -> ${f2.id}`)
    const slots = (f2.payload?.board?.agents ?? []).flatMap((l: any) => l.slots ?? [])
    check("and the resumed snapshot already contains what was missed",
      slots.some((sl: any) => (sl.text ?? "").includes("changed while disconnected")),
      JSON.stringify(slots).slice(0, 160))
  }
  const html = await served.text()

  check("board renders the agents", html.includes("builder") && html.includes("checker"))
  check("board renders the declared work", html.includes("rendering the web board"))
  // THE DOCUMENT MUST NOT CARRY MAIL, and this check used to require that it
  // did. The document is one of two routes a session cookie opens by itself,
  // because EventSource cannot send a header and cookies are host-scoped, so
  // embedding message bodies handed the whole mailbox to anything replaying the
  // cookie. This suite pinned that in place. Found by the pre-release review.
  check("the document carries no message body",
    !html.includes("Does the web board render?"))
  // It arrives over the keyed route instead, which is what the page does.
  const mail = await (await fetch(`http://${ADDR}/api/messages`, { headers: keyed })).json() as any
  check("the keyed route carries the message",
    JSON.stringify(mail.messages ?? []).includes("Does the web board render?"),
    JSON.stringify(mail.messages ?? []).slice(0, 200))
  // And the stream a bare cookie can open carries none of it either.
  {
    const ctl = new AbortController()
    const res = await fetch(`http://${ADDR}/events`, { headers: { cookie }, signal: ctl.signal })
    const reader = res.body!.getReader()
    const dec = new TextDecoder()
    let buf = ""
    while (!buf.includes("\n\n")) {
      const { value, done } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
    }
    ctl.abort()
    check("the event stream carries no message body",
      !buf.includes("Does the web board render?"), buf.slice(0, 200))
  }

  // The shared font contract, from the web side.
  check("fonts are inlined", html.includes("data:font/woff2;base64,"))
  check("no external font origin", !html.includes("https://fonts."))
  check("no dangling reference to the removed serif", !html.includes("Newsreader"))
  check("names only the vendored families", html.includes("Geist"))

  // Without the cookie it must not serve the board.
  const bare = await fetch(`http://${ADDR}/`)
  check("the board is closed without a session", bare.status !== 200 || !(await bare.text()).includes("builder"),
    String(bare.status))

  // A single-use link must not be reusable.
  const replay = await fetch(link, { redirect: "manual" })
  check("the link cannot be replayed", replay.status !== 303, String(replay.status))

  // ── it has to RENDER, not merely contain the data ────────────────────────
  //
  // The assertions above are satisfied by the JSON bootstrap being present in
  // the source, which is a much weaker claim than the board working. Since the
  // web board now renders client-side with the shared components, only a real
  // browser can tell the difference.
  const space = process.env.PW_CHANNEL === undefined ? "chrome" : process.env.PW_CHANNEL
  browser = await chromium.launch({ ...(space ? { space } : {}), headless: true })
  const ctx = await browser.newContext()
  const [name, value] = cookie.split("=")
  await ctx.addCookies([{ name, value, domain: "127.0.0.1", path: "/" }])
  // The browser normally takes this out of the fragment on the redemption
  // redirect. This context is handed the cookie directly and never makes that
  // trip, so it is seeded here, before any page script runs.
  await ctx.addInitScript((k: string) => {
    try { localStorage.setItem("dibs_board_key", k) } catch {}
  }, pageKey)
  const page = await ctx.newPage()
  const pageErrors: string[] = []
  page.on("pageerror", (e) => pageErrors.push(String(e)))
  page.on("console", (m) => { if (m.type() === "error") pageErrors.push(m.text()) })
  // One test deliberately aborts a request to prove the transport-failure path.
  // Chrome logs that abort as a console error, which is Chrome being correct,
  // so the injected one is named and excluded, rather than the whole assertion
  // being loosened to /net::/ and quietly permitting real network faults.
  const injected = (e: string) => /net::ERR_FAILED/.test(e)
  const realErrors = () => pageErrors.filter((e) => !injected(e))
  await page.goto(`http://${ADDR}/`, { waitUntil: "load" })

  await page.locator(".entry").first().waitFor({ timeout: 10000 })
  const rendered = await page.locator(".entry .name").allTextContents()
  check("the board renders agents in a real browser",
    rendered.includes("builder") && rendered.includes("checker"), rendered.join(","))
  check("it uses the shared roster grouping", (await page.locator(".band").count()) >= 1)
  // The design system is applied: in WHICHEVER theme the system asked for.
  //
  // This pinned the dark background, which made it a theme test wearing a
  // design-system test's name: the moment the board started honouring
  // prefers-color-scheme (it was dark-only, permanently, while the README
  // promised dark/light) this failed for the right behaviour.
  //
  // What it actually needs to prove is that the stylesheet reached the page at
  // all, so it accepts either palette and rejects the unstyled default.
  //
  // It reads the PAINTED PIXEL, not the computed string. The palette is
  // declared in OKLCH now, so `backgroundColor` serialises as `oklch(...)` and
  // an equality check against `rgb(14, 15, 17)` failed a page that was
  // rendering perfectly: the test was pinning a serialisation format, which is
  // not a promise anybody made. Painting it and reading the pixel back also
  // survives the browser clipping a wide-gamut colour into sRGB, which a string
  // comparison cannot see at all.
  const painted = async () => page.evaluate(() => {
    const c = document.createElement("canvas")
    c.width = c.height = 1
    const ctx = c.getContext("2d", { willReadFrequently: true })!
    ctx.fillStyle = getComputedStyle(document.body).backgroundColor
    ctx.fillRect(0, 0, 1, 1)
    return Array.from(ctx.getImageData(0, 0, 1, 1).data).slice(0, 3)
  })
  const bodyBg = await painted()
  // Deliberately a RANGE and not a value: the assertion is "the sheet reached
  // the page", and pinning an exact triplet made every palette adjustment
  // break a test that does not care about the palette.
  check("the shared design system is applied, in either theme",
    Math.max(...bodyBg) < 40 || Math.min(...bodyBg) > 230, JSON.stringify(bodyBg))

  // And BOTH themes are actually reachable, which is the claim the README makes.
  for (const scheme of ["dark", "light"] as const) {
    await page.emulateMedia({ colorScheme: scheme })
    const bg = await painted()
    check(`the board follows a ${scheme} system preference`,
      scheme === "dark" ? Math.max(...bg) < 40 : Math.min(...bg) > 230, JSON.stringify(bg))
  }
  await page.emulateMedia({ colorScheme: "dark" })

  // A custom property can be perfectly valid where it is DECLARED and lethal
  // where it is used, and nothing warns.
  //
  // light-dark() takes colours. The palette used it for `--grain: .045` (a
  // number) and `--grain-blend: multiply` (a keyword) as well, which parses
  // and stores fine: custom properties hold token streams. The damage lands
  // at substitution: the declaration goes invalid at computed-value time and
  // the property silently falls back to its INITIAL value. `opacity:
  // var(--grain)` became 1 and `mix-blend-mode: var(--grain-blend)` became
  // `normal`, so the noise texture that should be a whisper rendered at full
  // strength over the entire board. It was unreadable.
  //
  // Every check in this file passed. So did all 71 in the panel suite. A page
  // can be structurally perfect, correctly labelled, keyboard complete, AA
  // compliant, and completely unusable, because none of that is looking.
  const misused = await page.evaluate(() => {
    const bad: string[] = []
    for (const sheet of Array.from(document.styleSheets)) {
      let rules: CSSRule[]
      try { rules = Array.from(sheet.cssRules) } catch { continue }
      const walk = (rs: CSSRule[]) => {
        for (const r of rs) {
          if ("cssRules" in r) walk(Array.from((r as CSSGroupingRule).cssRules))
          const style = (r as CSSStyleRule).style
          if (!style) continue
          for (const prop of Array.from(style)) {
            const v = style.getPropertyValue(prop)
            if (!v.includes("light-dark(")) continue
            // The guarantee: if a value uses light-dark(), it must be a colour.
            if (!CSS.supports("color", v.trim())) bad.push(`${prop}: ${v.trim()}`)
          }
        }
      }
      walk(rules)
    }
    return bad
  })
  check("no token uses light-dark() for something that is not a colour",
    misused.length === 0, misused.join(" | ") || "none")

  // And the invariant that actually broke, stated as itself: the texture is a
  // texture. Asserted on the painted result, not on the token, because the
  // token was fine and the paint was not.
  const texture = await page.evaluate(() => {
    const el = document.querySelector(".surface")
    if (!el) return null
    // The grain is on the pseudo-element, not the layer that carries it,
    // reading .surface itself gives opacity 1 for a texture that is fine.
    const s = getComputedStyle(el, "::before")
    return { opacity: parseFloat(s.opacity), blend: s.mixBlendMode }
  })
  check("the surface texture stays a whisper",
    !!texture && texture.opacity <= 0.2 && texture.blend !== "normal",
    JSON.stringify(texture))

  // The cadence trace: how much an agent actually did, minute by minute.
  //
  // The board could always say what an agent IS: a state derived from a
  // timeout, and that cannot tell a healthy agent between turns from one that
  // stopped eight minutes ago and still claims to be working.
  const cadence = await page.evaluate(() => {
    const el = document.querySelector(".cadence")
    if (!el) return null
    const bars = [...el.children] as HTMLElement[]
    return {
      bins: bars.length,
      label: el.getAttribute("aria-label") ?? "",
      role: el.getAttribute("role"),
      // How many bins carry real weight, and how tall the tallest is. A strip
      // where every bar is the 1px floor is drawing nothing.
      painted: bars.filter((b) => b.getBoundingClientRect().height > 1.5).length,
      tallest: Math.max(...bars.map((b) => b.getBoundingClientRect().height)),
      box: el.getBoundingClientRect().height,
    }
  })
  check("an agent carries a cadence trace", !!cadence && cadence.bins > 8, JSON.stringify(cadence))
  // Not "the element exists": whether it DREW anything. The first two versions
  // of this rendered every event into the same one-pixel column, so a busy agent
  // and a dead one looked identical. Every check passed; only rendering it and
  // looking caught it.
  check("and it actually draws the work, not a flat line",
    !!cadence && cadence.painted >= 1 && cadence.tallest > cadence.box * 0.5,
    JSON.stringify(cadence))
  // A picture that says nothing to a screen reader is a picture shown to
  // someone with their eyes closed.
  check("and the trace states its own contents",
    !!cadence && cadence.role === "img" && /\d+ actions? in the last 10 minutes/.test(cadence.label),
    cadence?.label ?? "(none)")

  // An agent with no events in the window must draw NOTHING. An empty strip says
  // "measured, and nothing happened", which is a stronger and different claim
  // from "this agent predates the window".
  const silent = await page.evaluate(() => {
    const el = document.createElement("div")
    // @ts-expect-error Board is a page global
    el.innerHTML = Board.cadenceHTML("nobody-here", [{ agent: "someone-else", ts: new Date().toISOString() }])
    return el.innerHTML
  })
  check("an agent with nothing in the window draws nothing at all", silent === "", silent)

  // Binning, with inputs chosen so a broken implementation CANNOT pass.
  //
  // The screen check above cannot test this: the seeded board produced its
  // events inside a few seconds, so they genuinely belong in one bin and a
  // version that dumped everything into the last bin drew exactly the same
  // picture. Verified by injecting that bug and watching the check stay green.
  // Spread events across the window and the two stop agreeing.
  const binned = await page.evaluate(() => {
    const now = Date.now()
    const at = (minsAgo: number) => ({ agent: "L", ts: new Date(now - minsAgo * 60_000).toISOString() })
    const el = document.createElement("div")
    // @ts-expect-error Board is a page global
    el.innerHTML = Board.cadenceHTML("L", [at(9), at(5), at(0.1)], now)
    const bars = [...el.firstElementChild!.children] as HTMLElement[]
    const filled = bars.map((b, i) => (b.getAttribute("style") ? i : -1)).filter((i) => i >= 0)
    return { filled, count: bars.length }
  })
  check("three events nine minutes apart land in three different bins",
    binned.filled.length === 3 && new Set(binned.filled).size === 3,
    JSON.stringify(binned))
  // And in the right ORDER: oldest left, newest right. A strip that reads
  // backwards is worse than no strip, because it is confidently wrong.
  check("and the oldest sits left of the newest",
    binned.filled[0] < binned.filled[2] && binned.filled[2] >= binned.count - 2,
    JSON.stringify(binned))

  // ── keyboard and screen-reader craft ──────────────────────────────────────
  // A coordination board is scanned and driven from the keyboard for hours, and
  // it changes under the reader while they do it. Both of those were unserved:
  // four tabs were four separate tab stops, and nothing announced a change.
  const tabs = page.locator('.views button[role="tab"]')
  await tabs.first().focus()
  check("the tablist is ONE tab stop, not four",
    (await page.locator('.views button[role="tab"][tabindex="-1"]').count()) === 3,
    String(await page.locator('.views button[role="tab"][tabindex="-1"]').count()))

  await page.keyboard.press("ArrowRight")
  check("arrow keys move between tabs, as role=tablist promises",
    (await page.locator('.views button[aria-selected="true"]').getAttribute("data-view")) === "agents",
    String(await page.locator('.views button[aria-selected="true"]').getAttribute("data-view")))
  await page.keyboard.press("End")
  check("End jumps to the last tab",
    (await page.locator('.views button[aria-selected="true"]').getAttribute("data-view")) === "activity",
    String(await page.locator('.views button[aria-selected="true"]').getAttribute("data-view")))
  await page.keyboard.press("Home")

  // Moving Protocol out of the tablist was a semantics fix that left its CSS
  // behind on `.views .proto`, so it rendered as a default blue underlined link
  // beside four styled tabs. Checking the STRUCTURE and not the RESULT is how
  // that shipped.
  const proto = await page.evaluate(() => {
    const el = document.querySelector(".proto") as HTMLElement | null
    if (!el) return null
    const s = getComputedStyle(el)
    return { decoration: s.textDecorationLine, pad: parseFloat(s.paddingLeft), colour: s.color }
  })
  check("the Protocol link is still styled after leaving the tablist",
    !!proto && proto.decoration === "none" && proto.pad > 4, JSON.stringify(proto))

  check("every pane is a labelled tabpanel",
    (await page.locator('section[role="tabpanel"][aria-labelledby]').count()) === 4,
    String(await page.locator('section[role="tabpanel"][aria-labelledby]').count()))

  // The live region exists, is in the accessibility tree, and is silent on first
  // paint: announcing the whole board every frame would be worse than nothing.
  const liveRegion = page.locator("#live")
  check("there is a polite live region for changes",
    (await liveRegion.getAttribute("aria-live")) === "polite",
    String(await liveRegion.getAttribute("aria-live")))
  // "Silent on first paint" is a claim about ONE MOMENT (the first draw) and
  // asserting it later is a race: this suite runs long enough that an agent can
  // genuinely go stale in between, at which point the region is correctly
  // speaking and the check fails for the right behaviour. It did.
  //
  // So ask the page directly whether the first draw announced anything, rather
  // than inferring it from a reading taken some seconds afterwards.
  const spokeOnArrival = await page.evaluate(() => (window as any).__firstDrawAnnounced === true)
  check("nothing is announced on first paint: arriving is not a change",
    spokeOnArrival === false, String(spokeOnArrival))
  check("the live region is for readers, not for the eye",
    await liveRegion.evaluate((el) => getComputedStyle(el).position === "absolute"
      && parseFloat(getComputedStyle(el).width) <= 2))

  // ── a redraw must not eat what somebody is saying ────────────────────────
  //
  // The existing draft check exercised only the space composer, so it passed
  // while the MAIL pane was replaced wholesale on every SSE frame: losing the
  // body, the chosen type, focus and caret of anyone composing a message. On a
  // live board that is every few seconds. A test that covers one composer and
  // is read as covering composers is how that survived.
  await page.locator('.views button[data-view="mail"]').click()
  await page.locator("#msg-to").fill("builder")
  await page.locator("#msg-type").selectOption("request")
  await page.locator("#msg-body").fill("half a thought that must survive")
  await page.locator("#msg-body").focus()

  // Force a real redraw the way the fleet does: a genuine board event.
  await tool("declare", { token: a.token, text: "something new, to force a redraw" })
  await page.waitForTimeout(600)

  check("a redraw preserves the message body",
    (await page.locator("#msg-body").inputValue()) === "half a thought that must survive",
    await page.locator("#msg-body").inputValue())
  check("and the message TYPE that was deliberately chosen",
    (await page.locator("#msg-type").inputValue()) === "request",
    await page.locator("#msg-type").inputValue())
  check("and the recipient",
    (await page.locator("#msg-to").inputValue()) === "builder",
    await page.locator("#msg-to").inputValue())
  check("and leaves focus where the person left it",
    (await page.evaluate(() => document.activeElement?.id)) === "msg-body",
    String(await page.evaluate(() => document.activeElement?.id)))
  await page.locator('.views button[data-view="board"]').click()

  // ── the two things that go wrong on a network ────────────────────────────
  //
  // act() had neither guard. A dropped request became an unhandled rejection,
  // nothing in the interface at all, and the operator learns their announcement
  // never sent by noticing later that nobody answered it. A double click issued
  // it twice.
  await page.locator('.views button[data-view="mail"]').click()
  await page.route("**/api/act/send", (route) => route.abort("failed"))
  await page.locator("#msg-to").fill("builder")
  await page.locator("#msg-body").fill("this one cannot get through")
  await page.locator(".compose.new-msg .act.send").click()
  await page.waitForTimeout(400)
  const flashed = (await page.locator("#flash").textContent()) || ""
  check("a transport failure is reported, and says nothing was sent",
    /could not reach Dibs/.test(flashed) && /nothing was sent/.test(flashed), flashed)
  check("and the draft is still there to retry",
    (await page.locator("#msg-body").inputValue()) === "this one cannot get through",
    await page.locator("#msg-body").inputValue())
  check("no uncaught rejection reached the console",
    !realErrors().some((e) => /unhandled|rejection/i.test(e)), realErrors().join(" | "))
  await page.unroute("**/api/act/send")

  // A redraw must not reopen the duplicate-submit guard.
  //
  // Disabling the control is not enough: the board redraws on every SSE frame
  // and replaces that control with a fresh enabled one mid-request. The guard
  // survived exactly as long as no fleet event arrived. Found by codex.
  let sends = 0
  await page.route("**/api/act/send", async (route) => {
    sends++
    // Held open so a real board event lands underneath, then ABORTED rather
    // than forwarded: what is being proved is how many requests reach the
    // server, and letting one through would register the human as an agent and
    // break the later "watching does not register you" checks. A test must not
    // pay for its evidence with somebody else's state.
    await new Promise((r) => setTimeout(r, 900))
    await route.abort("failed")
  })
  await page.locator("#msg-to").fill("builder")
  await page.locator("#msg-body").fill("must be sent exactly once")
  await page.locator(".compose.new-msg .act.send").click()
  await tool("declare", { token: a.token, text: "a redraw, mid-flight" })
  await page.waitForTimeout(300)
  await page.locator(".compose.new-msg .act.send").click({ force: true })
  await page.waitForTimeout(1200)
  check("a redraw mid-request does not reopen the duplicate-submit guard",
    sends === 1, `${sends} sends reached the server`)
  await page.unroute("**/api/act/send")
  await page.locator('.views button[data-view="board"]').click()

  // ── every mark explains itself to everyone ───────────────────────────────
  //
  // The reason behind each mark: why an agent is stale, what "blocked" means,
  // what an auto-join scored and on what evidence: lived only in `title`.
  // title does not exist on touch, keyboards cannot summon it, and screen-reader
  // support is inconsistent. SPEC-CHANNELS §10.3 asks the board to explain its
  // marks; they were explainable only to somebody holding a mouse.
  await page.locator('.views button[data-view="agents"]').click()
  const explainable = await page.evaluate(() => {
    const out: { mark: string; inTree: boolean }[] = []
    document.querySelectorAll(".pill, .tag.why, .member-tag.gone, .badge").forEach((el) => {
      const t = el.getAttribute("title")
      if (!t) return
      // The reason must ALSO be readable, not only hoverable.
      out.push({ mark: el.className, inTree: !!el.querySelector(".sr-only") })
    })
    return out
  })
  check("every mark that has a reason exposes it to the accessibility tree",
    explainable.every((m) => m.inTree),
    JSON.stringify(explainable.filter((m) => !m.inTree)))

  // The auto-join provenance specifically: score, bar, scorer, evidence.
  const provenance = await page.evaluate(() =>
    [...document.querySelectorAll(".member")]
      .map((el) => el.querySelector(".sr-only")?.textContent || "")
      .filter((t) => /matched/.test(t)))
  if (provenance.length) {
    check("an auto-joined member carries its full provenance in readable text",
      /matched [\d.]+ ≥ [\d.]+ via /.test(provenance[0]), provenance[0])
  }
  await page.locator('.views button[data-view="board"]').click()

  // Motion must not describe something that is not happening. An open message
  // animates a light along the wire ("on its way") and a message whose
  // deadline passed is not on its way. Rendered from a real overdue message
  // rather than by poking CSS, so this proves the whole path.
  const overdueMsg = await page.evaluate(() => {
    const el = document.querySelector(".msg.overdue")
    if (!el) return null
    const wire = el.querySelector(".route .wire")
    return {
      labelled: !!el.querySelector(".pill.attn"),
      // The travelling light is a ::after; content:none removes it entirely.
      still: getComputedStyle(wire!, "::after").content === "none",
    }
  })
  if (overdueMsg) {
    check("an overdue message is labelled rather than left looking live", overdueMsg.labelled)
    check("and it stops animating: motion must not depict progress that stopped",
      overdueMsg.still, JSON.stringify(overdueMsg))
  }

  // The board's job is telling you where to look, so the group that might want
  // a person must come FIRST. It sat third, below two groups that are by
  // definition fine.
  const bandOrder = await page.locator(".band > span:first-child").allTextContents()
  if (bandOrder.includes("Out of touch")) {
    check("the group that needs a person is ranked above the ones that do not",
      bandOrder.indexOf("Out of touch") === 0, bandOrder.join(" | "))
  }

  // The labels must claim only what Dibs knows. It knows an agent has SPOKEN;
  // it does not know what that agent is doing. "Working" was a claim about work
  // applied to every active agent, including ones that had declared nothing, and
  // "Idle" made a dormant standing reviewer: exactly where it belongs between
  // activations: look like a problem.
  check("the roster claims coordination state, not what an agent is doing",
    !bandOrder.includes("Working") && !bandOrder.includes("Idle"),
    bandOrder.join(" | "))

  // And the grouping is structural, not just a coloured divider: a reader who
  // cannot see the rules still gets the groups.
  check("each band is a real heading owning its group",
    (await page.locator('section.band-group[aria-labelledby] > h2.band').count()) === bandOrder.length,
    `${await page.locator('section.band-group > h2.band').count()} of ${bandOrder.length}`)

  // Focus must be VISIBLE, and it must be reached the way a keyboard user
  // reaches it. `:focus-visible` deliberately does NOT match after a mouse
  // interaction, so a programmatic .focus() following a click tests nothing and
  // fails for the wrong reason. Tab is the real path.
  await page.locator("body").click({ position: { x: 2, y: 2 } })
  await page.keyboard.press("Tab")
  const ring = await page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    if (!el || el === document.body) return null
    const s = getComputedStyle(el)
    return { tag: el.tagName, style: s.outlineStyle, width: parseFloat(s.outlineWidth) }
  })
  check("tabbing to a control gives it a real focus ring",
    !!ring && ring.style !== "none" && ring.width >= 1, JSON.stringify(ring))

  // A god view has no "this agent": marking one would be a lie, and the shared
  // component takes selfId as a parameter precisely so this page can pass null.
  check("no agent is marked as the viewer's own",
    (await page.locator(".entry.self").count()) === 0)

  // ── a dead agent must LOOK dead ─────────────────────────────────────────
  // The reason an agent stopped counting as live was computed by the sweep and
  // put only into the `agent.stale` event, so the board showed "out of touch"
  // and nothing else: beside a last-contact time of "now", which reads as a
  // broken board rather than a dead agent. And the three cases are not
  // interchangeable: an exited process is definitive, a lapsed lease may be a
  // long build, and an agent that never gave a pid has said nothing at all.
  {
    const ghostRow = page.locator(".entry", { has: page.locator('.name:text-is("ghost")') })
    await ghostRow.locator(".tag.why").waitFor({ timeout: 10000 })
    check("a crashed agent's row says WHY it is out of touch",
      (await ghostRow.locator(".tag.why").innerText()).toLowerCase().includes("process gone"),
      await ghostRow.innerText())
    check("and distinguishes an exited process from a lapsed lease",
      (await ghostRow.locator(".tag.why.process_exited").count()) === 1)
    // Working agents must carry no such tag, or it is decoration rather than a
    // signal.
    const liveRow = page.locator(".entry", { has: page.locator('.name:text-is("checker")') })
    check("a working agent carries no reason tag",
      (await liveRow.locator(".tag.why").count()) === 0, await liveRow.innerText())
  }

  // ── an agent must not list a corpse as though it were working ─────────────
  // "Who is on this agent" is the question this view exists to answer, and a
  // dead member rendered identically to a live one answers it wrongly. Reading
  // the roster on another tab to find out is not an answer.
  {
    await page.locator('.views button[data-view="agents"]').click()
    const agent = page.locator(".space", { has: page.locator('.space-id:text-is("web-render")') })
    await agent.waitFor({ timeout: 5000 })
    const ghostChip = agent.locator(".member", { hasText: "ghost" })
    check("an agent marks the member that is not working",
      (await ghostChip.locator(".member-tag.gone").count()) === 1, await agent.innerText())
    check("and says what happened to it",
      (await ghostChip.innerText()).toLowerCase().includes("process gone"), await ghostChip.innerText())
    check("while a live member is left unmarked",
      (await agent.locator(".member", { hasText: "checker" }).locator(".member-tag.gone").count()) === 0)
    await page.locator('.views button[data-view="board"]').click()
  }

  await page.locator('.views button[data-view="mail"]').click()
  await page.locator(".msg").first().waitFor({ timeout: 5000 })
  check("mail renders with the shared component", (await page.locator(".msg").count()) >= 1)
  {
    const att = page.locator(".msg .att").first()
    await att.waitFor({ timeout: 5000 })
    const text = await att.innerText()
    check("a message's attachment is visible to the operator", text.length > 0, text)
    check("and says it is a blob", text.toLowerCase().includes("blob"), text)
    // The handle, truncated but matchable against get_blob output. Showing
    // nothing was the bug; showing all 71 characters would bury the message.
    check("and shows the start of the content address", text.includes("sha256:"), text)
    check("and a message without one grows no empty attachment list",
      (await page.locator(".msg .atts").count()) < (await page.locator(".msg").count()),
      `${await page.locator(".msg .atts").count()} lists / ${await page.locator(".msg").count()} messages`)
  }
  // The operator watches; only the panel holds an agent token and may answer.
  // This assertion used to read "the operator view offers no actions", which
  // was true and was the problem: a coordination service whose human can only
  // watch is a monitoring tool. The human is now a PARTICIPANT: an agent
  // identity, not an agent of their own, and everything below drives the real
  // browser through the same ops an agent sends.
  check("the operator can act", (await page.locator(".act").count()) > 0)

  // ── the human joins an agent and speaks in it ─────────────────────────────
  {
    await page.locator('.views button[data-view="agents"]').click()
    await page.locator("#pane-agents .space").first().waitFor({ timeout: 5000 })

    // Watching is not participating. Loading the board must not put an agent on
    // the roster: an operator who has joined nothing owes nobody an ack and
    // should not be counted in the fleet or swept for liveness.
    const before = (await page.locator("#ctx-you").textContent()) ?? ""
    check("merely watching the board does not register you", /observing/.test(before), before)
    // board's `content` is a prose summary for the model; the data lives in
    // structuredContent (the MCP Apps contract), so it cannot go through tool().
    const ids = (((await boardOf(a.token)).board?.agents) ?? []).map((l: any) => l.id)
    check("and no agent appears for the watcher", !ids.includes("ada"), ids.join(","))

    const box = page.locator('.compose[data-agent="web-render"]')
    check("an agent the human has not joined offers a join button",
      (await box.locator(".act.join").count()) === 1)
    check("and will not let them speak until they do",
      await box.locator('input[type="text"]').isDisabled())

    await box.locator(".act.join").click()
    await page.locator('.compose[data-agent="web-render"] .act.leave').waitFor({ timeout: 5000 })
    check("joining flips the control to leave", true)
    // The identity is minted by the first ACTION, not by the page load.
    await page.locator("#ctx-you").filter({ hasText: "you are" }).waitFor({ timeout: 5000 })
    check("acting is what gives you an identity", true)
    check("and unlocks the composer",
      !(await page.locator('.compose[data-agent="web-render"] input[type="text"]').isDisabled()))

    // A redraw must not eat what a person is in the middle of typing.
    //
    // Every board event calls draw(), which replaces whole panes with
    // innerHTML: destroying the composer, its contents, the focus and the
    // caret. In a live fleet an event arrives whenever ANY agent does anything,
    // so a human writing to an agent loses it mid-sentence. This is how it was
    // found: a fill() followed by a click() failed under load because the
    // redraw landed between them, which is the same race a person hits.
    const composer = page.locator('.compose[data-agent="web-render"] input[type="text"]')
    await composer.fill("humans are here too")
    await composer.focus()
    // Force the redraw the fleet would cause anyway, mid-compose.
    await page.evaluate(() => (window as any).draw())
    check("a redraw does not eat what the human is typing",
      (await composer.inputValue()) === "humans are here too", await composer.inputValue())
    check("and leaves the caret where it was",
      await page.evaluate(() => document.activeElement?.tagName === "INPUT"))

    await page.locator('.compose[data-agent="web-render"] .act.post').click()
    // NOT the flash: it clears itself after six seconds, so waiting on it is a
    // race that fails about one run in three. Assert the EFFECT instead, which
    // is the better test anyway, since it checks what happened rather than what
    // was announced.
    // The event announces that a post happened and deliberately does NOT carry
    // its text: space events have no recipient, so a body here goes to every
    // authenticated agent on the board, member or not (SPEC §10).
    await expectEvent(a.token, "agent.post", (e) => e.data?.agent_id === "web-render")
    check("posting reaches the board as an event", true)

    const seen = await tool("events_since", { token: a.token, since_serial: 0 })
    const posts = (seen.events ?? []).filter((e: any) => e.type === "agent.post")
    check("the event carries no body: only members and subscribers may read one",
      !JSON.stringify(posts).includes("humans are here too"),
      JSON.stringify(posts).slice(0, 200))

    // The point of all of it: an AGENT can see what the human said. Through
    // read_space, which checks who is asking.
    const read = await tool("read_space", { token: a.token, space: "web-render" })
    check("an agent reads the human's post in the agent",
      (read.posts ?? []).some((p: any) => (p.body ?? "").includes("humans are here too")),
      JSON.stringify(read.posts ?? []).slice(0, 200))
  }

  // ── the human answers an agent's question ──────────────────────────────
  {
    // `checker` asked `builder` a question during setup; ask the human one too.
    const me = ((await (await fetch(`http://${ADDR}/api/me`, { headers: keyed })).json()) as any).agent
    check("the human has an agent identity, not an agent of their own", typeof me === "string" && me.length > 0, String(me))

    const asked = await tool("send", { token: b.token, to: me, type: "question",
      body: "Should I proceed with the rename?", op_id: "ask-human", deadline_s: 600 })

    await page.locator('.views button[data-view="mail"]').click()
    const reply = page.locator(".compose.reply").first()
    await reply.waitFor({ timeout: 8000 })
    check("a question addressed to the human gets a reply box", true)

    await reply.locator('input[type="text"]').fill("no, hold off until Monday")
    await reply.locator(".act.respond").click()
    await Bun.sleep(250) // the read_mail assertion below is the real check

    // The agent must be able to READ the answer, not merely see it delivered.
    // By serial, not from the inbox: an inbox holds mail addressed TO you, and
    // the asker is the sender: its answer arrives on the message it sent.
    const got = await tool("read_mail", { token: b.token, msg_serial: asked.msg_serial })
    const answer = got.message?.response ?? got.response ?? ""
    check("the agent receives the human's answer",
      String(answer).includes("hold off until Monday"),
      JSON.stringify(got).slice(0, 240))
  }

  // ── the human APPROVES a request, which is not the same as answering ───
  {
    // THE GAP THIS CLOSES. Every open message on the board got one `answer`
    // button, and core refuses that disposition for a request, so a human on
    // their own authenticated board could read a coordinator grant and had no
    // way to act on it. The desktop notification was the only working approval
    // surface, and this board is what it is supposed to fall back to when
    // notifications cannot be raised.
    //
    // This suite covered answering a QUESTION and nothing else, which is why it
    // stayed green. Found by a pre-release review.
    const me = ((await (await fetch(`http://${ADDR}/api/me`, { headers: keyed })).json()) as any).agent
    const asked = await tool("send", { token: b.token, to: me, type: "request",
      body: "may I coordinate the release?", grant: "coordinator",
      op_id: "ask-human-grant", deadline_s: 600 })

    await page.locator('.views button[data-view="mail"]').click()
    const card = page.locator(`.msg[data-serial="${asked.msg_serial}"]`).first()
    await card.waitFor({ timeout: 8000 })

    // The card must say what approving DOES, not only what the sender wrote.
    const effect = (await card.locator(".effect").textContent().catch(() => "")) ?? ""
    check("the card says what approving the request would do",
      effect.includes("coordinator"), effect || "(no effect line)")

    // The controls are a SIBLING of the card, not a child: messageHTML renders
    // the message and the view appends its own compose block beside it.
    const approve = page.locator(`.compose.reply[data-serial="${asked.msg_serial}"] .act.approve`)
    check("a request offers approve, not a reply box", await approve.count() === 1)
    await approve.click()
    await Bun.sleep(300)

    const got = await tool("read_mail", { token: b.token, msg_serial: asked.msg_serial })
    const state = got.message?.state ?? got.state ?? ""
    check("the request is approved, from the board alone",
      String(state) === "approved", String(state))
  }

  // ── the human sends a message unprompted ───────────────────────────────
  {
    await page.locator("#msg-to").fill("builder")
    await page.locator("#msg-body").fill("please stop touching internal/web")
    await page.locator("#msg-type").selectOption("request")
    await page.locator(".act.send").click()
    // The inbox check below is the real assertion; nothing transient to wait on.
    await Bun.sleep(250)

    const inbox = await tool("inbox", { token: a.token })
    const got = (inbox.messages ?? []).find((m: any) =>
      (m.body ?? "").includes("stop touching internal/web"))
    check("an agent receives a message the human sent from the board", !!got,
      JSON.stringify(inbox.messages ?? []).slice(0, 240))
    check("and it arrives as an ordinary request it may decline",
      got?.type === "request", String(got?.type))
  }

  // ── spaces render on the operator board ──────────────────────────────
  await page.locator('.views button[data-view="agents"]').click()
  {
    const pane = page.locator("#pane-agents")
    await pane.locator(".space").first().waitFor({ timeout: 5000 })
    const text = (await pane.textContent()) ?? ""
    check("the Dibs tab lists the space", text.includes("web-render"), text.slice(0, 160))
    check("it shows the topic", text.includes("drawing the operator board"), text.slice(0, 160))
    check("it shows both members", text.includes("builder") && text.includes("checker"), text.slice(0, 160))
    check("an exclusive space is marked as such", /exclusive/i.test(text), text.slice(0, 160))
    // SPEC-CHANNELS.md §10.3: an auto-join must be explainable. The score rides
    // with the mark, so it is available without spending a line of the board on
    // every membership.
    const why = (await pane.locator(".member[data-why]").first().getAttribute("data-why")) ?? ""
    check("an auto-joined member carries its score as evidence",
      /0\.7/.test(why) && /lexical/.test(why), why || "(no explanation on the mark)")
    check("an exclusive space shows who is waiting", /waiting:/i.test(text), text.slice(0, 240))
    }
  {
    // Back to the roster for the agent-level marks.
    await page.locator('.views button[data-view="board"]').click()
    const roster = page.locator("#pane-board")
    await roster.locator(".entry").first().waitFor({ timeout: 5000 })
    const rtext = (await roster.textContent()) ?? ""
    check("a subagent is marked with its parent", /↳\s*builder/.test(rtext), rtext.slice(0, 220))
    check("a coordinator is marked as one", /coordinator/i.test(rtext), rtext.slice(0, 220))
    // Not "the attribute is present": whether a person can actually GET to the
    // explanation. It used to be a `title`, which meant: mouse only, hover only,
    // after a delay, and never on a phone. The text was written and most readers
    // could not reach it. So this drives it the way a keyboard user does.
    const badge = roster.locator(".badge.sub").first()
    check("and the mark explains itself",
      /inherits its agents/.test((await badge.getAttribute("data-why")) ?? ""),
      (await badge.getAttribute("data-why")) ?? "(no explanation on the mark)")

    await badge.focus()
    const shown = await page.evaluate(() => {
      const tip = document.getElementById("agents-why")
      return { open: tip?.matches(":popover-open") ?? false, text: tip?.textContent ?? "" }
    })
    check("and the explanation is reachable from the keyboard, not just a mouse",
      shown.open && /inherits its agents/.test(shown.text), JSON.stringify(shown))

    // It must also be positioned by the anchor rather than dumped in the middle
    // of the viewport, which is where an unanchored popover lands by default.
    const placed = await page.evaluate(() => {
      const tip = document.getElementById("agents-why")!.getBoundingClientRect()
      const b = document.querySelector(".badge.sub")!.getBoundingClientRect()
      return { dx: Math.abs((tip.left + tip.width / 2) - (b.left + b.width / 2)), h: tip.height }
    })
    check("and it is anchored to the mark it explains",
      placed.h > 0 && placed.dx < 200, JSON.stringify(placed))

    // A coordination board redraws whenever ANY agent does anything, and it
    // redraws by replacing its HTML, so the mark being read is destroyed and
    // rebuilt as a different node several times a minute. The first version of
    // the explainer closed on that, which meant the explanation was pulled away
    // mid-sentence every time the fleet moved, on the one screen whose whole
    // purpose is watching the fleet move. Observed on a live board, not
    // predicted.
    await page.evaluate(() => (globalThis as { draw?: () => void }).draw?.())
    const survived = await page.evaluate(() => {
      const tip = document.getElementById("agents-why")
      const a = document.activeElement as HTMLElement | null
      return {
        open: tip?.matches(":popover-open") ?? false,
        text: tip?.textContent ?? "",
        // The redraw must not cost the reader their tab position either.
        refocused: a?.matches("[data-why]") ?? false,
      }
    })
    check("and it survives the board redrawing underneath the reader",
      survived.open && /inherits its agents/.test(survived.text) && survived.refocused,
      JSON.stringify(survived))

    // And it goes away when the reader moves on, without stealing their place.
    await page.keyboard.press("Escape")
    check("and it closes on Escape",
      !(await page.evaluate(() => document.getElementById("agents-why")?.matches(":popover-open"))),
      "still open")
    await page.locator('.views button[data-view="agents"]').click()
    await page.locator("#pane-agents .space").first().waitFor({ timeout: 5000 })
  }
  {
    const page2 = page
    void page2
    check("the agent tally counts them",
      (await page.locator("#tally-agents").textContent()) === "2",
      String(await page.locator("#tally-agents").textContent()))
  }

  check("the live stream connected", await page.locator(".status-mark.live").count() > 0)
  check("no uncaught errors in the page", realErrors().length === 0, realErrors().slice(0, 2).join(" | "))
  // And the injection really did happen: otherwise the exclusion above is
  // hiding nothing and the transport test proved nothing.
  check("the deliberate transport failure was actually injected",
    pageErrors.some(injected), pageErrors.slice(0, 3).join(" | "))
} catch (err) {
  failures++
  console.log("  [31m✗[0m threw:", err?.stack ?? err)
}

console.log(`\n${checks - failures}/${checks} checks passed`)
process.exit(failures ? 1 : 0)
