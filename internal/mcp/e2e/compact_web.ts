// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

import type { Page } from "playwright"

// Real keyed web entry: the HTTP document, keyed mailbox fetch and SSE renderer,
// with its own human authority. No direct state setter or fabricated DOM.
export async function checkCompactWeb(page: Page, check: (name: string, ok: boolean, detail?: string) => void) {
  const search = page.getByRole("searchbox", { name: "Search Dibs" })
  await search.waitFor()
  const mark = await page.locator(".masthead .mark svg").evaluate((el) => ({
    tile: el.querySelector("rect")?.getAttribute("fill"), dots: el.querySelectorAll("circle").length,
    width: el.getBoundingClientRect().width,
  }))
  check("standalone uses the exact product mark at 20px", mark.tile === "#131518" && mark.dots === 3 && mark.width === 20, JSON.stringify(mark))
  const show = async (tab: string) => {
    await page.locator(`#tab-${tab}`).click()
    await page.locator(`#pane-${tab}`).waitFor({ state: "visible" })
  }
  const bound = await page.locator(".sheet").evaluate((el) => ({ height: el.getBoundingClientRect().height, viewport: innerHeight }))
  check("standalone board uses the same bounded frame", bound.height <= Math.min(560, bound.viewport * .7) + 1, JSON.stringify(bound))
  check("standalone has no invented viewing agent", await page.locator(".entry.self").count() === 0)
  check("standalone dormant agents start collapsed", await page.locator(".entry.dormant:visible").count() === 0)
  await search.fill("rendering the web board")
  check("standalone search finds declared work", await page.locator('#pane-board .entry:visible').count() === 1 && await page.locator('#pane-board .entry:visible').getAttribute("data-agent") === "builder")
  await search.fill("drawing the operator board")
  await show("agents")
  check("standalone search filters Spaces", await page.locator('#pane-agents .space:visible').count() === 1)
  await search.fill("Does the web board render?")
  await show("mail")
  await page.locator("#pane-mail").getByText("Does the web board render?", { exact: true }).first().waitFor()
  check("standalone search filters the keyed mailbox", await page.locator('#pane-mail .msg:visible').count() === 1)
  await search.fill("agent.registered")
  await show("activity")
  check("standalone search filters Activity", await page.locator('#pane-activity .event:visible').count() > 0)
  for (const tab of ["board", "agents", "mail", "activity"]) {
    await search.fill("not-present-anywhere-needle")
    await show(tab)
    check(`standalone ${tab} has a plain search miss`, await page.locator(`#pane-${tab}`).getByText("No matches", { exact: true }).isVisible())
  }
  await search.fill("")
  await show("board")
  // The rest of this suite intentionally inspects dead-process badges. Open
  // their section through its real keyboard/touch control first.
  const dormant = page.locator('.roster-group[data-group="idle"] > summary')
  if (await dormant.count()) await dormant.click()
}
