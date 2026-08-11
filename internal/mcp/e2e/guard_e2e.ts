/**
 * End-to-end test for the claim guard: the thing that makes a claim hold
 * rather than merely inform.
 *
 * WHY THIS TEST EXISTS, in the exact shape it has:
 *
 * The guard was correct in isolation from the day it was written. Every unit
 * test passed. It still let a live opencode agent overwrite a file another agent
 * held exclusively, because the two halves of Dibs disagreed about what to
 * call the session: the `dibs mcp-stdio` bridge registered the agent under an
 * id it invented for itself, while opencode's plugin asked about opencode's own
 * session id. The daemon could not match them, failed open exactly as designed,
 * and the file was clobbered. No test on either side could see it: the bug
 * lived precisely in the gap between them.
 *
 * So this test spans the gap. It spawns the REAL bridge as a child of itself,
 * lets it register an agent with no session id at all, and then asks the guard
 * questions as the REAL plugin: the actual module opencode loads, imported and
 * invoked with the argument shape opencode passes it. The bridge names the
 * session after its parent process; the plugin names it after its own. This
 * test IS that parent and that process, so if the two ever stop agreeing, the
 * agent the bridge registers stops being the agent the plugin speaks for, and the
 * denials below turn back into silent allows.
 *
 * Run: bun internal/mcp/e2e/guard_e2e.ts
 */
import { mkdtempSync, rmSync, mkdirSync, symlinkSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const PORT = process.env.PORT ?? "4933"
const ADDR = `127.0.0.1:${PORT}`

let failures = 0
let checks = 0
function check(name: string, cond: boolean, detail = "") {
  checks++
  if (cond) console.log(`  \x1b[32m✓\x1b[0m ${name}`)
  else { failures++; console.log(`  \x1b[31m✗\x1b[0m ${name}${detail ? ". " + detail : ""}`) }
}

const home = process.env.HOME
const dibd = process.env.DIBD ?? `${home}/.local/bin/dibd`
const dibsBin = process.env.DIBS ?? `${home}/.local/bin/dibs`

const dir = mkdtempSync(join(tmpdir(), "agents-guard-e2e-"))
const project = join(dir, "project")
mkdirSync(project)

const daemon = Bun.spawn({ cmd: [dibd, "-dir", dir, "-addr", ADDR], stdout: "ignore", stderr: "ignore" })
let bridge: ReturnType<typeof Bun.spawn> | undefined
const cleanup = () => {
  try { bridge?.kill() } catch {}
  daemon.kill()
  try { rmSync(dir, { recursive: true, force: true }) } catch {}
}
process.on("exit", cleanup)

// The plugin reads DIBS_ADDR/DIBS_DIR once, at module load, and caches the
// secret. Both must be set before the import below or it will talk to the
// developer's real board instead of this scratch one.
process.env.DIBS_ADDR = ADDR
process.env.DIBS_DIR = dir

// ── wait for the daemon, then talk to it directly for setup ──────────────
let secret = ""
for (let i = 0; i < 60 && !secret; i++) {
  try { secret = (await Bun.file(`${dir}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
}
if (!secret) { console.error("daemon never wrote local.secret"); process.exit(1) }

let rpcId = 0
async function call(name: string, args: Record<string, unknown>): Promise<any> {
  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
  })
  const body = await res.json() as any
  return JSON.parse(body.result.content[0].text)
}

console.log("\nguard e2e")
console.log("─".repeat(60))

// ── the holder: an agent that has taken the project exclusively ────────────
const holder = await call("register", { name: "holder", session_id: "holder-session", cwd: project })
await call("check_in", { token: holder.token })
const claim = await call("claim", { token: holder.token, path: project, mode: "exclusive" })
check("holder takes an exclusive claim", claim.granted === true, JSON.stringify(claim))

// ── the contract: the bridge names the session after its PARENT ──────────
// This is the whole point of the file. The bridge is spawned here, so its
// parent is this test process, so the session id it invents must be the one
// this process would compute for itself. Nothing is passed between them.
const EXPECTED = `host-${process.pid}`

bridge = Bun.spawn({
  cmd: [dibsBin, "mcp-stdio"],
  cwd: project,
  env: { ...process.env, DIBS_ADDR: ADDR, DIBS_DIR: dir },
  stdin: "pipe", stdout: "pipe", stderr: "ignore",
})
const send = (msg: unknown) => bridge!.stdin.write(JSON.stringify(msg) + "\n")
send({ jsonrpc: "2.0", id: 1, method: "initialize", params: {
  protocolVersion: "2025-06-18", capabilities: {},
  clientInfo: { name: "opencode", version: "e2e" },
} })
// Deliberately NO session_id, which is what models actually send.
send({ jsonrpc: "2.0", id: 2, method: "tools/call", params: {
  name: "register", arguments: { name: "intruder", description: "guard e2e" },
} })

// Read until the register reply comes back.
let intruderToken = ""
{
  const reader = bridge.stdout.getReader()
  const dec = new TextDecoder()
  let buf = ""
  const deadline = Date.now() + 15_000
  outer: while (Date.now() < deadline) {
    const { value, done } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    let nl: number
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl); buf = buf.slice(nl + 1)
      if (!line.trim()) continue
      let msg: any
      try { msg = JSON.parse(line) } catch { continue }
      if (msg.id !== 2) continue
      intruderToken = JSON.parse(msg.result.content[0].text).token
      break outer
    }
  }
  reader.releaseLock()
}
check("bridge registered an agent", !!intruderToken)

// The agent went in under a session id nobody told the bridge. If it is not the
// one this process computes, the plugin below is speaking for a different agent.
const asIntruder = await call("guard_path", { session_id: EXPECTED, path: join(project, "f.txt"), cwd: project })
check(`bridge session id is ${EXPECTED}`, asIntruder.decision === "deny",
  `guard answered "${asIntruder.decision}": the bridge named the session something else`)

// ── the plugin: the real module opencode loads ───────────────────────────
const { LanesPlugin } = await import("../../../plugins/opencode/dibs.ts")
const hooks = await (LanesPlugin as any)({})
const before = hooks["tool.execute.before"]

// The argument shape is opencode's own, from session/tools.ts:
//   plugin.trigger("tool.execute.before", { tool: item.id, sessionID, callID }, { args })
const fire = async (tool: string, args: Record<string, unknown>) => {
  try {
    await before({ tool, sessionID: "ses_opencode_native", callID: "c1" }, { args })
    return null
  } catch (e) { return (e as Error).message }
}

const denied = await fire("edit", { filePath: join(project, "protected.txt") })
check("plugin blocks an edit inside another agent's exclusive claim", denied !== null)
check("the refusal names the holder", !!denied && denied.includes("holder"), denied ?? "(allowed)")

check("plugin blocks write as well as edit",
  (await fire("write", { filePath: join(project, "new.txt") })) !== null)

check("plugin allows a path outside the claim",
  (await fire("edit", { filePath: join(dir, "elsewhere.txt") })) === null)

check("plugin ignores non-editing tools",
  (await fire("bash", { filePath: join(project, "protected.txt") })) === null)

check("plugin allows when the tool call carries no path",
  (await fire("edit", {})) === null)

// ── the same directory reached by another name is the same directory ─────
// The claim above was stored under the resolved path. An agent that names the
// project through a symlink must still be stopped: on macOS /tmp and /var ARE
// symlinks, so this is the ordinary case, not an exotic one. Before the guard
// canonicalised paths whose last component does not exist yet, this check
// passed silently as an allow: a claim that stopped nobody.
const alias = join(dir, "alias")
symlinkSync(project, alias)
const viaAlias = await call("guard_path", { session_id: EXPECTED, path: join(alias, "protected.txt"), cwd: alias })
check("a claim blocks an edit that names the path through a symlink",
  viaAlias.decision === "deny", viaAlias.decision)

// The file does not exist yet: `write` creating a new file must be guarded too,
// which is the case that broke resolution in the first place.
const newFile = await call("guard_path", { session_id: EXPECTED, path: join(alias, "does-not-exist-yet.txt"), cwd: alias })
check("a claim blocks creating a NEW file under a symlinked path",
  newFile.decision === "deny", newFile.decision)

// ── the guard must not block the holder from its own claim ───────────────
const own = await call("guard_path", { session_id: "holder-session", path: join(project, "protected.txt"), cwd: project })
check("the holder may still edit what it claimed", own.decision === "allow", own.decision)

// ── an allow must say WHICH allow it is ──────────────────────────────────
// "nothing claims this path" is the guard working. "I could not tell which
// agent you are" is the guard failing open: correctly, since blocking every
// editor it cannot identify would be a broken editor. But the two were
// indistinguishable in the reply, so a mismatched session id made the guard
// silently inert and looked exactly like a clean board. That happened: the
// opencode plugin sent its own session id while the bridge had registered the
// agent under another, and the guard was inert for a day without a symptom.
{
  const unknown = await call("guard_path", {
    session_id: "nobody-at-all", path: join(project, "protected.txt"), cwd: project,
  })
  check("an unidentifiable session is allowed but SAYS it is fail-open",
    unknown.decision === "allow" && unknown.basis === "unidentified-session",
    JSON.stringify(unknown).slice(0, 200))
  check("and warns it is not a finding that the path is unclaimed",
    /NOT a finding that the path is unclaimed/.test(String(unknown.hint)),
    String(unknown.hint).slice(0, 160))

  const known = await call("guard_path", {
    session_id: "holder-session", path: join(dir, "elsewhere.txt"), cwd: project,
  })
  check("a resolved agent editing an unclaimed path is a different basis",
    known.decision === "allow" && known.basis === "no-claim" && known.agent === "holder",
    JSON.stringify(known).slice(0, 200))
}

// ── doctor must not cry wolf ────────────────────────────────────────────
// A stdio-bridge config embeds no secret at all (it reads one from disk) so
// "mentions agents but lacks the current secret" flags every healthy stdio setup
// as broken. That false positive fired on this tool's first real run against a
// working Claude Desktop config. A diagnostic people learn to ignore is worse
// than no diagnostic.
{
  const cfgDir = mkdtempSync(join(tmpdir(), "agents-doctorcfg-"))
  const write = (rel: string, body: string) => {
    mkdirSync(join(cfgDir, rel.split("/").slice(0, -1).join("/")), { recursive: true })
    Bun.write(join(cfgDir, rel), body)
  }
  write(".codex/config.toml", `[mcp_servers.agents]\ncommand = "agents"\nargs = ["mcp-stdio"]\n`)
  await Bun.sleep(100)
  const run = Bun.spawnSync({
    cmd: [dibsBin, "doctor"],
    env: { ...process.env, HOME: cfgDir, DIBS_DIR: dir, DIBS_ADDR: ADDR },
    stdout: "pipe", stderr: "pipe",
  })
  const out = new TextDecoder().decode(run.stdout) + new TextDecoder().decode(run.stderr)
  check("a stdio-bridge config is not reported as a stale secret",
    !/STALE secret/.test(out), out.split("\n").filter((l) => /STALE/.test(l)).join(" "))
  check("and is recognised as the stdio path",
    /stdio bridge/.test(out), out.slice(0, 200))

  const doctor = (): string => {
    const r = Bun.spawnSync({
      cmd: [dibsBin, "doctor"],
      env: { ...process.env, HOME: cfgDir, DIBS_DIR: dir, DIBS_ADDR: ADDR },
      stdout: "pipe", stderr: "pipe",
    })
    return new TextDecoder().decode(r.stdout) + new TextDecoder().decode(r.stderr)
  }

  // Stale means: this config is FOR this daemon and carries the wrong secret.
  // The fixture has to name this daemon's address, or it is not testing that,
  // it was written with a placeholder host, so the old check could not tell a
  // stale config from one belonging to somebody else's daemon at all.
  write(".codex/config.toml",
    `[mcp_servers.agents]\nurl = "http://${ADDR}/mcp"\nhttp_headers = { "X-Dibs-Local" = "${"d".repeat(64)}" }\n`)
  await Bun.sleep(100)
  const out2 = doctor()
  check("but a genuinely stale embedded secret IS reported",
    /STALE secret/.test(out2), out2.slice(0, 200))

  // And the case that made this matter: doctor flagged the operator's real,
  // working config as STALE purely because it was run against a second daemon,
  // and told them to re-copy the block, which would have repointed a working
  // global setup at whichever scratch daemon happened to be running.
  write(".codex/config.toml",
    `[mcp_servers.agents]\nurl = "http://127.0.0.1:59999/mcp"\nhttp_headers = { "X-Dibs-Local" = "${"d".repeat(64)}" }\n`)
  await Bun.sleep(100)
  const out3 = doctor()
  check("a config for ANOTHER daemon is not called stale",
    !/STALE secret/.test(out3) && /different daemon/.test(out3),
    out3.split("\n").filter((l) => /daemon|STALE/.test(l)).join(" ").slice(0, 200))
  check("and the hint names that daemon rather than this one",
    /127\.0\.0\.1:59999\/mcp/.test(out3), out3.slice(0, 400))
  try { rmSync(cfgDir, { recursive: true, force: true }) } catch {}
}

// ── is the guard actually protecting anything? ───────────────────────────
// The guard fails open when it cannot resolve the caller: correct, and it makes
// a misconfigured guard indistinguishable from a board where nothing is claimed.
// The daemon sees every call and whether it resolved, so it is the only party
// that can tell. This is the failure that cost a day: opencode's plugin sent its
// own session id while the bridge had registered the agent under another, and
// nothing anywhere said so.
{
  const health = async () => {
    const res = await fetch(`http://${ADDR}/api/hook-health`, { headers: { "X-Dibs-Local": secret } })
    return await res.json() as any
  }
  // Calls above already resolved (EXPECTED names a real agent), so this board is
  // healthy, and must say so rather than warning about a working setup.
  const good = await health()
  check("a resolving guard reports ok", good.verdict === "ok", JSON.stringify(good).slice(0, 200))
  check("and counts what actually resolved", good.guard_resolved > 0, String(good.guard_resolved))

  // Now the poisoned case, on its own daemon so the counters are clean.
  const dBad = mkdtempSync(join(tmpdir(), "agents-inert-"))
  const ADDR2 = `127.0.0.1:${Number(PORT) + 5}`
  const p2 = Bun.spawn({ cmd: [dibd, "-dir", dBad, "-addr", ADDR2], stdout: "ignore", stderr: "ignore" })
  try {
    let s2 = ""
    for (let i = 0; i < 60 && !s2; i++) {
      try { s2 = (await Bun.file(`${dBad}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
    }
    const ask = async (sid: string) => fetch(`http://${ADDR2}/mcp`, {
      method: "POST",
      headers: { "content-type": "application/json", "X-Dibs-Local": s2 },
      body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call",
        params: { name: "guard_path", arguments: { session_id: sid, path: "/tmp/f.go", cwd: "/tmp" } } }),
    })
    const before = await (await fetch(`http://${ADDR2}/api/hook-health`,
      { headers: { "X-Dibs-Local": s2 } })).json() as any
    check("a daemon nothing has asked reports never-called",
      before.verdict === "never-called", JSON.stringify(before).slice(0, 160))

    for (let i = 0; i < 3; i++) await ask("a-session-nobody-registered")
    const after = await (await fetch(`http://${ADDR2}/api/hook-health`,
      { headers: { "X-Dibs-Local": s2 } })).json() as any
    check("hooks that never resolve are reported as an INERT guard",
      after.verdict === "never-resolved", JSON.stringify(after).slice(0, 160))
    check("and the hint says it looks like a board where nothing is claimed",
      /looks exactly like a board where nothing is claimed/.test(String(after.hint)),
      String(after.hint).slice(0, 160))
  } finally {
    p2.kill()
    try { rmSync(dBad, { recursive: true, force: true }) } catch {}
  }
}

// ── a subagent is its parent's work, not a third party to it ─────────────
// The ordinary delegation pattern is: claim the area, spawn a subagent to edit
// it. Without lineage the guard DENIED that subagent on its own parent's claim
// and because the guard is an enforcement path rather than advice, the
// harness then refused the edit outright. The exclusive claim locked out the
// very work it was taken for.
{
  // Vouched, because `parent` alone is a claim anybody can make. An agent that
  // merely declared parent:"holder" used to inherit the holder's memberships,
  // skip an exclusive space's queue, and be exempt from its claims right here,
  // so the parent proves it with a one-time nonce only it can issue.
  await call("vouch_child", { token: holder.token, nonce: "child-nonce-0123456789abcdef" })
  const child = await call("register", {
    name: "child", session_id: "child-session", parent: "holder", cwd: project,
    parent_nonce: "child-nonce-0123456789abcdef",
  })
  await call("vouch_child", { token: child.token, nonce: "grand-nonce-0123456789abcdef" })
  await call("register", {
    name: "grandchild", session_id: "grandchild-session", parent: "child", cwd: project,
    parent_nonce: "grand-nonce-0123456789abcdef",
  })
  const target = join(project, "protected.txt")
  for (const [who, session] of [["child", "child-session"], ["grandchild", "grandchild-session"]]) {
    const v = await call("guard_path", { session_id: session, path: target, cwd: project })
    check(`a ${who} of the claim holder may edit inside its claim`,
      v.decision === "allow", `${v.decision}. ${String(v.reason).slice(0, 90)}`)
  }
  // The point of the claim survives: an unrelated agent is still stopped.
  const third = await call("guard_path", { session_id: EXPECTED, path: target, cwd: project })
  check("while an unrelated agent is still denied", third.decision === "deny", third.decision)

  // And a lineage nobody vouched for buys nothing. Verified against a running
  // daemon before this existed: an agent registering with parent:"holder"
  // posted into the holder's exclusive space, skipped its queue, and got
  // allow/no-claim for a path the holder held exclusively.
  await call("register", {
    name: "impostor", session_id: "impostor-session", parent: "holder", cwd: project,
  })
  const faked = await call("guard_path", {
    session_id: "impostor-session", path: join(project, "protected.txt"), cwd: project,
  })
  check("but merely CLAIMING a parent buys no exemption",
    faked.decision === "deny", `${faked.decision}. ${String(faked.reason).slice(0, 80)}`)

  // Only that direction. A parent editing inside its SUBAGENT's claim is still
  // stopped: the child asked not to be disturbed, and force_release is how a
  // parent overrules that deliberately.
  await call("vouch_child", { token: holder.token, nonce: "kid-nonce-0123456789abcdef" })
  const kid = await call("register", {
    name: "kid", session_id: "kid-session", parent: "holder", cwd: project,
    parent_nonce: "kid-nonce-0123456789abcdef",
  })
  const kidDir = join(dir, "kid-area")
  mkdirSync(kidDir, { recursive: true })
  await call("check_in", { token: kid.token }) // claims require it
  const kidClaim = await call("claim", { token: kid.token, path: kidDir, mode: "exclusive" })
  check("the subagent's own claim was actually granted",
    kidClaim.granted === true, JSON.stringify(kidClaim).slice(0, 120))
  const upward = await call("guard_path", {
    session_id: "holder-session", path: join(kidDir, "x.txt"), cwd: kidDir,
  })
  check("but a parent is still stopped by its own subagent's claim",
    upward.decision === "deny", upward.decision)
}

// ── failing open is a feature, and must stay one ─────────────────────────
const unknown = await call("guard_path", { session_id: "host-0", path: join(project, "protected.txt"), cwd: project })
check("an unrecognised session is allowed, not blocked", unknown.decision === "allow", unknown.decision)

// A stale holder is neither a clean allow nor a fair deny.
await call("sign_off", { token: holder.token })
const afterClose = await call("guard_path", { session_id: EXPECTED, path: join(project, "protected.txt"), cwd: project })
check("a closed holder's claim stops blocking", afterClose.decision === "allow", afterClose.decision)

// ── the stamp: does a spawned subagent inherit its parent's agent? ─────────
//
// opencode is the only harness of the four that can do this natively. Claude
// Code and Codex expose a shell tool with no environment argument, so their
// plugins prefix an assignment onto the command STRING and must refuse every
// shape a prefix would change: subshells, leading redirects, `cd /x && …`.
// `shell.env` hands over the map, so there is nothing to parse and nothing to
// refuse.
//
// Uses the same session identity the guard proved above: the agent is
// registered by the bridge under `host-<pid>`, NOT opencode's own sessionID,
// and getting that wrong is what once made the guard useless in practice.
{
  const shellEnv = hooks["shell.env"]
  const out = { env: {} as Record<string, string> }
  await shellEnv({ cwd: project, sessionID: "ses_opencode_native" }, out)
  check("shell.env stamps the spawning agent into the environment",
    out.env["DIBS_PARENT"] === "intruder", out.env["DIBS_PARENT"] ?? "(unset)")

  // Never overwrite: the OUTERMOST parent is the one that can act on a stall,
  // so re-stamping at each level would reassign a child to its nearest
  // ancestor instead.
  const nested = { env: { DIBS_PARENT: "outer" } as Record<string, string> }
  await shellEnv({ cwd: project, sessionID: "ses_opencode_native" }, nested)
  check("an existing stamp is left alone", nested.env["DIBS_PARENT"] === "outer",
    nested.env["DIBS_PARENT"])
}

// ── progress: the one harness Dibs cannot observe from outside ───────────
//
// opencode keeps sessions in SQLite, where byte growth measures WAL churn, and
// its only append-only file is a single log SHARED by every run on the machine.
// So the plugin counts its own turns and reports them; without that, an
// opencode child is judged on CPU alone: a hard stall is caught and a slow one
// is not.
{
  const chat = hooks["chat.message"]
  await chat({ sessionID: "ses_opencode_native" }, { parts: [] })
  // Fire-and-forget by design: a supervision signal must never delay a turn, so
  // the report is in flight rather than awaited. Poll for it.
  let reported = 0
  for (let i = 0; i < 20 && reported === 0; i++) {
    const r = await call("hook_session", { session_id: EXPECTED, event: "poll" })
    reported = Number(r?.progress ?? 0)
    if (reported === 0) await new Promise((res) => setTimeout(res, 100))
  }
  check("the opencode plugin reports its own progress", reported > 0, String(reported))
}

console.log("─".repeat(60))
console.log(failures === 0
  ? `\x1b[32m${checks} checks passed\x1b[0m\n`
  : `\x1b[31m${failures} of ${checks} checks failed\x1b[0m\n`)
process.exit(failures === 0 ? 0 : 1)
