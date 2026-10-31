// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

import type { Page } from "playwright"
import { mkdirSync } from "node:fs"
import { join } from "node:path"

type Check = (name: string, condition: boolean, detail?: string) => void

// The shipped resources/read artifact, real AppBridge, real iframe and real
// browser layout. Only the fleet snapshot is a deterministic 60-agent fixture.
export async function checkCompactBoard(page: Page, base: any, self: string, check: Check, live: {
  tool: (name: string, args: any) => Promise<any>, token: string, peerToken: string,
}) {
  const savedViewport = page.viewportSize()!
  const savedStyle = await page.locator("#panel").getAttribute("style")
  const panel = page.frameLocator("#panel")
  const fixture = structuredClone(base)
  const payload = fixture._meta?.["com.dibs/panel"]
  if (!payload?.board?.agents?.length) throw new Error("compact setup: no actual board payload")
  const originalSelf = payload.board.agents.find((a: any) => a.id === self)
  if (!originalSelf) throw new Error("compact setup: caller absent from actual board")
  const serials: number[] = []
  for (const body of ["Compact unrelated question", "Compact selected question"]) {
    const result = await live.tool("send", { token: live.peerToken, to: self,
      type: "question", body, deadline_s: 600 })
    const sent = JSON.parse(result.content[0].text)
    if (!sent.msg_serial) throw new Error(`compact setup: send refused ${result.content[0].text}`)
    serials.push(sent.msg_serial)
  }
  const fresh = await live.tool("inbox", { token: live.token, limit: 8 })
  payload.inbox = JSON.parse(fresh.content[0].text).messages
  if (!payload.inbox?.some((m: any) => m.serial === serials[1] && m.body?.includes("Compact selected question"))) throw new Error("compact setup: new question absent")
  const now = Date.now()
  payload.board.agents = Array.from({ length: 60 }, (_, index) => ({
    ...structuredClone(originalSelf),
    id: index === 0 ? self : `compact-${index}`,
    name: index === 0 ? "This viewing agent" : `Worker ${String(index).padStart(2, "0")}`,
    status: index < 16 ? "active" : index < 18 ? "stale" : "dormant",
    stale_reason: index === 16 || index === 17 ? "lease_lapsed" : undefined,
    description: index === 59 ? "hidden-dormant-description" : "A fixture worker",
    name_aliases: index === 58 ? ["former-name-needle"] : [],
    slots: [{ id: "s1", text: index === 57 ? "declared-work-needle" : index === 0 ? "A long declaration that remains readable on expansion. ".repeat(12) : `Work for worker ${index}` }],
    host: index === 56 ? "Distant-workstation" : "Board workstation",
    local_host_label: index !== 56,
    last_coordination_at: new Date(now - (index === 0 ? 120 : 60 - index) * 1000).toISOString(),
  }))
  const settled = async () => {
    await panel.locator("body").evaluate(async () => {
      await document.fonts.ready
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const finite = document.getAnimations().filter((a) => Number.isFinite(a.effect?.getComputedTiming().endTime))
      await Promise.all(finite.map((a) => a.finished.catch(() => {})))
    })
  }
  const capture = async (theme: string) => {
    await settled()
    await panel.locator("#main").evaluate((el) => { el.scrollTop = 0 })
    const dir = process.env.DIBS_COMPACT_ARTIFACT_DIR
    if (!dir) return
    mkdirSync(dir, { recursive: true })
    await panel.locator("#app").screenshot({ path: join(dir, `${theme}.png`) })
  }
  const context = async (width: number, height?: number, theme = "dark", fixed = false) => {
    await page.locator("#panel").evaluate((el, size) => {
      const frame = el as HTMLIFrameElement
      frame.style.width = `${size.width}px`
      frame.style.height = "800px"
    }, { width })
    await page.evaluate(({ width, height, theme, fixed }) => (window as any).__bridge.setHostContext({
      theme, containerDimensions: { width, ...(height ? fixed ? { height } : { maxHeight: height } : {}) },
    }), { width, height, theme, fixed })
  }
  try {
    await page.setViewportSize({ width: 1040, height: 800 })
    await context(600, 800)
    await page.evaluate((result) => (window as any).__deliver(result), fixture)
    await panel.getByRole("tab", { name: "Board", exact: true }).click()
    await panel.locator('.entry[data-agent="compact-59"]').waitFor({ state: "attached" })
    await panel.locator('#pane-board').waitFor({ state: "visible" })
    const existingOwn = panel.locator("#self-row .says[open] > summary")
    if (await existingOwn.count()) await existingOwn.click()
    await panel.locator("body").evaluate(() => document.fonts.ready)
    await capture("dark")
    const mark = await panel.locator(".masthead .mark svg").evaluate((el) => ({
      tile: el.querySelector("rect")?.getAttribute("fill"), dots: el.querySelectorAll("circle").length,
      width: el.getBoundingClientRect().width,
    }))
    check("the unmodified product mark is used at 20px", mark.tile === "#131518" && mark.dots === 3 && mark.width === 20, JSON.stringify(mark))
    const reported = await page.evaluate(() => (window as any).__probe.sizes.at(-1))
    check("the host receives the bounded size, not the long document height", reported > 200 && reported <= 561, `${reported}px`)
    const size = await panel.locator("#app").evaluate((el) => el.getBoundingClientRect().height)
    check("60-agent app fits within 70% of the host height", size <= 561, `${size}px > 560px`)
    const active = await panel.locator("#pane-board .entry.active:visible").evaluateAll((rows) => rows.map((r) => (r as HTMLElement).dataset.agent))
    check("active starts with its eight most recent remaining agents",
      JSON.stringify(active) === JSON.stringify(["compact-15", "compact-14", "compact-13", "compact-12", "compact-11", "compact-10", "compact-9", "compact-8"]), JSON.stringify(active))
    check("the viewer is pinned once above the scrolling results", await panel.locator("#self-row .entry").count() === 1 && await panel.locator("#self-row .entry").getAttribute("data-agent") === self &&
      await panel.locator(`#pane-board .entry[data-agent="${self}"]`).count() === 0)
    check("dormant starts collapsed", await panel.locator("#pane-board .entry.dormant:visible").count() === 0)
    check("out-of-touch agents stay visible before healthy groups",
      await panel.locator("#pane-board .entry.stale:visible").count() === 2 &&
      await panel.locator("#pane-board .entry:visible").first().getAttribute("data-agent") === "compact-17")
    check("the repeated local host badge is omitted without hiding a different host",
      await panel.locator("#pane-board .badge.machine").count() === 1)
    check("unanswered mail appears above both healthy sections", await panel.locator("#pane-board").evaluate((el) => {
      const attention = el.querySelector(".attention"), active = el.querySelector(".entry.active")
      return !!attention && !!active && !!(attention.compareDocumentPosition(active) & Node.DOCUMENT_POSITION_FOLLOWING)
    }))
    const search = panel.getByRole("searchbox", { name: "Search Dibs" })
    const present = await search.count() === 1
    check("one named search field is available outside the scrolling list", present)
    if (!present) return // old-code proof ends without opening any external UI
    await panel.getByText("Show all 15", { exact: true }).click()
    check("Show all expands every active row", await panel.locator("#pane-board .entry.active:visible").count() === 15)
    await search.fill("Worker 15")
    await search.fill("")
    check("expanded Active survives a redraw", await panel.locator("#pane-board .entry.active:visible").count() === 15)
    await panel.getByText("Show fewer", { exact: true }).click()
    const ownSummary = panel.locator("#self-row .says > summary")
    check("the fixed own row is one line initially", await panel.locator("#self-row").evaluate((el) => el.getBoundingClientRect().height >= 25 && el.getBoundingClientRect().height < 42))
    await ownSummary.focus()
    await ownSummary.press("Enter")
    await panel.locator("#self-row .says[open]").waitFor()
    check("keyboard expansion exposes the full declaration", await panel.locator("#self-row .lede").evaluate((el) => getComputedStyle(el).whiteSpace === "normal" && el.getBoundingClientRect().height > 25))
    await ownSummary.press("Enter")
    await panel.locator("#self-row .says:not([open])").waitFor()
    for (const [query, expected] of [
      ["Worker 59", "compact-59"], ["former-name-needle", "compact-58"],
      ["hidden-dormant-description", "compact-59"], ["declared-work-needle", "compact-57"],
      ["Distant-workstation", "compact-56"],
    ]) {
      await search.fill(query)
      const ids = await panel.locator("#pane-board .entry:visible").evaluateAll((rows) => rows.map((r) => (r as HTMLElement).dataset.agent))
      check(`search finds ${query} inside collapsed data`, ids.length === 1 && ids[0] === expected, JSON.stringify(ids))
    }
    await search.fill("not-present-anywhere-needle")
    check("a search miss says No matches", await panel.locator("#pane-board").getByText("No matches", { exact: true }).isVisible())
    await search.fill("rendering the panel")
    await panel.locator("#tab-agents").click()
    await settled()
    check("the same search filters Spaces", await panel.locator("#pane-agents .space:visible").count() === 1)
    await search.fill("agent.registered")
    await panel.locator("#tab-activity").click()
    await settled()
    const kinds = await panel.locator("#pane-activity .e-type:visible").allTextContents()
    check("the same search filters Activity", kinds.length > 0 && kinds.every((k) => k.includes("agent.registered")), JSON.stringify(kinds))
    for (const tab of ["agents", "mail", "activity"]) {
      await search.fill("not-present-anywhere-needle")
      await panel.locator(`#tab-${tab}`).click()
      await settled()
      check(`${tab} shows a plain search miss`, await panel.locator(`#pane-${tab}`).getByText("No matches", { exact: true }).isVisible())
    }
    await panel.locator("#tab-board").click()
    await search.fill("")
    await capture("dark")
    await context(600, 800, "light")
    await capture("light")
    check("the pinned agent remains painted after searches and tab changes", await panel.locator("#self-row").evaluate((el) => el.getBoundingClientRect().height >= 25))
    const face = await panel.locator(".entry .name").first().evaluate((el) => getComputedStyle(el).fontFamily)
    check("agent names use the identity's Atkinson face", face.includes("Atkinson Hyperlegible Next"), face)
    const header = await panel.locator(".masthead").evaluate((el) => el.getBoundingClientRect().top)
    const ownTop = await panel.locator("#self-row").evaluate((el) => el.getBoundingClientRect().top)
    await panel.locator("#main").evaluate((el) => { el.scrollTop = el.scrollHeight })
    check("the own row stays visible when the list scrolls", Math.abs(ownTop - await panel.locator("#self-row").evaluate((el) => el.getBoundingClientRect().top)) < 1)
    check("list scrolling leaves the header fixed", Math.abs(header - await panel.locator(".masthead").evaluate((el) => el.getBoundingClientRect().top)) < 1)
    await context(390, 500)
    await page.setViewportSize({ width: 430, height: 700 })
    const mobile = await panel.locator("#app").evaluate((el) => ({ height: el.getBoundingClientRect().height, overflow: document.documentElement.scrollWidth > innerWidth }))
    check("a smaller host updates the bound without horizontal overflow", mobile.height <= 351 && !mobile.overflow, JSON.stringify(mobile))
    await capture("mobile")
    const mobileUI = await panel.locator("#app").evaluate((app) => {
      const name = app.querySelector("#self-row .name")!, nav = app.querySelector(".views")!, foot = app.querySelector(".foot")!
      return { nameVisible: name.scrollWidth <= name.clientWidth, nameStyle: getComputedStyle(name).textOverflow,
        scrollableTabs: getComputedStyle(nav).overflowX === "auto",
        tabTextFits: [...nav.querySelectorAll("button")].every((button) => button.scrollWidth <= button.clientWidth),
        footerWraps: getComputedStyle(foot).whiteSpace === "normal", fade: getComputedStyle(app.querySelector("#main")!).maskImage }
    })
    check("mobile keeps the sender, tabs, footer and scroll edge readable", mobileUI.nameVisible && mobileUI.nameStyle !== "ellipsis" && mobileUI.scrollableTabs && mobileUI.tabTextFits && mobileUI.footerWraps && mobileUI.fade !== "none", JSON.stringify(mobileUI))
    check("the small frame still exposes readable results", await panel.locator("#main").evaluate((el) => el.clientHeight >= 45))
    await context(900, 800)
    const wide = await page.evaluate(() => (window as any).__probe.sizes.at(-1))
    check("a wide host includes frame margins inside the height bound", wide <= 561, `${wide}px`)
    await context(600, 400, "dark", true)
    const fixed = await panel.locator("#app").evaluate((el) => el.getBoundingClientRect().height)
    check("a host's fixed height is honored too", fixed <= 281, `${fixed}px`)
    await context(600)
    const fallback = await panel.locator("#app").evaluate((el) => el.getBoundingClientRect().height)
    check("a host with no height gets a finite fallback", fallback >= 559 && fallback <= 561, `${fallback}px`)
    await search.focus()
    check("search is keyboard reachable", await search.evaluate((el) => document.activeElement === el))
    await search.fill("Compact selected question")
    await panel.locator("#tab-mail").click()
    await settled()
    const target = panel.locator("#pane-mail .msg:visible")
    check("mail filtering keeps its actual serial and body together", await target.count() === 1 &&
      await target.getAttribute("data-serial") === String(serials[1]), JSON.stringify(await target.evaluateAll((els) => els.map((el) => ({serial: (el as HTMLElement).dataset.serial, text: el.textContent?.slice(0, 200)})))))
    if (await target.count() !== 1) throw new Error("compact setup: selected message absent after search")
    await target.getByRole("button", { name: "Decline", exact: true }).click()
    // Read actual daemon state, not only the optimistically updated DOM.
    await panel.locator(`.msg[data-serial="${serials[1]}"] button`).first().waitFor({ state: "detached" })
    const read = async (serial: number) => JSON.parse((await live.tool("read_mail", { token: live.token, msg_serial: serial })).content[0].text).message
    const selected = await read(serials[1]), unrelated = await read(serials[0])
    check("a filtered mail action changes only the selected real message", selected.state === "declined" && !["declined", "answered"].includes(unrelated.state), JSON.stringify([selected.state, unrelated.state]))
  } finally {
    await page.setViewportSize(savedViewport)
    await page.locator("#panel").evaluate((el, style) => el.setAttribute("style", style), savedStyle ?? "")
    await page.evaluate((result) => {
      ;(window as any).__bridge.setHostContext({ theme: "dark", containerDimensions: { width: 900, maxHeight: 6000 } })
      ;(window as any).__deliver(result)
    }, base)
  }
}
