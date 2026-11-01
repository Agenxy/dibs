// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
import { chromium } from "playwright"
import { join } from "node:path"

type RPC = (method: string, params: unknown) => Promise<any>
type Check = (name: string, condition: boolean, detail?: string) => void

// The real human-flow dev daemon supplies only the OS-presence leaf fixture.
// App HTML, MCP calls, AppBridge, iframe, keyboard and layout are unchanged.
export async function checkConfirmedBoard(rpc: RPC, caller: any, receiver: any, check: Check) {
  const result = await rpc("tools/call", { name: "board", arguments: { token: caller.token } })
  const payload = result._meta?.["com.dibs/panel"]
  if (!payload?.board?.agents?.some((a: any) => a.id === caller.agent_id)) throw new Error("confirmed board setup: real caller missing")
  const resource = await rpc("resources/read", { uri: result._meta.ui.resourceUri })
  const html = resource.contents[0].text
  const fixture = structuredClone(result)
  const board = fixture._meta["com.dibs/panel"].board
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
    const rows = panel.locator("#pane-board .entry.active:visible")
    const before = await rows.evaluateAll(els => els.map(el => ({ id: (el as HTMLElement).dataset.agent, height: el.getBoundingClientRect().height })))
    if (before.length !== 8) throw new Error(`confirmed board setup: expected eight recent rows, got ${before.length}`)
    await panel.locator("#human-lock").click()
    await panel.locator('#human-lock[data-state="unlocked"]').waitFor()
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
    check("Enter opens one labelled row composer and focuses it", await composers.count() === 1 && await composers.getAttribute("role") === "group" && await first.getAttribute("aria-controls") === await composers.getAttribute("id") && await first.getAttribute("aria-expanded") === "true" && await text.evaluate(el => el === document.activeElement))
    await text.fill("A draft survives the fleet changing")
    const pushed = structuredClone(fixture)
    pushed._meta["com.dibs/panel"].board.serial++
    await page.evaluate(result => (window as any).__deliver(result), pushed)
    check("a board push keeps the open row draft and focus", await text.inputValue() === "A draft survives the fleet changing" && await text.evaluate(el => el === document.activeElement))
    const second = panel.getByRole("button", { name: "Message Role 2", exact: true })
    await second.click()
    check("opening another row replaces the first composer", await composers.count() === 1 && await composers.getAttribute("data-target") === "confirmed-2" && await first.getAttribute("aria-expanded") === "false")
    await composers.getByRole("textbox").press("Escape")
    check("Escape closes the composer and returns focus to its row action", await composers.count() === 0 && await second.evaluate(el => el === document.activeElement) && await second.getAttribute("aria-expanded") === "false" && await second.getAttribute("aria-controls") === null)
    await first.click()
    check("reopening a row retains its own draft", await text.inputValue() === "A draft survives the fleet changing")
    await text.fill("Confirmed row message through the real MCP app")
    await composers.getByRole("button", { name: "tell", exact: true }).click()
    let delivered = false
    for (let until = Date.now() + 5000; !delivered && Date.now() < until;) {
      const inbox = await rpc("tools/call", { name: "inbox", arguments: { token: receiver.token, limit: 8 } })
      delivered = JSON.parse(inbox.content[0].text).messages?.some((m: any) => m.body === "Confirmed row message through the real MCP app")
      if (!delivered) await Bun.sleep(25)
    }
    check("the selected row sends actual human mail through the real MCP proxy", delivered)
    check("confirmed row interactions have no browser exceptions", errors.length === 0, errors.join(" | "))
  } finally {
    await browser.close(); server.stop(true)
  }
}
