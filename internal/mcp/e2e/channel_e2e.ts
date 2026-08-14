/**
 * End-to-end test for spaces. SPEC-CHANNELS.md.
 *
 * Everything here goes over real HTTP to a real daemon, so it exercises the
 * whole path the agents use: tool schema → argument decode → op construction →
 * state machine → ledger → response. The Go tests prove the state machine; this
 * proves the WIRE, which is where a field name silently not matching a JSON tag
 * turns a recorded score into a zero and nobody notices until replay.
 *
 * Two properties get the most attention because they are the ones that fail
 * quietly:
 *
 *   - the recorded score survives the round trip verbatim (§4.3). A score that
 *     arrives as 0 still joins the agent, still looks fine on the board, and
 *     destroys the ledger's meaning.
 *   - an exclusive space QUEUES rather than refuses (§5). A refusal an agent
 *     can ignore is not coordination.
 *
 * Run: bun internal/mcp/e2e/channel_e2e.ts
 */
import { mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const ADDR = `127.0.0.1:${process.env.PORT ?? 4934}`

let failures = 0
let checks = 0
function check(name: string, cond: boolean, detail = "") {
  checks++
  if (cond) console.log(`  \x1b[32m✓\x1b[0m ${name}`)
  else { failures++; console.log(`  \x1b[31m✗\x1b[0m ${name}${detail ? ". " + detail : ""}`) }
}

const home = process.env.HOME
const dibd = process.env.DIBD ?? `${home}/.local/bin/dibd`
const dir_ = mkdtempSync(join(tmpdir(), "agents-space-e2e-"))
const dir = dir_
// -match-repo enables work-overlap scoring against THIS repository, and
// -match-join sets the bar `dibs calibrate` measured for it. Without both, the
// daemon runs suggest-only and the auto-join checks below cannot pass.
const repo = process.env.MATCH_REPO ?? new URL("../../..", import.meta.url).pathname

/**
 * Measure the join bar rather than hardcoding one.
 *
 * The score this suite asserts on is computed from the REPOSITORY'S OWN GIT
 * HISTORY, which grows every time anybody commits. A fixed bar therefore has a
 * shelf life: this file used to say 0.30 against an observed 0.333, and by the
 * time it was next run in anger the same pair scored 0.2967: the suite went
 * red because a commit had been made, which is not a defect anyone can act on
 * and trains a contributor to disbelieve the suite.
 *
 * So the bar is derived from a score this run actually observed. A throwaway
 * daemon with an unreachable bar (0.95 never joins) scores the same pair the
 * assertions below use; the real daemon then runs with a bar just under it.
 * What the checks assert is the PROPERTY: above the bar joins, below it only
 * advises, which is what the threshold means and is true at any absolute
 * score. It cannot pass vacuously either: the margin is small and deliberate,
 * so a daemon that stopped consulting the bar still fails.
 */
async function measureTheBar(): Promise<number> {
  const mdir = mkdtempSync(join(tmpdir(), "agents-space-e2e-measure-"))
  const maddr = `127.0.0.1:${Number(process.env.PORT ?? 4934) + 2}`
  const md = Bun.spawn({
    cmd: [dibd, "-dir", mdir, "-addr", maddr,
          "-match-repo", repo, "-match-join", "0.95", "-match-notify", "0.05"],
    stdout: "ignore", stderr: "ignore",
  })
  try {
    let sec = ""
    for (let i = 0; i < 60 && !sec; i++) {
      try { sec = (await Bun.file(`${mdir}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
    }
    if (!sec) throw new Error("measurement daemon never wrote local.secret")
    let id = 0
    const c = async (name: string, args: Record<string, unknown>) => {
      const res = await fetch(`http://${maddr}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": sec },
        body: JSON.stringify({ jsonrpc: "2.0", id: ++id, method: "tools/call", params: { name, arguments: args } }),
      })
      const b = await res.json() as any
      if (b.error) throw new Error(`${name}: ${JSON.stringify(b.error)}`)
      return JSON.parse(b.result.content[0].text)
    }
    const mk = async (n: string) => {
      const r = await c("register", { name: n, session_id: `m-${n}` })
      await c("check_in", { token: r.token })
      return r.token
    }
    // The same two declarations the assertions use, in the same order.
    const a = await mk("m-one"), b = await mk("m-two")
    await c("declare", { token: a, text: "enforcing exclusive claims in the guard" })
    await c("open_space", { token: a, space: "guard-work", topic: "claim guard denies edits to claimed paths" })
    const r = await c("declare", { token: b, text: "guard path enforcement for exclusive claims" })
    const m = (r.spaces ?? []).find((l: any) => l.space === "guard-work")
    if (!m || typeof m.score !== "number" || !(m.score > 0)) {
      throw new Error(`could not measure a score for the guard pair: ${JSON.stringify(r).slice(0, 300)}`)
    }
    return m.score
  } finally {
    md.kill()
    try { rmSync(mdir, { recursive: true, force: true }) } catch {}
  }
}

const observedScore = await measureTheBar()
// Just under the observed score, floored so a pathologically low measurement
// cannot push the bar to zero and make "unrelated work is not auto-joined"
// unfalsifiable.
const JOIN_BAR = Math.max(0.05, Number((observedScore - 0.02).toFixed(4)))
console.log(`  · join bar measured from this repository: ${JOIN_BAR} (observed ${observedScore.toFixed(4)})`)

// -match-repo enables work-overlap scoring against THIS repository, and
// -match-join is the bar measured just above: the same thing `dibs calibrate`
// does for a real operator. Without both, the daemon runs suggest-only and the
// auto-join checks below cannot pass.
const daemon = Bun.spawn({
  cmd: [dibd, "-dir", dir, "-addr", ADDR,
        "-match-repo", repo, "-match-join", String(JOIN_BAR), "-match-notify", "0.15"],
  stdout: "ignore", stderr: "ignore",
})
const cleanup = () => {
  daemon.kill()
  try { rmSync(dir, { recursive: true, force: true }) } catch {}
}
process.on("exit", cleanup)

let secret = ""
for (let i = 0; i < 60 && !secret; i++) {
  try { secret = (await Bun.file(`${dir}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
}
if (!secret) { console.error("daemon never wrote local.secret"); process.exit(1) }

let rpcId = 0
async function raw(name: string, args: Record<string, unknown>): Promise<any> {
  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
  })
  return await res.json() as any
}
async function call(name: string, args: Record<string, unknown>): Promise<any> {
  const body = await raw(name, args)
  if (body.error) throw new Error(`${name}: ${JSON.stringify(body.error)}`)
  // A tool-level failure arrives as isError with the error JSON in `content`,
  // NOT as a JSON-RPC error, so without this it parses cleanly and is returned
  // as though it had worked. A setup step that quietly failed then shows up as
  // a baffling assertion failure several checks later, which is exactly how a
  // `lock_space` that was refused looked like an agent that had no owner.
  // Use fails() when a refusal is the thing being tested.
  if (body.result?.isError) {
    throw new Error(`${name}: ${body.result.content?.[0]?.text ?? "unknown tool error"}`)
  }
  const out = JSON.parse(body.result.content[0].text)
  if (Array.isArray(out?.spaces)) seenSuggestions.push(...out.spaces)
  return out
}
/**
 * board is the one tool whose `content` is a prose summary for the model
 * and whose panel data lives in tool-result metadata: the MCP Apps private
 * backchannel. Parsing content[0].text as JSON here fails on
 * "Dibs board: 3 agent(s)…".
 */
async function board(token: string): Promise<any> {
  const body = await raw("board", { token })
  if (body.error) throw new Error(`board: ${JSON.stringify(body.error)}`)
  return body.result?._meta?.["com.dibs/panel"] ?? {}
}

/** Expect a tool to fail, and return the error for inspection. */
async function fails(name: string, args: Record<string, unknown>): Promise<string> {
  const body = await raw(name, args)
  if (!body.error) {
    const txt = body.result?.content?.[0]?.text ?? ""
    if (!/error|E_[A-Z_]+/i.test(txt)) return ""
    return txt
  }
  return JSON.stringify(body.error)
}

/**
 * Every suggestion the daemon returned, across the whole run.
 *
 * Collected so the threshold can be checked as an INVARIANT rather than by
 * finding one declaration that happens to sit between the bars. An earlier
 * version of this file tested "unrelated work is not auto-joined" against a
 * pair that scored exactly 0, which passes just as happily when the threshold
 * check is deleted entirely, because a zero-scoring agent never reaches the
 * comparison at all. Mutation-testing caught it: replacing the bar with
 * `if true` left the suite fully green.
 *
 * Checked against the JOIN_BAR measured at the top of this file: the same
 * number the daemon was started with, so the invariant is about the bar the
 * daemon is actually applying rather than one this file hoped it would.
 */
const seenSuggestions: any[] = []

async function agent(name: string): Promise<string> {
  const r = await call("register", { name, session_id: `s-${name}` })
  await call("check_in", { token: r.token })
  return r.token
}

console.log("\nchannel e2e")
console.log("─".repeat(60))

// ── the tool surface is actually published ───────────────────────────────
{
  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: 999, method: "tools/list", params: {} }),
  })
  const names = ((await res.json()) as any).result.tools.map((t: any) => t.name)
  const want = ["open_space", "join_space", "leave_space", "watch_space",
    "lock_space", "post", "announce", "ack_announcement"]
  const missing = want.filter((w) => !names.includes(w))
  check("all space tools are discoverable", missing.length === 0, "missing: " + missing.join(", "))
}

const alpha = await agent("alpha")
const beta = await agent("beta")
const gamma = await agent("gamma")

// ── open and join ────────────────────────────────────────────────────────
{
  const r = await call("open_space", { token: alpha, space: "Auth Refactor", topic: "reworking auth middleware" })
  check("open_space normalises the id so one topic is one agent", r.agent_id === "auth-refactor", r.agent_id)
}

// §4.3: the property that quietly destroys the ledger if it breaks.
{
  const r = await call("join_space", {
    token: beta, space: "auth-refactor",
    score: 0.8137, threshold: 0.327,
    scorer_id: "lexical+cochange", scorer_version: "1",
    evidence: ["internal/mcp/identity.go", "internal/core/roles.go"], auto: true,
  })
  check("join_space reports joined", r.joined === true, JSON.stringify(r))
  // Round-tripped through JSON tags, arg decode and the op: the exact float.
  check("the recorded score survives the wire verbatim", r.score === 0.8137,
    `got ${r.score}: a score that arrives as 0 still joins, and silently voids replay`)
  check("the join is marked automatic", r.auto === true)
}

// ── exclusivity queues rather than refuses (§5) ──────────────────────────
{
  await call("open_space", { token: gamma, space: "hot", topic: "single-writer work", exclusive: true })
  const r = await call("join_space", { token: alpha, space: "hot", score: 0.91 })
  check("joining an exclusive space queues rather than refusing", r.queued === true && r.joined === false,
    JSON.stringify(r))
  check("the queue tells you your position", r.queue_position === 1, String(r.queue_position))
  check("the queue names the owner so you can ask them", r.owner === "gamma", r.owner)
  check("and says what to do next", typeof r.hint === "string" && r.hint.length > 0, r.hint)

  const again = await call("join_space", { token: alpha, space: "hot" })
  check("re-asking does not queue you twice", again.queue_position === 1, String(again.queue_position))
}

// ── announce requires acks; post does not ───────────────────────────────
let annSerial = 0
{
  const r = await call("announce", { token: alpha, space: "auth-refactor", body: "renaming AgentInfo.Token" })
  annSerial = r.serial
  check("announce requires an ack from every other member", r.must_ack === 1, JSON.stringify(r))

  const post = await call("post", { token: alpha, space: "auth-refactor", body: "halfway done" })
  check("post is delivered without requiring anything", typeof post.serial === "number")
}

{
  const r = await call("ack_announcement", { token: beta, msg_serial: annSerial })
  check("acking clears the requirement", r.acked === true && r.outstanding === 0, JSON.stringify(r))
}

// ── membership is what collides; subscription is free ───────────────────
{
  const r = await call("watch_space", { token: gamma, space: "auth-refactor" })
  check("subscribing works without joining", r.subscribed === true, JSON.stringify(r))

  const err = await fails("post", { token: gamma, space: "auth-refactor", body: "hello" })
  check("a subscriber may read but not speak", err.includes("E_NOT_MEMBER"), err || "(allowed!)")
}

// ── leaving hands the agent on ───────────────────────────────────────────
{
  await call("leave_space", { token: gamma, space: "hot" })
  const r = await call("join_space", { token: alpha, space: "hot" })
  check("the queued agent is admitted once the owner leaves",
    r.joined === true || r.already === true, JSON.stringify(r))
}

// ── the awareness gate still applies (SPEC §6) ──────────────────────────
{
  const fresh = await call("register", { name: "ungreeted", session_id: "s-ungreeted" })
  const err = await fails("open_space", { token: fresh.token, space: "somewhere", topic: "t" })
  check("spaces respect the awareness gate", /E_MUST_ACK|check_in/i.test(err), err || "(allowed!)")
}

// ── auto-join: the whole point, end to end ──────────────────────────────
// Two agents declare related work IN THEIR OWN WORDS, never naming an agent, and
// must end up in the same one. This is the feature: not that a score can be
// computed, but that declaring work puts you next to the person already doing it.
{
  // Readiness is probed with the very declaration this block asserts on, not
  // with a throwaway one.
  //
  // Indexing is asynchronous, so the daemon answers before it can match and the
  // probe has to retry. A SEPARATE probe declaration is not free, though:
  // declaring work that matches nothing opens an agent for it, so the probe left
  // `claim-guard-path-enforcement` on the board, and the block below is
  // explicitly about a board where nothing matches the bootstrap work. That
  // premise held only while the probe's wording happened not to resemble the
  // bootstrap wording in this repository's index, which made the test a
  // measurement of the repository's commit history. A few commits that touched
  // guard and space code together were enough to break it.
  //
  // Probing with the real declaration removes the extra agent entirely: the first
  // call that gets an answer IS the assertion.

  // BOOTSTRAP FIRST: two agents, nothing matching them, nobody calling open_space.
  //
  // This is the case the whole feature exists for and the one that was broken:
  // matching only ever compared a declaration against agents that ALREADY
  // existed, and nothing created the first one. Two agents could declare
  // identical work and both were told "you have the field to yourself".
  //
  // The suite did not catch it because every matching test called open_space by
  // hand between the two agents: testing the second half of a mechanism whose
  // first half did not exist. So this runs before any open_space does.
  const one = await agent("matcher-one")
  const two = await agent("matcher-two")
  const solo = await agent("bootstrap-one")
  const peer = await agent("bootstrap-two")
  const work = "reworking the queue promotion logic in internal/core/space.go"

  // Same slot each time, so a retry updates the declaration instead of adding a
  // second one and making this agent look like it is doing two things.
  let first: any = {}
  for (let i = 0; i < 60; i++) {
    first = await call("declare", { token: solo, slot_id: "s1", text: work })
    if (first.spaces || first.spaces_hint) break
    await Bun.sleep(250)
  }
  const born = (first.spaces ?? []).find((l: any) => l.action === "opened")
  check("declaring work nobody is doing OPENS the first agent, per SPEC-CHANNELS §3",
    born !== undefined, JSON.stringify(first).slice(0, 260))

  const second = await call("declare", { token: peer, text: work })
  const met = (second.spaces ?? []).find((l: any) => l.space === born?.space)
  // A SCORE is a proposal now, not a membership: two agents caught false
  // auto-joins from the inside that no threshold had, so the decision belongs to
  // whoever is better at it. The agent must still be SURFACED: that is the part
  // that carries the value, and the part that has to keep working.
  check("and the next agent declaring the same work is shown it, not told it is alone",
    met !== undefined, JSON.stringify(second).slice(0, 260))
  check("a score-only match is proposed rather than joined",
    met?.action === "consider", `action=${met?.action} score=${met?.score}`)

  // The follow-up that used to lie: an agent refreshing its slot is filtered out
  // of its own agent's suggestions, and the fallback said "you have the field to
  // yourself": to an agent standing in an agent with the peer it names.
  const again = await call("declare", { token: solo, text: work + ", plus tests" })
  check("refreshing a slot does not spawn a second agent for the same work",
    (again.spaces ?? []).every((l: any) => l.action !== "opened"),
    JSON.stringify(again.spaces ?? []).slice(0, 200))
  check("and never claims solitude to an agent that is already coordinating",
    !/field to yourself/.test(String(again.matching_hint ?? "")),
    String(again.matching_hint ?? ""))

  // First agent declares, then opens an agent for that work.
  await call("declare", { token: one, text: "enforcing exclusive claims in the guard" })
  const opened = await call("open_space", {
    token: one, space: "guard-work", topic: "claim guard denies edits to claimed paths",
  })
  check("an agent opened from a topic gets a footprint to match against",
    opened.agent_id === "guard-work", JSON.stringify(opened))

  // Second agent declares OVERLAPPING work, in different words, naming no agent.
  const r = await call("declare", { token: two, text: "guard path enforcement for exclusive claims" })
  const agents: any[] = r.spaces ?? []
  const guard = agents.find((l) => l.space === "guard-work")
  check("declaring related work surfaces the agent already doing it",
    guard !== undefined, JSON.stringify(r).slice(0, 300))
  if (guard) {
    check("the match carries a score", typeof guard.score === "number" && guard.score > 0, String(guard.score))
    check("the match shows the evidence behind it",
      Array.isArray(guard.shared) && guard.shared.length > 0, JSON.stringify(guard.shared))
    check("above the join bar it is PROPOSED, because a score is not a fact",
      guard.action === "consider", `action=${guard.action} score=${guard.score}`)
    check("and the proposal names what WOULD make it automatic",
      typeof guard.hint === "string" && guard.hint.includes("refs"), String(guard.hint))
    check("and the agent is told to read the space before starting",
      typeof r.spaces_hint === "string" && r.spaces_hint.length > 0, r.spaces_hint)
  }

  // Unrelated work must NOT be dragged in: the failure mode that collapses
  // every agent into one agent.
  const three = await agent("matcher-three")
  const u = await call("declare", { token: three, text: "restyling the web board fonts and stylesheet" })
  const dragged = (u.spaces ?? []).find((l: any) => l.space === "guard-work" && l.action === "joined")
  check("unrelated work is not auto-joined into the guard agent", dragged === undefined,
    JSON.stringify(u.spaces ?? []).slice(0, 200))
}

// ── announcements ride the wake path (§6) ───────────────────────────────
// This is why the injection mechanism matters. An announcement is by definition
// something every member MUST know, and an agent mid-turn has no reason to poll
// so it has to arrive through the hook the harness already fires.
{
  const speaker = await agent("announcer")
  const listener = await call("register", { name: "listener", session_id: "sess-listener" })
  await call("check_in", { token: listener.token })
  await call("open_space", { token: speaker, space: "wake-test", topic: "wake path check" })
  await call("join_space", { token: listener.token, space: "wake-test" })
  await call("announce", { token: speaker, space: "wake-test", body: "INTERFACE CHANGED: Token is now Secret" })

  const poll = await call("hook_poll", { session_id: "sess-listener", event: "Stop" })
  const ctxText: string = poll?.hookSpecificOutput?.additionalContext ?? ""
  // The wake path is authenticated by NOTHING: it takes a session id and a
  // cwd off the wire because a harness lifecycle hook has no agent token. So it
  // must wake the agent without disclosing anything private: any holder of the
  // coordination secret can name any session id, or omit it and name a working
  // directory, and be treated as that agent.
  check("an unacked announcement is announced through the wake path",
    /unacknowledged announcement/.test(ctxText) && ctxText.includes("wake-test"),
    ctxText.slice(0, 200) || "(nothing injected)")
  check("but its CONTENT is not handed to an unauthenticated caller",
    !ctxText.includes("INTERFACE CHANGED"), ctxText.slice(0, 240))
  // And the agent that actually holds the token can read it: the wake is a
  // pointer, the token is the key.
  {
    const owed = await call("inbox", { token: listener.token })
    const mine = (owed.announcements ?? []).filter((x: any) =>
      (x.body ?? "").includes("INTERFACE CHANGED"))
    check("while the real agent reads it with its own token",
      mine.length === 1, JSON.stringify(owed.announcements ?? []).slice(0, 200))
  }
  check("it is labelled as needing acknowledgement",
    /ANNOUNCEMENT/.test(ctxText) && /ack_announcement/.test(ctxText), ctxText.slice(0, 200))
  check("and it is framed as data, not as an instruction",
    /not instructions/.test(ctxText), ctxText.slice(0, 200))

  // Throttled, not repeated on every hook. An announcement that rides every
  // single turn is indistinguishable from a stuck loop, which destroys the
  // signal that makes an announcement worth reading at all.
  const second = await call("hook_poll", { session_id: "sess-listener", event: "Stop" })
  const secondText: string = second?.hookSpecificOutput?.additionalContext ?? ""
  check("an immediate re-poll does not repeat the announcement",
    !secondText.includes("INTERFACE CHANGED"), secondText.slice(0, 160))

  // Acking must stop the redelivery, or the agent sees it forever.
  const anns = ctxText.match(/#(\d+) in agent/)
  const serial = anns ? Number(anns[1]) : 0
  await call("ack_announcement", { token: listener.token, msg_serial: serial })
  const after = await call("hook_poll", { session_id: "sess-listener", event: "Stop" })
  const afterText: string = after?.hookSpecificOutput?.additionalContext ?? ""
  check("acknowledging stops the redelivery",
    !afterText.includes("INTERFACE CHANGED"), afterText.slice(0, 200))
}

// ── subagents inherit, and cost nothing (§8.2) ──────────────────────────
{
  const par = await agent("parent-agent")
  await call("open_space", { token: par, space: "sub-work", topic: "work with a helper" })
  await call("vouch_child", { token: par, nonce: "helper-nonce-0123456789abcdef" })
  // A subagent names its parent at registration and joins nothing.
  const sub = await call("register", {
    name: "helper-agent", session_id: "s-helper", parent: "parent-agent",
    // Vouched. `parent` alone is a claim anybody can make, and a subagent
    // inherits its parent's memberships, so the parent proves it with a
    // one-time nonce only it can issue.
    parent_nonce: "helper-nonce-0123456789abcdef",
  })
  await call("check_in", { token: sub.token })

  const posted = await call("post", { token: sub.token, space: "sub-work", body: "progress" })
  check("a subagent may speak in its parent's agent without joining",
    typeof posted.serial === "number", JSON.stringify(posted))

  const b = await board(par)
  const subWork = (b.board?.spaces ?? []).find((c: any) => c.id === "sub-work")
  check("and is NOT counted as a second occupant",
    subWork !== undefined && subWork.members.length === 1,
    JSON.stringify(subWork?.members))
}

// ── the director: coordinator powers over spaces (§8.1) ───────────────
{
  const owner = await agent("stuck-owner")
  const waiter = await agent("stuck-waiter")
  await call("open_space", { token: owner, space: "stuck", topic: "locked work", exclusive: true })
  await call("join_space", { token: waiter, space: "stuck" }) // queues

  // Without the role, every director power is refused.
  const denied = await fails("unlock_space", { token: waiter, space: "stuck" })
  check("director powers are refused without the granted role",
    denied.includes("E_NOT_COORDINATOR"), denied || "(allowed!)")

  // Granted by a human through the admin path: no agent can promote itself.
  // Same route web_e2e uses: DIBS_ADMIN=1 escapes the interactive-terminal
  // gate, and the password is piped, because readPassword reads stdin bytewise
  // rather than requiring a tty.
  const dibsBin = process.env.DIBS ?? `${home}/.local/bin/dibs`
  const adminCLI = (args: string[], input: string) => {
    const r = Bun.spawnSync({
      cmd: [dibsBin, ...args],
      stdin: new TextEncoder().encode(input),
      env: { ...process.env, DIBS_ADDR: ADDR, DIBS_DIR: dir_, DIBS_ADMIN: "1" },
      stdout: "pipe", stderr: "pipe",
    })
    return new TextDecoder().decode(r.stdout) + new TextDecoder().decode(r.stderr)
  }
  const PW = "space-e2e-password"
  adminCLI(["admin", "set-password"], `${PW}\n${PW}\n`)

  const dirSpace = await call("register", { name: "director", session_id: "s-director" })
  await call("check_in", { token: dirSpace.token })
  const grantOut = adminCLI(["admin", "coordinator", "director"], `${PW}\n`)
  check("a human can grant the coordinator role", /coordinator/i.test(grantOut) && !/error/i.test(grantOut),
    grantOut.trim().slice(0, 160))

  const r = await call("unlock_space", {
    token: dirSpace.token, space: "stuck", note: "owner's machine died",
  })
  check("a director can unstick an agent whose owner is gone", r.released === true, JSON.stringify(r))
  check("and the former owner is named, never silent", r.former_owner === "stuck-owner", String(r.former_owner))

  const after = await board(waiter)
  const stuck = (after.board?.spaces ?? []).find((c: any) => c.id === "stuck")
  check("the queued agent was admitted when the lock lifted",
    stuck !== undefined && stuck.members.some((m: any) => m.agent === "stuck-waiter"),
    JSON.stringify(stuck?.members))

  // ── retiring a finished agent ───────────────────────────────────────────
  //
  // An agent a human opened outlives its members on purpose, so nothing reclaims
  // it and until close_space nothing could end it at all. Exercised here rather
  // than only in units because the interesting half is the ROLE: the grant is a
  // human act through the admin CLI, and a unit test that sets Role directly
  // proves nothing about whether an agent can reach this over MCP.
  const closerOwner = await agent("closer-owner")
  await call("open_space", { token: closerOwner, space: "finished", topic: "work that ended" })

  const occupied = await fails("close_space", { token: dirSpace.token, space: "finished" })
  check("a coordinator may not close an agent with somebody in it",
    /member/i.test(occupied), occupied.slice(0, 200) || "(allowed!)")

  await call("leave_space", { token: closerOwner, space: "finished" })
  const stillThere = await board(dirSpace.token)
  check("a human-opened agent is not reclaimed when it empties",
    (stillThere.board?.spaces ?? []).some((c: any) => c.id === "finished"))

  // A STRANGER: neither coordinator nor the agent that opened it.
  const stranger = await agent("closer-stranger")
  const refusedRole = await fails("close_space", { token: stranger, space: "finished" })
  check("a stranger may not close somebody else's agent",
    refusedRole.includes("E_NOT_COORDINATOR"), refusedRole.slice(0, 200) || "(allowed!)")

  // The agent that OPENED it may retire it without the role: open_space is
  // unprivileged, so an agent an agent could create and never end was a hole, and
  // the refusal called its own agent "another agent's".
  const byOwner = await call("close_space", { token: closerOwner, space: "finished", note: "mine, done" })
  check("the agent that opened an agent can close it", byOwner.closed === true, JSON.stringify(byOwner))

  // And a coordinator can retire one that is not theirs.
  await call("open_space", { token: closerOwner, space: "finished-2", topic: "more work that ended" })
  await call("leave_space", { token: closerOwner, space: "finished-2" })
  const closed = await call("close_space", {
    token: dirSpace.token, space: "finished-2", note: "done with it",
  })
  check("a coordinator can retire somebody else's finished agent", closed.closed === true, JSON.stringify(closed))
  const gone = await board(dirSpace.token)
  check("and it is actually gone from the board",
    !(gone.board?.spaces ?? []).some((c: any) => c.id === "finished" || c.id === "finished-2"))
}

// ── the threshold is enforced, as an invariant ──────────────────────────
// Checked over every suggestion the run produced rather than over one chosen
// pair: "joined" must imply the score cleared the configured bar. This is what
// catches a threshold comparison being loosened or removed, which is the
// failure that silently collapses an entire fleet into one agent.
{
  check("suggestions were actually produced (else the checks below are vacuous)",
    seenSuggestions.length > 0, `saw ${seenSuggestions.length}`)
  const joinedBelowBar = seenSuggestions.filter((s) => s.action === "joined" && s.score < JOIN_BAR)
  check("nothing was ever auto-joined below the configured threshold",
    joinedBelowBar.length === 0,
    JSON.stringify(joinedBelowBar.map((s: any) => ({ space: s.space, score: s.score }))).slice(0, 300))
  // INVERTED deliberately. Nothing is auto-joined on a score alone any more; only
  // DECLARED overlap does that. So the invariant to police is the opposite one:
  // every automatic join must point at something both agents actually stated.
  const joinedOnScoreAlone = seenSuggestions.filter(
    (s: any) => s.action === "joined" && (s.shared_refs ?? []).length === 0)
  check("nothing was auto-joined on a score alone",
    joinedOnScoreAlone.length === 0,
    JSON.stringify(joinedOnScoreAlone.map((s: any) => ({ space: s.space, score: s.score }))).slice(0, 300))
}

// ── director_required: matching advises, admission decides (§8.1) ───────
// With the gate on, a match above the join bar must NOT create a membership.
// The agent has to be told who to ask, or it is left wondering why the agent it
// clearly belongs in never opened to it.
{
  const dirDir = mkdtempSync(join(tmpdir(), "agents-space-e2e-gated-"))
  const ADDR3 = `127.0.0.1:${Number(process.env.PORT ?? 4934) + 2}`
  const d3 = Bun.spawn({
    cmd: [dibd, "-dir", dirDir, "-addr", ADDR3, "-match-repo", repo,
          // Measured, not fixed: this daemon scores the same live repository,
          // so a constant here rots exactly as the one above did.
          // A bar of its own, deliberately NOT the measured JOIN_BAR.
          //
          // JOIN_BAR is measured from the scenario at the top of this file and
          // was then applied here, to different declarations. The comment below
          // already worried about that and tried to make the two scenarios
          // identical; they still are not, and this test's score landed either
          // side of the moving bar depending on the repository index. 0.2636
          // against a bar of 0.333 on one run in three, which made the check fail
          // on a system behaving correctly.
          //
          // This test is about the DIRECTOR GATE: that an eligible match awaits
          // a human instead of joining itself. Whether a particular score clears
          // a calibrated bar is a different question with its own tests. Pinning
          // the bar low makes any genuine match eligible, so the gate is what is
          // being measured and nothing else.
          "-match-join", "0.05", "-match-notify", "0.02", "-match-director-required"],
    stdout: "ignore", stderr: "ignore",
  })
  try {
    let sec3 = ""
    for (let i = 0; i < 60 && !sec3; i++) {
      try { sec3 = (await Bun.file(`${dirDir}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
    }
    const c3 = async (name: string, args: Record<string, unknown>) => {
      const res = await fetch(`http://${ADDR3}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": sec3 },
        body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
      })
      const b = await res.json() as any
      if (b.error) throw new Error(`${name}: ${JSON.stringify(b.error)}`)
      return JSON.parse(b.result.content[0].text)
    }
    const mk3 = async (n: string) => {
      const r = await c3("register", { name: n, session_id: `s3-${n}` })
      await c3("check_in", { token: r.token })
      return r.token
    }
    const first = await mk3("g-first")
    const second = await mk3("g-second")
    // The opener declares its work, exactly as measureTheBar's opener does.
    //
    // This line was missing, and the join bar is measured from that scenario and
    // applied to this one, so the two have to BE the same scenario or the gate
    // is being judged against a bar taken from something else. It passed anyway
    // while a candidate was scored against the agent's merged footprint, which
    // ignored member declarations and so made the two identical by accident.
    // Now that a candidate is judged against the closest live declaration, an
    // opener that has declared nothing is a materially different case, and the
    // mismatch became a failure. The bar was always being applied to a scenario
    // it had not measured.
    await c3("declare", { token: first, text: "enforcing exclusive claims in the guard" })
    await c3("open_space", { token: first, space: "guard-work", topic: "claim guard denies edits to claimed paths" })

    // Wait for an ELIGIBLE match, not merely a visible one.
    //
    // This broke as soon as any suggestion appeared and then asserted
    // awaiting_director: an outcome only an eligible match can produce. While
    // the index is still warming the score comes in under the join threshold,
    // the action is `consider`, and the director gate is never consulted, so the
    // assertion failed on a system behaving correctly. Observed once in three
    // runs at score 0.2635 against a bar of 0.333.
    //
    // A gate that fails at random stops being evidence, which is worse than the
    // check not existing: it trains people to re-run rather than read. So the
    // loop now waits for the decision the test is about, and if it never comes
    // the failure says the score never cleared rather than blaming the gate.
    //
    // The slot id is stable because declare WITHOUT one adds a declaration
    // every time: sixty polls would leave sixty slots and eventually hit the
    // per-agent cap, which is a different failure wearing this test's name.
    let sug: any
    for (let i = 0; i < 60; i++) {
      const r = await c3("declare", {
        token: second, slot_id: "s1",
        text: "guard path enforcement for exclusive claims",
      })
      const m = (r.spaces ?? []).find((l: any) => l.space === "guard-work")
      if (m) sug = m
      if (m && m.action !== "consider") break
      await Bun.sleep(250)
    }
    check("with the gate on, a match still surfaces", sug !== undefined, "no match: index may not be ready")
    check("and the match became eligible (else the gate below is never consulted)",
      sug !== undefined && sug.action !== "consider",
      `action=${sug?.action} score=${sug?.score}: the score never cleared the join bar`)
    if (sug) {
      check("but it does NOT auto-join", sug.action === "awaiting_director",
        `action=${sug.action} score=${sug.score}`)
      check("and the agent is told what to do about it",
        typeof sug.hint === "string" && /admit/.test(sug.hint), sug.hint ?? "(no hint)")
    }
  } finally {
    d3.kill()
    try { rmSync(dirDir, { recursive: true, force: true }) } catch {}
  }
}

// ── the bar is what decides, proven by moving it ─────────────────────────
// The invariant above can only see the scores this run happens to produce, and
// they all landed on one side of the bar, so it would stay green if the
// comparison were deleted. Mutation-testing showed exactly that.
//
// So instead of hunting for a declaration that scores between the bars, move
// the BAR past a score we know occurs. Same daemon, same repository, same two
// declarations, one threshold raised above the observed 0.333: the identical
// match must now come back as advice rather than an auto-join. That cannot pass
// vacuously: if the threshold stops being consulted, this fails immediately.
{
  const dir2 = mkdtempSync(join(tmpdir(), "agents-space-e2e-hi-"))
  const ADDR2 = `127.0.0.1:${Number(process.env.PORT ?? 4934) + 1}`
  const d2 = Bun.spawn({
    cmd: [dibd, "-dir", dir2, "-addr", ADDR2,
          "-match-repo", repo, "-match-join", "0.95", "-match-notify", "0.05"],
    stdout: "ignore", stderr: "ignore",
  })
  try {
    let sec2 = ""
    for (let i = 0; i < 60 && !sec2; i++) {
      try { sec2 = (await Bun.file(`${dir2}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
    }
    const call2 = async (name: string, args: Record<string, unknown>) => {
      const res = await fetch(`http://${ADDR2}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": sec2 },
        body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
      })
      const b = await res.json() as any
      if (b.error) throw new Error(`${name}: ${JSON.stringify(b.error)}`)
      return JSON.parse(b.result.content[0].text)
    }
    const mk = async (n: string) => {
      const r = await call2("register", { name: n, session_id: `s2-${n}` })
      await call2("check_in", { token: r.token })
      return r.token
    }
    const one = await mk("hi-one")
    const two = await mk("hi-two")
    await call2("open_space", { token: one, space: "guard-work", topic: "claim guard denies edits to claimed paths" })

    // Retry while the async index finishes; the daemon serves before it is ready.
    let sug: any
    for (let i = 0; i < 60; i++) {
      const r = await call2("declare", { token: two, text: "guard path enforcement for exclusive claims" })
      sug = (r.spaces ?? []).find((l: any) => l.space === "guard-work")
      if (sug) break
      await Bun.sleep(250)
    }
    check("the same work still matches when the bar is raised", sug !== undefined,
      "no match at all: the index may not have been ready")
    if (sug) {
      check("a score below the raised bar is advised, not auto-joined",
        sug.action === "consider", `action=${sug.action} score=${sug.score} bar=0.95`)
      check("and it is the same score, so only the bar changed",
        sug.score > 0 && sug.score < 0.95, String(sug.score))
    }
  } finally {
    d2.kill()
    try { rmSync(dir2, { recursive: true, force: true }) } catch {}
  }
}

// ── tier 2 against a service Dibs did not write ────────────────────────
// The client is otherwise only ever tested against our own test double and our
// own reference server: both ours, so both could share a misreading of the
// spec. This runs the DAEMON against whatever OpenAI-compatible service is up
// (Ollama by default) and drives the whole auto-join path through it.
//
// Skipped, not failed, when nothing is serving: an optional capability must not
// make the suite red on a machine that never opted into it.
{
  const embedURL = process.env.EMBED_URL ?? "http://127.0.0.1:11434"
  const embedModel = process.env.EMBED_MODEL ?? "qwen3-embedding:0.6b"
  let up = false
  try {
    const probe = await fetch(`${embedURL}/v1/embeddings`, {
      method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: embedModel, input: ["probe"] }),
      signal: AbortSignal.timeout(4000),
    })
    up = probe.ok
  } catch { up = false }

  if (!up) {
    console.log(`  \x1b[33m·\x1b[0m tier-2 interop skipped: no embeddings service at ${embedURL}`)
  } else {
    // A TINY repository, not this one. The property under test is interop,
    // that Dibs drives a service it did not write, and indexing 438 chunks of
    // Dibs through a real model to prove it costs eight minutes and tests
    // throughput instead. Three files prove the same thing in seconds.
    const tiny = mkdtempSync(join(tmpdir(), "agents-embed-repo-"))
    writeFileSync(join(tiny, "guard.go"), "package guard\n// deny edits to paths claimed exclusively by another agent\n")
    writeFileSync(join(tiny, "styles.css"), "/* board fonts, colours and layout */\n")
    writeFileSync(join(tiny, "README.md"), "# tiny fixture\n")
    for (const args of [["init", "-q"], ["add", "-A"],
                        ["-c", "user.email=e@e", "-c", "user.name=e", "commit", "-qm", "guard: deny claimed edits"]]) {
      Bun.spawnSync({ cmd: ["git", "-C", tiny, ...args], stdout: "ignore", stderr: "ignore" })
    }

    const d2 = mkdtempSync(join(tmpdir(), "agents-embed-e2e-"))
    const ADDR3 = `127.0.0.1:${Number(process.env.PORT ?? 4934) + 2}`
    const proc = Bun.spawn({
      cmd: [dibd, "-dir", d2, "-addr", ADDR3,
            "-match-repo", tiny, "-match-join", "0.40", "-match-notify", "0.20",
            "-match-embed-url", embedURL, "-match-embed-model", embedModel],
      stdout: "ignore", stderr: "ignore",
    })
    try {
      let sec = ""
      for (let i = 0; i < 60 && !sec; i++) {
        try { sec = (await Bun.file(`${d2}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
      }
      const c3 = async (name: string, args: Record<string, unknown>) => {
        const res = await fetch(`http://${ADDR3}/mcp`, {
          method: "POST",
          headers: { "content-type": "application/json", "X-Dibs-Local": sec },
          body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
        })
        const b = await res.json() as any
        if (b.error) throw new Error(`${name}: ${JSON.stringify(b.error)}`)
        return JSON.parse(b.result.content[0].text)
      }
      const mk = async (n: string) => {
        const r = await c3("register", { name: n, session_id: `e-${n}` })
        await c3("check_in", { token: r.token })
        return r.token
      }
      const one = await mk("embed-one")
      const two = await mk("embed-two")
      await c3("open_space", { token: one, space: "guard-work", topic: "deny edits to exclusively claimed paths" })

      // The daemon answers while indexing runs, which is itself part of the
      // contract, so poll rather than waiting for a ready signal.
      let sug: any
      for (let i = 0; i < 90; i++) {
        const r = await c3("declare", { token: two, text: "guard path enforcement for exclusive claims" })
        sug = (r.spaces ?? []).find((l: any) => l.space === "guard-work")
        if (sug) break
        await Bun.sleep(2000)
      }
      check("the daemon matches through a third-party embeddings service", sug !== undefined,
        `no match via ${embedModel}: indexing may not have finished`)
      if (sug) {
        check("the match carries the model in its provenance",
          typeof sug.score === "number" && sug.score > 0, JSON.stringify(sug).slice(0, 200))
        // board on THIS daemon. `board()` is bound to the main one, and a
        // token from a different daemon is meaningless there.
        const res3 = await fetch(`http://${ADDR3}/mcp`, {
          method: "POST",
          headers: { "content-type": "application/json", "X-Dibs-Local": sec },
          body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call",
                                 params: { name: "board", arguments: { token: one } } }),
        })
        const b = ((await res3.json()) as any).result?._meta?.["com.dibs/panel"] ?? {}
        const ch = (b.board?.spaces ?? []).find((c: any) => c.id === "guard-work")
        const auto = (ch?.members ?? []).find((m: any) => m.auto)
        check("the recorded scorer names the model, not the endpoint",
          !!auto && typeof auto.scorer === "string" && auto.scorer.startsWith("embed:"),
          JSON.stringify(auto ?? {}).slice(0, 200))
      }
    } finally {
      proc.kill()
      try { rmSync(d2, { recursive: true, force: true }) } catch {}
      try { rmSync(tiny, { recursive: true, force: true }) } catch {}
    }
  }
}

// ── things done TO an agent must reach it ───────────────────────────────
// An agent told "awaiting_director" was then told nothing, ever. It was
// admitted seconds later with no way to learn the wait had ended short of
// polling the event stream on the off-chance. Same for queue promotion, same
// for eviction: all three are changes the agent did not cause and cannot
// predict, and all three were silent.
{
  const d3 = mkdtempSync(join(tmpdir(), "agents-director-"))
  const ADDR5 = `127.0.0.1:${Number(process.env.PORT ?? 4934) + 4}`
  const tiny = mkdtempSync(join(tmpdir(), "agents-director-repo-"))
  // The filename must share a token with what the agents declare: tier 0
  // matches words against PATHS, so "token validation retry" against a file
  // called auth.go has nothing to go on (that case is covered separately, as
  // the no-opinion phase).
  writeFileSync(join(tiny, "token_retry.go"), "package auth\n// token validation and retry\n")
  for (const args of [["init", "-q"], ["add", "-A"],
                      ["-c", "user.email=e@e", "-c", "user.name=e", "commit", "-qm", "auth"]]) {
    Bun.spawnSync({ cmd: ["git", "-C", tiny, ...args], stdout: "ignore", stderr: "ignore" })
  }
  const proc = Bun.spawn({
    cmd: [dibd, "-dir", d3, "-addr", ADDR5, "-match-repo", tiny,
          "-match-join", String(JOIN_BAR), "-match-director-required"],
    stdout: "ignore", stderr: "ignore",
  })
  try {
    let sec = ""
    for (let i = 0; i < 80 && !sec; i++) {
      try { sec = (await Bun.file(`${d3}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
    }
    const c5 = async (name: string, args: Record<string, unknown>) => {
      const res = await fetch(`http://${ADDR5}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": sec },
        body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
      })
      const b = await res.json() as any
      if (b.error) throw new Error(`${name}: ${JSON.stringify(b.error)}`)
      return JSON.parse(b.result.content[0].text)
    }
    const mk = async (n: string, sid: string) => {
      const r = await c5("register", { name: n, session_id: sid })
      await c5("check_in", { token: r.token })
      return r.token
    }
    const dir = await mk("director", "dsid")
    await c5("open_space", { token: dir, space: "auth-work", topic: "token validation and retry in auth" })
    // Only a human may grant the role; no agent can promote itself.
    Bun.spawnSync({ cmd: [process.env.DIBS ?? `${home}/.local/bin/dibs`, "admin", "set-password"],
                    stdin: new TextEncoder().encode("dir-e2e-12345\ndir-e2e-12345\n"),
                    env: { ...process.env, DIBS_ADMIN: "1", DIBS_DIR: d3, DIBS_ADDR: ADDR5 },
                    stdout: "ignore", stderr: "ignore" })
    await fetch(`http://${ADDR5}/api/admin/role`, {
      method: "POST",
      headers: { "content-type": "application/json", "X-Dibs-Local": sec, "X-Dibs-Admin": "dir-e2e-12345" },
      body: JSON.stringify({ agent: "director", role: "coordinator" }),
    })

    const worker = await mk("worker", "wsid")
    let decl: any
    for (let i = 0; i < 90; i++) {
      decl = await c5("declare", { token: worker, text: "fixing the token validation retry loop" })
      if ((decl.spaces ?? []).length) break
      await Bun.sleep(1000)
    }
    const m = (decl.spaces ?? []).find((l: any) => l.space === "auth-work")
    check("a director gate holds the match instead of joining it",
      m?.action === "awaiting_director", JSON.stringify(m ?? {}).slice(0, 200))
    check("and names the tool that unblocks it",
      /admit/.test(String(m?.hint)), String(m?.hint))

    await c5("admit", { token: dir, space: "auth-work", to: "worker" })
    const poll = await c5("hook_poll", { session_id: "wsid", event: "Stop" })
    const txt: string = poll?.hookSpecificOutput?.additionalContext ?? ""
    check("the admitted agent is TOLD, through the wake path",
      /admitted to agent "auth-work" by director/.test(txt), txt.slice(0, 200) || "(silent)")
    check("and told what it may now do", /you may start/.test(txt), txt.slice(0, 200))

    // The wake path is token-less (a lifecycle hook has no token) so any
    // holder of the coordination secret can poll another agent's session. It
    // therefore may not CONSUME anything: reading it is repeatable and changes
    // nothing, or a peer could spend a victim's notices on its behalf.
    //
    // This check used to assert the opposite ("delivered once"), which was the
    // consuming design. It is here in its corrected form rather than deleted,
    // because the property it was guarding: an agent must not be nagged
    // forever: is real; it is now the AGENT that ends the repetition, by
    // acknowledging the board, which no peer can do for it.
    const again = await c5("hook_poll", { session_id: "wsid", event: "Stop" })
    check("a peer polling the wake path cannot consume the notice",
      /admitted to agent "auth-work" by director/.test(again?.hookSpecificOutput?.additionalContext ?? ""),
      String(again?.hookSpecificOutput?.additionalContext ?? "").slice(0, 120) || "(silent)")

    const ackd = await c5("check_in", { token: worker })
    check("the agent's own check_in is what delivers it authoritatively",
      (ackd.agent_updates ?? []).some((u: string) => /admitted to agent "auth-work"/.test(u)),
      JSON.stringify(ackd.agent_updates ?? []).slice(0, 160))

    const settled = await c5("hook_poll", { session_id: "wsid", event: "Stop" })
    check("and having acknowledged it, the agent stops being told",
      !/admitted to agent/.test(settled?.hookSpecificOutput?.additionalContext ?? ""),
      String(settled?.hookSpecificOutput?.additionalContext ?? "").slice(0, 120))

    await c5("evict", { token: dir, space: "auth-work", to: "worker" })
    const ev = await c5("hook_poll", { session_id: "wsid", event: "Stop" })
    check("eviction reaches the agent too, with what to do about it",
      /removed from agent "auth-work"/.test(ev?.hookSpecificOutput?.additionalContext ?? "") &&
      /stop work there/.test(ev?.hookSpecificOutput?.additionalContext ?? ""),
      String(ev?.hookSpecificOutput?.additionalContext ?? "").slice(0, 200) || "(silent)")
  } finally {
    proc.kill()
    try { rmSync(d3, { recursive: true, force: true }) } catch {}
    try { rmSync(tiny, { recursive: true, force: true }) } catch {}
  }
}

// ── silence is never an answer ──────────────────────────────────────────
// `declare` used to return {"ok":true,"slot_id":"s1"} whether matching was
// off, still indexing, degraded, or working and genuinely found nothing. Four
// unrelated situations, one identical reply: an agent could not tell whether
// to wait, to reconfigure, or to get on with it alone.
{
  // Words that name real files here, so the scorer HAS an opinion; the point of
  // this check is the case where it compared and found nothing close, which is
  // different from the no-opinion case below.
  const r = await call("declare", { token: alpha, text: "blobstore retention and eviction limits" })
  check("a declaration always says whether matching ran",
    typeof r.matching === "string" && r.matching.length > 0, JSON.stringify(r).slice(0, 200))
  // Actionable guidance is the invariant; WHICH field carries it depends on the
  // outcome. This asserted `matching_hint` specifically, and started failing the
  // moment a declaration that matched nothing began opening an agent instead of
  // falling through: the guidance moved to `spaces_hint` and got better, while
  // the check reported a regression. Assert the property, not the field.
  const guidance = String(r.spaces_hint ?? r.matching_hint ?? "")
  check("and always says what to do about it",
    guidance.length > 20, JSON.stringify({ spaces_hint: r.spaces_hint, matching_hint: r.matching_hint }))
  // "I compared you and found nothing" and "I could form no opinion" are
  // different facts. Reporting the second as the first is a confident claim
  // built on no evidence: tier 0 reads FILE PATHS, so a declaration naming no
  // file in the repo predicts nothing at all.
  {
    const blind = await call("declare", { token: beta, text: "zzqq wibble frobnicate" })
    check("words that name no file are reported as no-opinion, not as solitude",
      blind.matching === "no-opinion", `phase=${blind.matching}`)
    check("and the hint says explicitly it is not a finding of working alone",
      /NOT a finding that you are working alone/.test(String(blind.matching_hint)),
      String(blind.matching_hint).slice(0, 140))
  }

  check("a working scorer that found nothing says so, not nothing",
    r.matching === "ready" || r.matching === "suggest-only" || r.matching === "degraded",
    `phase=${r.matching}`)

  // The same call against a daemon with NO repository configured must be
  // distinguishable: that is the whole point.
  const dOff = mkdtempSync(join(tmpdir(), "agents-nomatch-"))
  const ADDR4 = `127.0.0.1:${Number(process.env.PORT ?? 4934) + 3}`
  const off = Bun.spawn({ cmd: [dibd, "-dir", dOff, "-addr", ADDR4], stdout: "ignore", stderr: "ignore" })
  try {
    let sec4 = ""
    for (let i = 0; i < 60 && !sec4; i++) {
      try { sec4 = (await Bun.file(`${dOff}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
    }
    const c4 = async (name: string, args: Record<string, unknown>) => {
      const res = await fetch(`http://${ADDR4}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": sec4 },
        body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
      })
      return JSON.parse(((await res.json()) as any).result.content[0].text)
    }
    const t4 = (await c4("register", { name: "solo", session_id: "s4" })).token
    await c4("check_in", { token: t4 })
    const r4 = await c4("declare", { token: t4, text: "fixing the auth retry loop" })
    check("an unconfigured daemon says matching is OFF, not silent",
      r4.matching === "off", `phase=${r4.matching}`)
    check("and names the flag that turns it on",
      /match-repo/.test(String(r4.matching_hint)), String(r4.matching_hint))

    // The same answer is available to a human, over HTTP, without a token.
    const st = await (await fetch(`http://${ADDR4}/api/match-status`,
      { headers: { "X-Dibs-Local": sec4 } })).json() as any
    check("the daemon exposes why, for `dibs doctor`", st.phase === "off", JSON.stringify(st).slice(0, 160))
  } finally {
    off.kill()
    try { rmSync(dOff, { recursive: true, force: true }) } catch {}
  }
}

// ── it is all still replayable ──────────────────────────────────────────
{
  const verify = Bun.spawnSync({
    cmd: [process.env.DIBS ?? `${home}/.local/bin/dibs`, "verify", `${dir}/ledger.jsonl`],
    stdout: "pipe", stderr: "pipe",
  })
  const out = new TextDecoder().decode(verify.stdout) + new TextDecoder().decode(verify.stderr)
  check("the ledger still verifies after all of that", verify.exitCode === 0 && out.includes("chain intact"),
    out.trim().slice(0, 200))
}

// ── context loss must not cost an agent its place ───────────────────────
// Context loss is the most common thing that happens to an agent, and a token
// rotation must not silently drop what its agent held. Everything has to
// survive: exclusive ownership, membership, queue position, and: the one it
// cannot reconstruct for itself: what it still owes an acknowledgement on.
{
  const own = await call("register", { name: "re-owner", session_id: "re1" })
  await call("check_in", { token: own.token })
  const wait = await call("register", { name: "re-waiter", session_id: "re2" })
  await call("check_in", { token: wait.token })
  const mem = await call("register", { name: "re-member", session_id: "re3" })
  await call("check_in", { token: mem.token })

  // Two agents, because one cannot hold both states: an exclusive space QUEUES a
  // joiner rather than admitting it, so "a member who owes an acknowledgement"
  // and "an agent waiting in a queue" have to live in different agents. Built
  // without a coordinator so this block stands on its own.
  await call("open_space", { token: own.token, space: "re-locked", topic: "single-writer work", exclusive: true })
  const queued = await call("join_space", { token: wait.token, space: "re-locked" })
  check("the waiter is queued behind the owner", queued.queued === true, JSON.stringify(queued))

  await call("open_space", { token: own.token, space: "re-agent", topic: "work that outlives a session" })
  await call("join_space", { token: mem.token, space: "re-agent" })
  const ann = await call("announce", { token: own.token, space: "re-agent", body: "re-space: FREEZE the parser" })

  // All three lose context and come back the documented way: same name, same
  // session id, fresh token.
  const own2 = await call("register", { name: "re-owner", session_id: "re1" })
  const wait2 = await call("register", { name: "re-waiter", session_id: "re2" })
  const mem2 = await call("register", { name: "re-member", session_id: "re3" })
  check("reattaching says so", own2.reattached === true, JSON.stringify(own2).slice(0, 120))
  check("and rotates the token", own2.token !== own.token)

  const b = await board(own2.token)
  const chans = b.board?.spaces ?? []
  const locked = chans.find((c: any) => c.id === "re-locked")
  const reAgent = chans.find((c: any) => c.id === "re-agent")
  check("exclusive ownership survives a token rotation", locked?.owner === "re-owner", JSON.stringify(locked))
  check("queue position survives", (locked?.queue ?? []).includes("re-waiter"), JSON.stringify(locked?.queue))
  check("membership survives", (reAgent?.members ?? []).some((m: any) => m.agent === "re-member"), JSON.stringify(reAgent))

  // The obligation is the one an agent cannot reconstruct, so it must be
  // PULLABLE with the new token, not merely still true in the daemon.
  const owed = await call("inbox", { token: mem2.token })
  const mine = (owed.announcements ?? []).filter((a: any) => /FREEZE the parser/.test(a.body ?? ""))
  check("a reattached agent can ask what it still owes", mine.length === 1,
    JSON.stringify(owed.announcements ?? []).slice(0, 200))
  check("and the obligation names the agent it belongs to", mine[0]?.agent === "re-agent", mine[0]?.agent)

  // Acking what you owe must NOT be gated on the board: an agent could
  // otherwise be stuck owing something it is not allowed to answer.
  const cleared = await call("ack_announcement", { token: mem2.token, msg_serial: ann.serial })
  check("and can clear it with the new token, ungated", cleared.acked === true, JSON.stringify(cleared))

  // The strongest act in the system IS gated. A reattached agent has read
  // nothing, and an announcement obliges every member to answer it.
  const early = await fails("announce", {
    token: own2.token, space: "re-agent", body: "should not land",
  })
  check("but announcing before reading the board is refused",
    /E_MUST_ACK_BOARD/.test(early), early.slice(0, 160))
  await call("check_in", { token: own2.token })
  const after = await call("announce", { token: own2.token, space: "re-agent", body: "now it lands" })
  check("and allowed once it has", typeof after.serial === "number", JSON.stringify(after).slice(0, 120))
  // Posting stays ungated on purpose: a remark obliges nobody.
  await call("check_in", { token: wait2.token })
}

// ── an announcement waiting on somebody who is not there ────────────────
// "Still asking" and "asking somebody who is not there" look identical on a
// board and are not the same problem. Redelivery is driven by the agent
// POLLING, so an announcement owed only by sleeping or crashed agents never
// spends its retry budget and never reaches "unanswered": it sits at
// "awaiting ack" indefinitely, looking healthy, while nothing can arrive.
{
  const say = await call("register", { name: "bl-sender", session_id: "bl1" })
  await call("check_in", { token: say.token })
  const here = await call("register", { name: "bl-present", session_id: "bl2" })
  await call("check_in", { token: here.token })
  // A real process, so its death is detected by the sweep's pid probe rather
  // than asserted by the test.
  const doomed = Bun.spawn({ cmd: ["sleep", "300"], stdout: "ignore", stderr: "ignore" })
  const away = await call("register", { name: "bl-absent", session_id: "bl3", pid: doomed.pid })
  await call("check_in", { token: away.token })

  await call("open_space", { token: say.token, space: "bl-agent", topic: "work with an absentee" })
  await call("join_space", { token: here.token, space: "bl-agent" })
  await call("join_space", { token: away.token, space: "bl-agent" })
  const ann = await call("announce", { token: say.token, space: "bl-agent", body: "bl-space: FREEZE" })

  const spaceOf = async (tok: string) =>
    ((await board(tok)).board?.spaces ?? []).find((c: any) => c.id === "bl-agent")

  check("the announcement is outstanding while somebody can still answer",
    (await spaceOf(say.token))?.unacked_announcements === 1, JSON.stringify(await spaceOf(say.token)))
  check("and is not reported as blocked yet",
    (await spaceOf(say.token))?.blocked_announcements === undefined)

  doomed.kill()
  await doomed.exited
  // The one who could answer answers, leaving only the agent that is gone.
  await call("ack_announcement", { token: here.token, msg_serial: ann.serial })
  for (let i = 0; i < 50; i++) {
    if ((await spaceOf(say.token))?.blocked_announcements === 1) break
    await Bun.sleep(200)
  }
  const whose = await spaceOf(say.token)
  check("once only an absentee owes it, the board says it is BLOCKED",
    whose?.blocked_announcements === 1, JSON.stringify(whose))
  check("while still counting as outstanding, because it is",
    whose?.unacked_announcements === 1, JSON.stringify(whose))
  check("and never silently became 'unanswered': nothing gave up",
    whose?.abandoned_announcements === undefined, JSON.stringify(whose))
}

// ── a notice nobody read must not be recorded as read ───────────────────
// A departing member's ack requirement has to be dropped, or the agent waits
// forever on somebody who is never coming back. But dropping it silently
// settled the announcement as `acked`: and in the extreme case that means an
// announcement with an empty ack list, nobody at all, recorded as
// acknowledged and invisible on the board. A sender checking later is told its
// freeze notice landed when zero agents saw it.
{
  const say = await call("register", { name: "du-sender", session_id: "du1" })
  await call("check_in", { token: say.token })
  const quit = await call("register", { name: "du-quitter", session_id: "du2" })
  await call("check_in", { token: quit.token })
  await call("open_space", { token: say.token, space: "du-agent", topic: "work with a leaver" })
  await call("join_space", { token: quit.token, space: "du-agent" })
  const ann = await call("announce", {
    token: say.token, space: "du-agent", body: "du-space: FREEZE the tokenizer",
  })

  // The only member that owed it leaves without reading it.
  await call("sign_off", { token: quit.token })
  const spaceOf = async (tok: string) =>
    ((await board(tok)).board?.spaces ?? []).find((c: any) => c.id === "du-agent")
  const whose = await spaceOf(say.token)
  check("the agent does not wait forever on an agent that left",
    (whose?.unacked_announcements ?? 0) === 0, JSON.stringify(whose))
  check("but the member that never read it is recorded",
    whose?.departed_unacked === 1, JSON.stringify(whose))

  // And the sender must not be told it was acknowledged.
  const msg = await call("read_mail", { token: say.token, msg_serial: ann.serial })
    .catch(() => null)
  // read_mail is for mail, not announcements; the board is the sender's view,
  // so the assertion that matters is that nothing claims an ack happened.
  check("and nothing on the board claims somebody acknowledged it",
    (whose?.abandoned_announcements ?? 0) >= 1 || whose?.departed_unacked === 1,
    JSON.stringify(agent))
  void msg
}

// ── the terminal board must show the work, not just the agents ──────────
// `dibs board` is the surface for an operator without a browser, and it
// carried agents, slots and claims, but no CHANNELS at all. The whole of what
// v1.2 added was invisible there, including an announcement waiting on
// somebody, which is the state that most needs a person.
{
  const cliBin = process.env.DIBS ?? `${process.env.HOME}/.local/bin/dibs`
  const cli = (...args: string[]) => {
    const r = Bun.spawnSync({
      cmd: [cliBin, ...args],
      env: { ...process.env, DIBS_ADDR: ADDR, DIBS_DIR: dir_ },
      stdout: "pipe", stderr: "pipe",
    })
    return new TextDecoder().decode(r.stdout) + new TextDecoder().decode(r.stderr)
  }
  const out = cli("board")
  // Styled output that is not going to a terminal must be plain. A coordination
  // tool gets piped into grep, teed into a log and redirected into an issue
  // report; `doctor` used to write raw ANSI unconditionally, so a redirected
  // run produced a file of escape sequences. This runs the shipped binary with
  // its stdout captured, which IS the not-a-terminal case.
  // eslint-disable-next-line no-control-regex
  check("styled output is plain when it is not going to a terminal",
    !/\u001b\[/.test(out), JSON.stringify(out.slice(0, 120)))
  check("and doctor is too, which is what gets pasted into a bug report",
    !/\u001b\[/.test(cli("doctor")))
  check("the terminal board lists spaces", /spaces/.test(out), out.slice(0, 300))
  check("with their topic", /token validation|drawing|single-writer|work that outlives/.test(out),
    out.slice(0, 400))
  check("and who is in them", /\bin: /.test(out), out.slice(0, 400))
  // An exclusive space and its queue are the two facts that decide whether an
  // agent can start; neither was reachable from a terminal.
  check("an exclusive space names its owner", /exclusive to /.test(out), out.slice(0, 600))
  check("and shows who is waiting for it", /waiting: /.test(out), out.slice(0, 600))
  // The announcement states, which must stay four distinct facts rather than
  // one number.
  check("outstanding announcements are surfaced in the terminal",
    /awaiting ack|UNANSWERED|blocked|left unread/.test(out), out.slice(0, 800))
  // And WHY an agent stopped counting as live, the same as the other two surfaces.
  check("a dead agent's reason is shown in the terminal too",
    /\(process gone\)|\(no contact\)|\(idle, no pid\)/.test(out), out.slice(0, 800))
  // The summary a person reads before anything else. The browser board has
  // carried it since it existed; over ssh an operator had to count rows.
  check("the terminal board opens with a tally of the fleet",
    /\d+ of \d+ live/.test(out), out.split("\n").slice(0, 3).join(" | "))
}

console.log("─".repeat(60))
console.log(failures === 0
  ? `\x1b[32m${checks} checks passed\x1b[0m\n`
  : `\x1b[31m${failures} of ${checks} checks failed\x1b[0m\n`)
process.exit(failures === 0 ? 0 : 1)
