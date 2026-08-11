/* ═══════════════════════════════════════════════════════════════
   LANES. SHARED BOARD COMPONENTS

   The rendering both surfaces have in common, as pure functions:
   data in, HTML string out. No globals, no transport, no state.

   The two surfaces are genuinely different views and must stay so,
   the MCP Apps panel is ONE lane's board and mailbox, authenticated
   by that lane's token; the web board is the operator's god view
   over every lane and all mail, behind the admin password. What
   they share is what a lane looks like, what a message looks like,
   and what an event looks like. Sharing the page would be wrong;
   sharing the components is the whole point.

   Purity is what makes that possible. An earlier version of these
   read `state.lane` and called `canCallTools()` directly, which
   welded them to the panel; every caller-specific decision is now a
   parameter.

   Inlined into both by internal/assets: never fetched, because the
   panel's CSP declares no external origins.
   ═══════════════════════════════════════════════════════════════ */

const Board = (() => {
  const esc = (s) =>
    String(s ?? "").replace(/[&<>"']/g, (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c])

  function ago(iso) {
    if (!iso) return ""
    const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
    // The cutover is 60, not 45: between the two, Math.floor(s/60) is 0 and a
    // lane renders as "0m" old, which reads as broken rather than fresh.
    if (s < 60) return "now"
    if (s < 3600) return Math.floor(s / 60) + "m"
    if (s < 86400) return Math.floor(s / 3600) + "h"
    return Math.floor(s / 86400) + "d"
  }

  // Terminal states, mirroring core.Message.Terminal(). A message is finished
  // when its STATE says so, not when it happens to carry response text,
  // because a denial and a decline are both terminal and both carry none.
  const TERMINAL = new Set([
    "answered", "approved", "denied", "declined", "acked",
    "expired_unanswered", "expired_recipient_dormant", "expired_recipient_dead",
    "displaced",
  ])

  const VERDICT = {
    answered: "Answered", approved: "Approved", denied: "Denied",
    declined: "Declined", acked: "Acknowledged",
    expired_unanswered: "Expired, unanswered",
    expired_recipient_dormant: "Expired: recipient dormant",
    expired_recipient_dead: "Expired: recipient gone",
    displaced: "Displaced by a newer message",
  }

  // In a fleet, "reviewer" is not enough: a human needs to see that it is
  // Opus 5 in Claude Code, not Codex in a terminal.
  function identHTML(a) {
    if (!a) return ""
    const bits = []
    if (a.model) bits.push(`<span class="model">${esc(a.model)}</span>`)
    if (a.harness) bits.push(`<span>${esc(a.harness)}${a.version ? " " + esc(a.version) : ""}</span>`)
    // Project before branch, and branch folded into it. A fleet spread over
    // three repositories rendered three rows reading "main", which is not a
    // distinguishing fact: the project is the thing that tells them apart, and
    // a bare branch name means nothing until you know which tree it is in.
    if (a.project) bits.push(`<span>${esc(a.project)}${a.branch ? " @ " + esc(a.branch) : ""}</span>`)
    else if (a.branch) bits.push(`<span>${esc(a.branch)}</span>`)
    if (a.surface) bits.push(`<span>${esc(a.surface)}</span>`)
    if (a.effort) bits.push(`<span>effort ${esc(a.effort)}</span>`)
    return bits.length ? `<div class="idents">${bits.join("")}</div>` : ""
  }

  // `selfId` is the lane whose view this is, or null on a god view where no
  // lane is "this one". Passing it rather than reading a global is what lets
  // the same function serve both surfaces.
  // WHY an agent stopped counting as live. "Out of touch" beside a last-contact
  // time of "now" reads as a broken board rather than a dead agent, and the
  // three cases are not interchangeable: a process that exited is definitive, a
  // lapsed lease may be nothing worse than a long build, and a lane that never
  // gave a PID has said nothing about a process at all: grouping that last one
  // with the crashed is the misread this exists to prevent.
  const STALE_WHY = {
    process_exited: ["process gone", "its process exited: this agent is not coming back on its own"],
    lease_lapsed: ["no contact", "it stopped checking in; it may be mid-build rather than dead"],
    idle_no_activity: ["idle", "it never gave a pid, so silence says nothing about whether it is alive"],
  }
  function staleReasonHTML(l) {
    const why = STALE_WHY[l.stale_reason]
    if (!why) return ""
    return explained(`tag why ${esc(l.stale_reason)}`, why[0], why[1])
  }

  /**
   * How recently this lane was heard from, as something CSS can select on.
   *
   * The board already showed an age, "now", "4m", "2h", but that is a
   * STRING, and a stylesheet cannot ask whether a string means recently. So
   * liveness was drawn as a fixed pulse on everything alive, which claims a
   * precision the page did not have: an agent that spoke a second ago and one
   * that spoke four minutes ago pulsed identically. Sol declined to animate it
   * on exactly that ground, which was the right call, and this is the missing
   * half.
   *
   * Buckets, not a continuous value, because they are what actually differ to
   * a person watching: something is happening RIGHT NOW / this is a working
   * agent / this one has gone quiet but has not timed out. The thresholds line
   * up with how the age string itself reads, so the mark and the number can
   * never disagree.
   */
  function recency(iso) {
    if (!iso) return "unknown"
    const s = (Date.now() - new Date(iso).getTime()) / 1000
    if (!isFinite(s) || s < 0) return "unknown"
    if (s < 20) return "immediate"   // mid-exchange: the age still reads "now"
    if (s < 300) return "recent"     // working: minutes, single digits
    return "aging"                   // quiet, but not yet stale
  }

  /**
   * The cadence trace: when this lane actually did something, drawn discretely.
   *
   * The board says what an agent IS, active, dormant, out of touch, and that
   * is a state, checked against a timeout. It never showed the shape of the
   * work, and the shape is what a person actually reads a fleet by: steady
   * rhythm, a burst then nothing, or a lane that claims to be active and has
   * not moved in eight minutes. The last of those is a stalled agent, and until
   * now it looked exactly like a healthy one between turns.
   *
   * Discrete marks, NOT a smoothed sparkline. Coordination events are discrete
   *, a claim, a message, an acknowledgement, and drawing them as a continuous
   * curve would invent values between them and imply a rate that was never
   * measured. A chart may be beautiful or honest; where they conflict here, the
   * instrument has to be honest, and the density of real marks turns out to
   * read better than an interpolation anyway.
   *
   * Rendered only when there is something to render. An empty strip would say
   * "measured, and nothing happened", which is a different and much stronger
   * claim than "this lane predates the window".
   */
  const CADENCE_WINDOW = 10 * 60 * 1000
  const CADENCE_BINS = 24

  function cadenceHTML(laneID, events, now = Date.now()) {
    if (!laneID || !Array.isArray(events)) return ""
    const bins = new Array(CADENCE_BINS).fill(0)
    let total = 0, newest = Infinity, mail = 0
    for (const e of events) {
      if (e.lane !== laneID) continue
      const t = new Date(e.ts).getTime()
      if (!isFinite(t)) continue
      const age = now - t
      if (age < 0 || age > CADENCE_WINDOW) continue
      // Oldest bin on the left, newest on the right. Clamped because an event
      // arriving during this very millisecond computes to exactly CADENCE_BINS.
      const i = Math.min(CADENCE_BINS - 1, Math.floor((1 - age / CADENCE_WINDOW) * CADENCE_BINS))
      bins[i]++
      total++
      if (age < newest) newest = age
      if (String(e.type || "").startsWith("message")) mail++
    }
    if (!total) return ""

    const peak = Math.max(...bins)
    const secs = Math.round(newest / 1000)
    const label = `${total} ${total === 1 ? "action" : "actions"} in the last 10 minutes` +
      (mail ? `, ${mail} of them mail` : "") +
      (secs < 60 ? `, most recent ${secs} seconds ago` : `, most recent ${Math.round(secs / 60)} minutes ago`)

    return `<div class="cadence" role="img" aria-label="${esc(label)}">` +
      bins.map((n) => {
        if (!n) return `<i></i>`
        // Height is sqrt-scaled against the lane's own peak. Linear would make
        // a single action invisible next to a burst of twenty-five, and the
        // question a reader is asking is "did anything happen here", not "how
        // does this second compare to that one".
        return `<i style="--h:${(Math.sqrt(n / peak)).toFixed(3)}"></i>`
      }).join("") + "</div>"
  }

  function laneHTML(l, { selfId = null, events = null } = {}) {
    const st = l.status || "dormant"
    const self = selfId != null && l.id === selfId
    const tasks = (l.slots || []).map((s) => `
      <div class="task">
        <p>${esc(s.text)}</p>
        ${(s.refs || []).length || (s.dirs || []).length ? `<div class="paths">
          ${(s.refs || []).map((r) => `<span class="path">${esc(r)}</span>`).join("")}
          ${(s.dirs || []).map((d) => `<span class="path dir">${esc(d)}/</span>`).join("")}
        </div>` : ""}
      </div>`).join("")

    return `
      <article class="entry ${esc(st)}${self ? " self" : ""}" data-lane="${esc(l.id)}" data-recency="${recency(l.last_coordination_at)}">
        <div class="entry-head">
          <span class="pip"></span>
          <span class="name">${esc(l.display_name || l.id || l.name)}</span>
          ${l.display_name ? explained("tag", l.id, "its addressable id: ids must be ASCII, and nothing in that name survived") : ""}
          ${self ? '<span class="tag self">This lane</span>' : ""}
          ${l.kind === "persistent" ? '<span class="tag">Standing</span>' : ""}
          ${agentBadges(l)}
          ${staleReasonHTML(l)}
          ${cadenceHTML(l.id, events)}
          <time class="age" datetime="${esc(l.last_coordination_at || "")}">${esc(ago(l.last_coordination_at))}</time>
        </div>
        ${l.description ? `<p class="about">${esc(l.description)}</p>` : ""}
        ${tasks}
        ${identHTML(l.agent)}
      </article>`
  }

  // Grouped by COORDINATION STATE. At fifteen lanes an ungrouped list buries
  // the two that matter under thirteen that do not.
  //
  // The labels used to say "Working" and "Idle", which are claims about what an
  // agent is DOING, and the grouping is on status, which is a claim about
  // whether it is talking to Lanes. An active agent that has declared nothing
  // was labelled "Working"; a dormant standing reviewer, exactly where it is
  // meant to be between activations, was labelled "Idle" as though something
  // were wrong. Lanes knows an agent has spoken. It does not know what it is
  // doing, and this board must not say otherwise (SPEC §7).
  function rosterHTML(lanes, { selfId = null, empty = "", events = null } = {}) {
    if (!lanes || !lanes.length) return empty
    // Ordered by WHAT NEEDS A PERSON, not by status name.
    //
    // "Out of touch" sat third, below Working and Idle, so the one group that
    // might want a human was the one you had to scroll past two healthy groups
    // to reach, on a board whose entire job is telling you where to look. An
    // agent in that group stopped coordinating and may still be holding an
    // exclusive claim; the others are, by definition, fine.
    //
    // Careful about what that means, because the board must not overstate it:
    // stale is loss of COORDINATION, never proof of a crash (SPEC §7). Putting
    // it first says "look here first", which is true. It does not say "this is
    // broken", which would not be.
    const groups = [
      ["gone", "Out of touch", lanes.filter((l) => l.status === "stale")],
      ["working", "Active", lanes.filter((l) => l.status === "active")],
      ["idle", "Dormant", lanes.filter((l) => l.status === "dormant")],
      // Anything the server adds later still has to appear, or the board
      // silently under-reports the fleet.
      ["other", "Other", lanes.filter((l) =>
        !["active", "dormant", "stale"].includes(l.status))],
    ]
    // Each band is a real heading owning a real group, so an agent's status is
    // carried by structure and not only by where it happens to sit and what
    // colour the divider is. A screen reader can jump between groups; a sighted
    // reader sees exactly what they saw before.
    return groups.filter(([, , ls]) => ls.length).map(([cls, label, ls]) => `
      <section class="band-group" aria-labelledby="band-${cls}">
        <h2 class="band ${cls}" id="band-${cls}">
          <span>${esc(label)}</span><span class="n">${ls.length}</span><span class="hr"></span>
        </h2>
        ${ls.map((l) => laneHTML(l, { selfId, events })).join("")}
      </section>`).join("")
  }

  // The reader is a human, so an agent is named like everyone else: second
  // person would ask the reader to be the agent.
  //
  // `actionsHTML` is a caller-supplied function, because who may act differs
  // per surface: the panel can answer as its own lane, the god view watches.
  // Attachments were carried by the server and rendered by nobody.
  //
  // A message can attach a blob or a fileref, AllMessages returns them, and
  // both human surfaces dropped the field on the floor, so a message reading
  // "review the attached evidence" appeared to have no attachment, and an
  // operator had no way to learn that one existed, how many, or which handle
  // the recipient was given. That is a false picture of the message, not a
  // missing nicety.
  //
  // Handles are shown truncated: a sha256 content address is 71 characters and
  // would dominate the message, but the leading digits are what a reader
  // matches against get_blob output. A fileref shows its path, and is marked as
  // one, because Lanes never opened it and cannot vouch that it is there.
  function attachmentsHTML(atts) {
    if (!Array.isArray(atts) || atts.length === 0) return ""
    // The FULL handle goes in the DOM and CSS narrows it, rather than slicing
    // the string here and putting the rest in a title. A title reaches only a
    // reader with a mouse who waits, which the shared library forbids, and it
    // is the wrong tool anyway: this is the datum, not an explanation of a
    // mark. Left in full it stays selectable, copyable, findable with the
    // browser's own search, and readable to a screen reader, while showing the
    // leading characters that match get_blob output.
    const one = (a) => {
      const size = a.size ? ` <span class="size">${esc(fmtBytes(a.size))}</span>` : ""
      if (a.blob) {
        return `<li class="att blob">
          <span class="kind">blob</span> <code class="handle">${esc(a.blob)}</code>${size}</li>`
      }
      return `<li class="att fileref">
        <span class="kind">file</span> <code class="handle">${esc(a.path || ", ")}</code>${size}
        <span class="advisory">not verified by Lanes</span></li>`
    }
    return `<ul class="atts" aria-label="${atts.length} attachment${atts.length === 1 ? "" : "s"}">
      ${atts.map(one).join("")}</ul>`
  }

  function fmtBytes(n) {
    if (n < 1024) return `${n} B`
    if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
    return `${(n / (1024 * 1024)).toFixed(1)} MB`
  }

  function messageHTML(m, { selfId = null, actionsHTML = null } = {}) {
    const t = m.type || "notify"
    // `response` is a STRING; an earlier version read `.body` / `.disposition`
    // off it (both always undefined) so every answered message rendered an
    // empty reply block. The disposition lives in `state`.
    const settled = TERMINAL.has(m.state)
    const verdict = VERDICT[m.state] || m.state
    // A deadline that has passed means this is NOT in flight.
    //
    // An open message animates a light travelling along the wire, which reads
    // as "moving, on its way". For a message whose deadline went by an hour ago
    // that is a picture of progress over something stalled: the same
    // confidently-wrong shape this codebase keeps removing, in motion instead of
    // in words. Overdue goes still, and says so.
    const overdue = !settled && m.deadline && new Date(m.deadline) < new Date()
    const who = (id) =>
      `<span class="who${selfId != null && id === selfId ? " focal" : ""}">${esc(id || ", ")}</span>`
    return `
      <article class="msg ${settled ? "" : "open"}${overdue ? " overdue" : ""}">
        <div class="msg-head">
          <span class="serial">#${esc(m.serial ?? "")}</span>
          <span class="kind ${esc(t)}">${esc(t)}</span>
          ${overdue ? explained("pill attn", "past its deadline", "the deadline on this message has passed and nobody has answered. Lanes is still waiting, but nothing is in flight") : ""}
        </div>
        <div class="route">
          ${who(m.from)}<span class="wire"></span><span class="arrow">▶</span>${who(m.to)}
        </div>
        <p class="body">${esc(m.body)}</p>
        ${attachmentsHTML(m.attachments)}
        ${settled ? `<div class="reply ${esc(m.state)}">
          <span class="verdict">${esc(verdict)}</span>
          ${m.response ? esc(m.response) : '<span class="none">No message given</span>'}
        </div>` : ""}
        ${actionsHTML ? actionsHTML(m) : ""}
      </article>`
  }

  function eventHTML(e) {
    const kind = String(e.type || "")
    const cls = kind.startsWith("message") ? "msg-ev" : kind.startsWith("claim") ? "claim-ev" : ""
    const when = e.ts
      ? new Date(e.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })
      : ""
    return `
      <div class="event ${esc(cls)}">
        <span class="e-serial">#${esc(e.serial ?? "")}</span>
        <span class="e-when">${esc(when)}</span>
        <span class="e-lane">${esc(e.lane || ", ")}${e.to ? ` <i>▶</i> ${esc(e.to)}` : ""}</span>
        <span class="e-type">${esc(kind)}</span>
      </div>`
  }

  // Four numbers on one rule. This is the line a person reads first and it
  // must survive being glanced at, so it carries no prose.
  /**
   * Four readings, weighted by whether they are news.
   *
   * Every figure used to be typeset identically, so "0 unanswered": the best
   * possible answer, and the one a reader never needs to act on: carried the
   * same weight as "2 out of touch", which is the whole reason to look. A row
   * where three of four cells are zeros then spends most of its size saying
   * nothing is wrong, in the same voice it would use to say something is.
   *
   * A zero with no attention tone is marked so it can recede. Nothing is hidden:
   * the reading is still there, still legible, still announced to a screen
   * reader by the aria-label the caller builds. It simply stops competing.
   */
  function summaryHTML(metrics) {
    return metrics.map(({ figure, label, tone }) => {
      const quiet = !tone && /^0(<|$)/.test(String(figure))
      return `
      <div class="metric${quiet ? " quiet" : ""}">
        <span class="figure ${tone || ""}">${figure}</span>
        <span class="label">${esc(label)}</span>
      </div>`
    }).join("")
  }

  /**
   * A mark and the reason for it, reachable by everyone.
   *
   * Every explanation on this board: why an agent is stale, what "blocked"
   * means, what a match scored, who owns a lane exclusively: lived ONLY in a
   * `title` on a non-focusable span. title is a mouse affordance: it does not
   * appear on touch at all, keyboard users cannot summon it, and screen-reader
   * support for it is inconsistent enough that nothing important should depend
   * on it. SPEC-CHANNELS §10.3 asks the board to explain its marks, and the
   * explanations were only reachable if you happened to be holding a mouse.
   *
   * So the reason goes in the accessibility tree as well: hidden from the eye,
   * read aloud with the label, and still available on hover for the mouse.
   *
   * Honest about what this does not solve: a sighted touch user still has only
   * the visible label. Fixing that means a tap target on every mark, which is
   * more chrome than these marks are worth: the labels are written to stand
   * alone, and the reason is elaboration rather than the only information.
   */
  /**
   * A mark that can say what it means.
   *
   * The board's vocabulary is tiny coloured pills. "blocked", "exclusive",
   * "3 awaiting ack", and a person seeing one for the first time has no way
   * to derive it. That is what `why` is for.
   *
   * It used to be delivered by `title`, which reaches exactly one kind of
   * reader: someone with a mouse, willing to hover and wait. It does not
   * appear on touch at all, it does not appear on keyboard focus, it cannot be
   * styled, and it is announced inconsistently between screen readers. So the
   * explanation existed, was written carefully, and most people could not get
   * at it: the same shape of bug as a documented feature nothing calls.
   *
   * Now the text is carried as data and rendered by the shared explainer (see
   * Board.explainer) into ONE popover in the top layer. The `.sr-only` copy
   * stays: a popover shown on hover is not announced, so assistive tech reads
   * the mark's own text and the pointer/keyboard reader gets the popover.
   *
   * The mark is focusable, which is what makes it reachable without a mouse.
   * That is a real cost in tab stops and it is bounded by design: marks
   * carrying an explanation are overwhelmingly the CONDITIONAL ones (blocked,
   * overdue, abandoned), so a healthy board has almost none and a board in
   * trouble puts the trouble on the tab path. Which is where it belongs.
   */
  function explained(cls, label, why) {
    if (!why) return `<span class="${cls}">${esc(label)}</span>`
    return `<span class="${cls}" data-why="${esc(why)}" tabindex="0">` +
      `${esc(label)}<span class="sr-only">. ${esc(why)}</span></span>`
  }

  function emptyHTML(title, body) {
    return `<div class="empty"><h3>${esc(title)}</h3><p>${esc(body)}</p></div>`
  }

  /**
   * The board before anything has ever happened on it.
   *
   * This is the first thing a person sees: the moment they decide whether the
   * thing is real, and it used to be the word "No lanes", a sentence, and a
   * row of four zeros. All true, and none of it a way forward: somebody who has
   * just started the daemon does not need to be told the board is empty, they
   * can see that. They need the next command.
   *
   * Distinguished from "nothing outstanding", which is a GOOD state on a board
   * that has been running all day and must not be dressed up as a problem.
   */
  function firstRunHTML() {
    return `
      <div class="firstrun">
        <h3>Nothing has registered yet</h3>
        <p>Lanes is running and waiting. Agents appear the moment one connects,
           this board updates live, so leave it open.</p>
        <p class="firstrun-do">Point an agent at it:</p>
        <code class="firstrun-cmd">lanes mcp-config</code>
        <p class="firstrun-note">Prints the MCP config for your host. Matching,
           two agents finding each other on the same work: needs a repository
           and a measured threshold; <code>lanes calibrate</code> reports both.</p>
      </div>`
  }

  /**
   * One channel of work: who is in it, who owns it, who is waiting.
   *
   * A CHANNEL is what SPEC-CHANNELS.md calls a lane, and it is NOT the thing
   * laneHTML draws: that one draws an agent. The two coexist until the
   * Lane→Agent rename, so the names here are deliberately unambiguous even
   * though they read oddly side by side.
   *
   * `selfId` is the reading agent, or null on the operator view, which has no
   * "you" and must not invent one.
   */
  /**
   * A badge for what an agent IS, when it is not the default.
   *
   * Two facts change how a reader should treat a row and are invisible without
   * this: a coordinator can act on lanes it does not own, and a subagent's work
   * is really its parent's, so seeing three "agents" where one is a helper of
   * another would overstate how crowded the board is.
   */
  function agentBadges(l) {
    let out = ""
    if (l.role && l.role !== "member") {
      out += explained("badge role", l.role, "granted by a human; can administer lanes")
    }
    if (l.parent) {
      out += explained("badge sub", `↳ ${l.parent}`, `subagent of ${l.parent}: inherits its lanes`)
    }
    return out
  }

  // Who in this lane is actually working.
  //
  // A lane's member list is the answer to "who is on this", and rendering a
  // corpse identically to a live agent makes it the wrong answer. Observed: a
  // lane whose exclusive owner had crashed showed `crashed` and `reviewer` as
  // two identical chips, and the only way to learn that one of them was dead
  // was to cross-reference the roster on another tab.
  //
  // `agents` is the roster; without it this degrades to what it did before
  // rather than inventing a status it does not have.
  function memberStateHTML(agent, agents) {
    const l = (agents || []).find((x) => x.id === agent)
    if (!l || l.status === "active") return ""
    const why = STALE_WHY[l.stale_reason]
    const label = l.status === "dormant" ? "asleep" : (why ? why[0] : "out of touch")
    return explained("member-tag gone", label, why ? why[1] : "this member is not currently working")
  }

  function channelHTML(ch, { selfId = null, agents = null } = {}) {
    const members = ch.members || []
    const mine = selfId && members.some((m) => m.agent === selfId)
    const owned = !!ch.owner
    const queue = ch.queue || []
    const unacked = ch.unacked_announcements || 0
    // Two different states, deliberately not one number. "waiting" means Lanes
    // is still asking; "abandoned" means it gave up and nobody ever answered,
    // which is the one a person has to act on, because nothing else will.
    const abandoned = ch.abandoned_announcements || 0
    // A third state. "Waiting" and "waiting on somebody who is not there" look
    // identical on a board and are not the same problem: redelivery is driven
    // by the agent polling, so an announcement owed only by sleeping or crashed
    // agents never spends its retry budget and never reaches "unanswered": it
    // waits forever, looking healthy.
    const blocked = ch.blocked_announcements || 0
    // Members that left owing an acknowledgement. Recorded, not alarmed about:
    // their requirement had to be dropped or the lane would wait forever on
    // somebody who is not coming back, but "they never read it" stays true and
    // the sender may want to say it again to whoever replaced them.
    const departed = ch.departed_unacked || 0

    // An auto-joined member carries the score that put it there. Showing it is
    // not decoration: §10.3 requires every auto-join to be explainable, and a
    // number the human can see is the cheapest form of that.
    const roster = members.map((m) => {
      const tag = m.agent === ch.owner ? "owner" : m.auto ? "auto" : ""
      const gone = memberStateHTML(m.agent, agents)
      // Why this agent is in this lane. For an AUTO join this is the whole
      // provenance: score, the bar it cleared, which scorer said so, and the
      // files that drove it, and SPEC-CHANNELS §10.3 requires it be
      // explainable.
      const reason = m.score
        ? `matched ${m.score.toFixed(3)} ≥ ${(m.threshold || 0).toFixed(3)} via ${m.scorer || "an unnamed scorer"}${
            (m.evidence || []).length ? ": shared " + (m.evidence || []).slice(0, 3).join(", ") : ""}`
        : ""
      // Carried the same way every other mark carries its meaning. This site
      // hand-rolled its own `title` instead of going through explained(), so
      // when explanations moved to the popover it silently stayed behind on
      // the one mark that SPEC-CHANNELS §10.3 actually requires be
      // explainable: the evidence for an automatic join.
      const why = reason ? ` data-why="${esc(reason)}" tabindex="0"` : ""
      return `<span class="member${m.agent === selfId ? " self" : ""}${gone ? " gone" : ""}"${why}>${esc(m.agent)}${
        tag ? `<span class="member-tag">${tag}</span>` : ""}${gone}${
        reason ? `<span class="sr-only">. ${esc(reason)}</span>` : ""}</span>`
    }).join("")

    return `
      <article class="channel${owned ? " exclusive" : ""}${mine ? " mine" : ""}">
        <header>
          <span class="channel-id">${esc(ch.id)}</span>
          ${owned ? explained("pill warn", "exclusive", `held exclusively by ${ch.owner}: coordinate with them before working here`) : ""}
          ${unacked ? explained("pill attn", `${unacked} awaiting ack`, "announcements still awaiting acknowledgement") : ""}
          ${departed ? explained("pill quiet", `${departed} left unread`, "left this lane still owing an acknowledgement: their requirement was dropped so the lane could settle, but they never read it") : ""}
          ${blocked ? explained("pill blocked", `${blocked} blocked`, "every agent that still owes these is asleep or gone, so nothing will arrive until one of them comes back. Lanes will keep waiting, but it is not waiting on anyone who can answer") : ""}
          ${abandoned ? explained("pill abandoned", `${abandoned} unanswered`, "Lanes stopped asking and nobody ever acknowledged: this needs a person") : ""}
          <span class="grow"></span>
          <span class="count">${members.length} in</span>
        </header>
        ${ch.topic ? `<p class="channel-topic">${esc(ch.topic)}</p>` : ""}
        <div class="members">${roster || `<span class="member empty">nobody</span>`}</div>
        ${queue.length
          ? `<p class="channel-queue">waiting: ${queue.map((q) => esc(q)).join(" · ")}</p>`
          : ""}
        ${saidHTML(ch.said)}
      </article>`
  }

  /**
   * What has been ANNOUNCED in a lane.
   *
   * The board used to show membership and a count ("1 awaiting ack") which
   * tells an operator that something is outstanding and not what it is. A human
   * could join a lane, broadcast into it, and have no way anywhere in the
   * interface to read the announcement they had just sent, let alone the ones
   * the agents had sent each other. Joining a lane to watch the work is the
   * whole reason a human joins a lane.
   *
   * Newest last, so it reads like a conversation rather than a feed.
   */
  function saidHTML(said) {
    if (!said || !said.length) return ""
    return `<ol class="channel-said">${said.map((a) => `
      <li class="${a.owed > 0 ? "owed" : "settled"}">
        <span class="said-from">${esc(a.from)}</span>
        <span class="said-body">${esc(a.body)}</span>
        <span class="said-state">${a.owed > 0
          ? `${a.owed} yet to acknowledge`
          : "acknowledged"}</span>
      </li>`).join("")}</ol>`
  }

  /** The channel list, or an honest empty state. */
  function channelsHTML(channels, { selfId = null, empty = "", agents = null } = {}) {
    if (!channels || !channels.length) return empty
    return channels.map((c) => channelHTML(c, { selfId, agents })).join("")
  }

  // The stagger used to live here: a loop that wrote an inline
  // animation-delay onto every child on every redraw. It is now one line of
  // CSS using sibling-index() (board.css, "The stagger, without JavaScript").
  // Kept as a no-op rather than removed outright would have been the coward's
  // version: an inline style beats the stylesheet, so leaving it would have
  // silently overridden the rule that replaced it. Which it did, until this
  // was actually deleted.


  /**
   * Install the shared explainer: one popover, every mark.
   *
   * A popover per mark would mean a hundred elements in the top layer and a
   * hundred that die on the next redraw: both surfaces replace their HTML
   * wholesale when the board changes. One element outside the redrawn region,
   * re-anchored as the pointer moves, survives all of it and costs nothing.
   *
   * CSS anchor positioning does the placement, so the popover follows its mark
   * without measuring anything in JavaScript and without a scroll listener.
   * The mark lends its `anchor-name` while it is being explained and takes it
   * back afterwards: leaving it set would make every mark on the board an
   * anchor of the same name, and the resolution between them is not something
   * to rely on.
   *
   * Returns a teardown, because the panel is a guest that can be closed.
   */
  function explainer(root = document) {
    const tip = document.createElement("div")
    tip.id = "lanes-why"
    tip.setAttribute("popover", "manual")
    tip.setAttribute("role", "tooltip")
    document.body.appendChild(tip)

    let anchor = null
    let by = null      // "pointer" or "focus": how the current mark was reached
    let at = { x: 0, y: 0 }

    // A coordination board redraws whenever ANY agent does anything, and both
    // surfaces redraw by replacing their HTML, so the mark being read is
    // destroyed and rebuilt as a different node several times a minute. The
    // first version of this simply closed the popover when its anchor left the
    // DOM, which meant the explanation was yanked away mid-sentence every time
    // the fleet did something. On the one screen whose entire purpose is
    // watching a fleet do something.
    //
    // So a redraw re-acquires rather than closes. The pointer is exact,
    // whatever is under it now is what the reader is pointing at. Focus has no
    // such coordinate, so the equivalent mark is found by what it says and
    // focus is put back on it, which also repairs the tab position the redraw
    // would otherwise have thrown away.
    const watch = new MutationObserver(() => {
      if (!anchor || anchor.isConnected) return
      const said = anchor.dataset.why
      const again = by === "pointer"
        ? document.elementFromPoint(at.x, at.y)?.closest?.("[data-why]")
        : [...document.querySelectorAll("[data-why]")].find((el) => el.dataset.why === said)
      anchor = null                       // the old node is gone; do not touch its style
      if (!again || again.dataset.why !== said) { hide(); return }
      adopt(again)
      if (by === "focus") again.focus({ preventScroll: true })
    })

    // adopt points the popover at a node without re-running the open sequence,
    // so a redraw does not restart the fade: the reader should not be able to
    // tell that the thing they are reading was rebuilt underneath them.
    function adopt(el) {
      anchor = el
      el.style.anchorName = "--lanes-why"
    }

    function show(el, how) {
      by = how
      if (el === anchor) return
      hide()
      adopt(el)
      tip.textContent = el.dataset.why
      try { tip.showPopover() } catch { return }
      watch.observe(document.body, { childList: true, subtree: true })
    }
    function hide() {
      watch.disconnect()
      if (anchor) anchor.style.anchorName = ""
      anchor = null
      try { tip.hidePopover() } catch { /* already closed with its host */ }
    }
    const mark = (e) => e.target?.closest?.("[data-why]")

    const onOver = (e) => {
      at = { x: e.clientX, y: e.clientY }
      const m = mark(e)
      if (m) show(m, "pointer"); else hide()
    }
    const onOut = (e) => { if (anchor && !anchor.contains(e.relatedTarget)) hide() }
    const onIn = (e) => { const m = mark(e); m ? show(m, "focus") : hide() }
    // Escape closes it without moving focus: the reader is mid-scan and being
    // thrown back to the top of the board is a worse outcome than the popover.
    const onKey = (e) => { if (e.key === "Escape" && anchor) { e.stopPropagation(); hide() } }

    root.addEventListener("pointerover", onOver)
    root.addEventListener("pointerout", onOut)
    root.addEventListener("focusin", onIn)
    root.addEventListener("keydown", onKey, true)

    return () => {
      hide()
      root.removeEventListener("pointerover", onOver)
      root.removeEventListener("pointerout", onOut)
      root.removeEventListener("focusin", onIn)
      root.removeEventListener("keydown", onKey, true)
      tip.remove()
    }
  }


  /**
   * Run an update inside a View Transition, or run it plainly.
   *
   * Shared because coherence is the point: the panel had view transitions, a
   * reduced-motion guard and a feature check, and the web board: the same
   * components, the same design system, the same product: redrew by
   * replacing its HTML with no continuity at all. Two surfaces wearing one
   * name is exactly the drift the design system exists to prevent, and it is
   * as true of motion as it is of colour.
   *
   * `kind` lands on <html data-transition> so a surface can name what is
   * moving and style the transition accordingly, and is removed afterwards so
   * it never describes a transition that already finished.
   *
   * Motion is decoration here and never the only carrier of meaning: if the
   * reader prefers reduced motion, the API is missing, or one is already
   * running, the update simply happens. The caller is told which it got.
   */
  const reducedMotion = typeof matchMedia === "function"
    ? matchMedia("(prefers-reduced-motion: reduce)")
    : { matches: false }
  let running = null

  function transition(kind, update) {
    const root = document.documentElement
    if (typeof document.startViewTransition !== "function" ||
        reducedMotion.matches || running) {
      update(false)
      return false
    }
    root.dataset.transition = kind
    try {
      running = document.startViewTransition(() => update(true))
    } catch {
      delete root.dataset.transition
      update(false)
      return false
    }
    running.finished.finally(() => {
      running = null
      delete root.dataset.transition
    })
    return true
  }


  /**
   * Keep the time-derived marks honest while nothing is happening.
   *
   * Both surfaces redraw when the board CHANGES, and never otherwise, so an
   * age rendered as "now" stayed "now", for as long as the fleet stayed quiet.
   * On a board whose entire purpose is answering "is that agent still alive",
   * the one field that decays with no event to announce it was the one field
   * nothing ever refreshed.
   *
   * It became urgent when liveness started encoding recency: a lane drawn at
   * `immediate` would go on pulsing as though mid-exchange an hour after its
   * last word. That is exactly the false precision Sol refused to fake when
   * the data was missing, arriving instead through a stale DOM: a mark that
   * lies is worse than a mark that says nothing.
   *
   * Deliberately NOT a redraw. It rewrites the two derived values in place, so
   * it cannot disturb a draft being typed, a popover being read, or where the
   * focus is: all of which a redraw costs. The interval is the finest bucket
   * boundary over four, which is enough to make the change look continuous and
   * cheap enough to leave running on a laptop all day.
   */
  function ticker(root = document, every = 5000) {
    const tick = () => {
      for (const el of root.querySelectorAll("time.age[datetime]")) {
        const iso = el.getAttribute("datetime")
        if (!iso) continue
        const now = ago(iso)
        if (el.textContent !== now) el.textContent = now
        const entry = el.closest("[data-recency]")
        if (!entry) continue
        const bucket = recency(iso)
        if (entry.dataset.recency !== bucket) entry.dataset.recency = bucket
      }
    }
    tick()
    const id = setInterval(tick, every)
    return () => clearInterval(id)
  }

  return {
    esc, ago, TERMINAL, VERDICT,
    identHTML, laneHTML, cadenceHTML, rosterHTML, messageHTML, eventHTML, agentBadges, explained, explainer, transition, ticker, recency,
    channelHTML, channelsHTML,
    summaryHTML, emptyHTML, firstRunHTML,
  }
})()
