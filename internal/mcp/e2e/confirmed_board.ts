// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
import { chromium } from "playwright"
import { join } from "node:path"

type RPC = (method: string, params: unknown) => Promise<any>
type Check = (name: string, condition: boolean, detail?: string) => void

// The real human-flow dev daemon supplies only the OS-presence leaf fixture.
// App HTML, MCP calls, AppBridge, iframe, keyboard and layout are unchanged.
export async function checkConfirmedBoard(rpc: RPC, caller: any, receiver: any, check: Check) {
  await rpc("tools/call", { name: "open_space", arguments: { token: receiver.token, space: "auth-unjoined", topic: "Real browser join fixture" } })
  const result = await rpc("tools/call", { name: "board", arguments: { token: caller.token } })
  const payload = result._meta?.["com.dibs/panel"]
  if (!payload?.board?.agents?.some((a: any) => a.id === caller.agent_id)) throw new Error("confirmed board setup: real caller missing")
  const resource = await rpc("resources/read", { uri: result._meta.ui.resourceUri })
  const html = resource.contents[0].text
  const fixture = structuredClone(result)
  const board = fixture._meta["com.dibs/panel"].board
  if (!Number.isSafeInteger(board.serial)) throw new Error("confirmed board setup: missing real board serial")
  const now = Date.now()
  board.agents = Array.from({ length: 60 }, (_, index) => ({
    id: index === 0 ? caller.agent_id : index === 1 ? receiver.agent_id : `confirmed-${index}`,
    name: index === 0 ? "Viewer" : index === 1 ? "Recipient" : `Role ${index}`,
    status: index < 16 ? "active" : "dormant", local_host_label: true,
    last_coordination_at: new Date(now - (index === 0 ? 120 : index) * 1000).toISOString(),
    slots: [{ id: "s1", text: `Fixture work ${index}` }],
  }))
  fixture._meta["com.dibs/panel"].inbox = []
  const built = await Bun.build({ entrypoints: [join(import.meta.dir, "host", "host.js")], target: "browser" })
  if (!built.success) throw new Error("confirmed board setup: host bundle failed")
  const hostJS = await built.outputs[0].text()
  const server = Bun.serve({ port: 0, async fetch(req) {
    const path = new URL(req.url).pathname
    if (path === "/rpc") {
      const body = await req.json() as any
      return Response.json({ result: await rpc(body.method, body.params) })
    }
    if (path === "/panel.html") return new Response(html, { headers: { "content-type": "text/html" } })
    if (path === "/host.js") return new Response(hostJS, { headers: { "content-type": "text/javascript" } })
    if (path === "/favicon.ico") return new Response(null, { status: 204 })
    return new Response('<!doctype html><title>Confirmed board fixture</title><body><script>window.__serverCapabilities={tools:{},resources:{}}</script><script type="module" src="/host.js"></script>', { headers: { "content-type": "text/html" } })
  } })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 900, height: 800 } })
    const errors: string[] = []
    page.on("pageerror", err => errors.push(String(err)))
    await page.goto(`http://127.0.0.1:${server.port}/`, { waitUntil: "load" })
    await page.waitForFunction(() => !!(window as any).__ready)
    await page.evaluate((result) => {
      const frame = document.getElementById("panel") as HTMLIFrameElement
      frame.style.width = "600px"; frame.style.height = "560px"
      ;(window as any).__bridge.setHostContext({ theme: "dark", containerDimensions: { width: 600, maxHeight: 800 } })
      ;(window as any).__deliver(result)
    }, fixture)
    const panel = page.frameLocator("#panel")
    await panel.locator("#self-row .entry").waitFor()
    await panel.getByRole("tab", { name: "Board", exact: true }).click()
    const settled = () => panel.locator("body").evaluate(async () => {
      await document.fonts.ready
      await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await Promise.all(document.getAnimations().filter(animation => Number.isFinite(animation.effect?.getComputedTiming().endTime)).map(animation => animation.finished.catch(() => {})))
    })
    const rows = panel.locator("#pane-board .entry.active:visible")
    await settled()
    const before = await rows.evaluateAll(els => els.map(el => ({ id: (el as HTMLElement).dataset.agent, height: el.getBoundingClientRect().height })))
    if (before.length !== 8) throw new Error(`confirmed board setup: expected eight recent rows, got ${before.length}`)
    await panel.locator("#human-lock").click()
    await panel.locator('#human-lock[data-state="unlocked"]').waitFor()
    await settled()
    const composers = panel.locator("#pane-board .human-compose")
    const count = await composers.count()
    check("confirmed roster renders no composer before a row is opened", count === 0, `${count} composers`)
    if (count !== 0) return // The intended old-code RED, not a setup exception.
    const after = await rows.evaluateAll(els => els.map(el => ({ id: (el as HTMLElement).dataset.agent, height: el.getBoundingClientRect().height })))
    check("confirmation preserves compact row heights and recency order", JSON.stringify(before) === JSON.stringify(after), JSON.stringify({ before, after }))
    check("confirmed 60-agent frame stays bounded with Dormant collapsed", await panel.locator("#app").evaluate(el => el.getBoundingClientRect().height <= 561) && await panel.locator(".entry.dormant:visible").count() === 0)
    const first = panel.getByRole("button", { name: "Message Recipient", exact: true })
    await first.focus(); await first.press("Enter")
    const text = composers.getByRole("textbox")
    check("Enter opens one labelled row composer and focuses it", await composers.count() === 1 && await panel.getByRole("group", { name: "Message Recipient", exact: true }).count() === 1 && await first.getAttribute("aria-controls") === await composers.getAttribute("id") && await first.getAttribute("aria-expanded") === "true" && await text.evaluate(el => el === document.activeElement))
    await text.fill("A draft survives the fleet changing")
    const pushed = structuredClone(fixture)
    pushed._meta["com.dibs/panel"].board.serial++
    pushed._meta["com.dibs/panel"].board.agents[2].slots[0].text = "A real fleet update changes the roster HTML"
    await page.evaluate(result => (window as any).__deliver(result), pushed)
    await panel.locator("#foot-node").filter({ hasText: `serial ${pushed._meta["com.dibs/panel"].board.serial}` }).waitFor()
    check("a board push keeps the open row draft and focus", await text.inputValue() === "A draft survives the fleet changing" && await text.evaluate(el => el === document.activeElement))
    await page.evaluate(() => {
      const input = document.createElement("input")
      input.id = "host-composer"
      input.setAttribute("aria-label", "Host message composer")
      document.body.prepend(input)
    })
    const hostComposer = page.getByRole("textbox", { name: "Host message composer" })
    await hostComposer.fill("I am typing in the host chat")
    const outsidePush = structuredClone(pushed)
    outsidePush._meta["com.dibs/panel"].board.serial++
    outsidePush._meta["com.dibs/panel"].board.agents[2].slots[0].text = "Another fleet update while the host has focus"
    await page.evaluate(result => (window as any).__deliver(result), outsidePush)
    await panel.locator("#foot-node").filter({ hasText: `serial ${outsidePush._meta["com.dibs/panel"].board.serial}` }).waitFor()
    check("a confirmed-row redraw does not take focus from the host chat", await hostComposer.evaluate(el => el === document.activeElement) && await hostComposer.inputValue() === "I am typing in the host chat")
    const second = panel.getByRole("button", { name: "Message Role 2", exact: true })
    await second.click()
    check("opening another row replaces the first composer", await composers.count() === 1 && await composers.getAttribute("data-target") === "confirmed-2" && await first.getAttribute("aria-expanded") === "false")
    await composers.getByRole("textbox").press("Escape")
    check("Escape closes the composer and returns focus to its row action", await composers.count() === 0 && await second.evaluate(el => el === document.activeElement) && await second.getAttribute("aria-expanded") === "false" && await second.getAttribute("aria-controls") === null)
    await first.click()
    check("reopening a row retains its own draft", await text.inputValue() === "A draft survives the fleet changing")
    await text.fill("Confirmed row message through the real MCP app")
    const retainedTell = await composers.getByRole("button", { name: "tell", exact: true }).elementHandle()
    if (!retainedTell) throw new Error("confirmed board setup: no human send control")
    // Observe real rebinding on the retained node without changing its effect.
    // Clearing the field can hide duplicate listeners from a request count,
    // so also record any click-listener additions during the two redraws.
    await retainedTell.evaluate((el) => {
      const add = el.addEventListener
      ;(window as any).__retainedClickListeners = 0
      el.addEventListener = function(type: string, ...args: any[]) {
        if (type === "click") (window as any).__retainedClickListeners++
        return add.call(this, type, ...args as [any, any])
      }
    })
    for (let redraw = 1; redraw <= 2; redraw++) {
      const update = structuredClone(outsidePush)
      update._meta["com.dibs/panel"].board.serial += redraw
      update._meta["com.dibs/panel"].board.agents[2].slots[0].text = `Unrelated roster update ${redraw} before the human sends`
      await page.evaluate(result => (window as any).__deliver(result), update)
      await panel.locator("#foot-node").filter({ hasText: `serial ${update._meta["com.dibs/panel"].board.serial}` }).waitFor()
    }
    const retained = await retainedTell.evaluate((el) => ({
      connected: el.isConnected, same: el === document.querySelector('.human-compose button[data-do="notify"]'),
      propertyHandler: typeof el.onclick === "function", listenerAdds: (window as any).__retainedClickListeners,
    }))
    check("two roster redraws retain the human send control without adding click listeners",
      retained.connected && retained.same && retained.propertyHandler && retained.listenerAdds === 0,
      JSON.stringify(retained))
    await composers.getByRole("button", { name: "tell", exact: true }).click()
    let delivered = false
    for (let until = Date.now() + 5000; !delivered && Date.now() < until;) {
      const inbox = await rpc("tools/call", { name: "inbox", arguments: { token: receiver.token, limit: 8 } })
      delivered = JSON.parse(inbox.content[0].text).messages?.some((m: any) => m.body === "Confirmed row message through the real MCP app")
      if (!delivered) await Bun.sleep(25)
    }
    check("the selected row sends actual human mail through the real MCP proxy", delivered)
    const sendCalls = await page.evaluate(() => (window as any).__probe.toolCalls.filter((call: any) =>
      call.name === "send" && call.arguments?.body === "Confirmed row message through the real MCP app").length)
    check("one click after two redraws sends exactly one human request", sendCalls === 1, String(sendCalls))
    await panel.locator("#tab-agents").click()
    const space = panel.locator(".space").filter({ hasText: "auth-work" })
    await space.getByRole("textbox", { name: "say something in auth-work", exact: true }).fill("Confirmed human space post through the real MCP app")
    await space.getByRole("button", { name: "post", exact: true }).click()
    let posted = false
    for (let until = Date.now() + 5000; !posted && Date.now() < until;) {
      const transcript = await rpc("tools/call", { name: "read_space", arguments: { token: receiver.token, space: "auth-work" } })
      posted = JSON.stringify(transcript).includes("Confirmed human space post through the real MCP app")
      if (!posted) await Bun.sleep(25)
    }
    check("space composers keep their real post action after row messaging is wired", posted)
    await space.getByRole("textbox", { name: "say something in auth-work", exact: true }).fill("Confirmed human announcement through the real MCP app")
    await space.getByRole("button", { name: "announce", exact: true }).click()
    let announced = false
    for (let until = Date.now() + 5000; !announced && Date.now() < until;) {
      const transcript = await rpc("tools/call", { name: "read_space", arguments: { token: receiver.token, space: "auth-work" } })
      announced = JSON.stringify(transcript).includes("Confirmed human announcement through the real MCP app")
      if (!announced) await Bun.sleep(25)
    }
    check("space composers send real announcements through the MCP proxy", announced)
    const unjoined = panel.locator(".space").filter({ hasText: "auth-unjoined" })
    await unjoined.getByRole("button", { name: "join to speak", exact: true }).click()
    const joinedComposer = unjoined.getByRole("textbox", { name: "say something in auth-unjoined", exact: true })
    const joined = await joinedComposer.waitFor({ timeout: 5000 }).then(() => true, () => false)
    check("joining through the browser refreshes real membership and opens its composer", joined)
    check("confirmed row interactions have no browser exceptions", errors.length === 0, errors.join(" | "))
  } finally {
    await browser.close(); server.stop(true)
  }
}
