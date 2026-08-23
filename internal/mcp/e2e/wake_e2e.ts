/**
 * End-to-end test for `[wake.exec]`: reaching an agent that is NOT running.
 *
 * WHY THIS TEST EXISTS, in the exact shape it has:
 *
 * Every other delivery path waits for the agent to come to Dibs, so an idle
 * session never sees its mail. `[wake.exec]` is the answer, and the first
 * version of it did not work in the one configuration anybody runs: it
 * substituted the agent's `session_id`, which the stdio bridge invents as
 * `host-<ppid>`, and the resume command it produced named no thread. The
 * subprocess started, failed, and the mail stayed unread. Every unit test
 * passed, because they all built an agent whose primary id was already a uuid.
 *
 * The identifier that CAN be resumed does not arrive with `register` at all. It
 * arrives earlier, on the harness's own SessionStart hook, and is adopted by
 * the agent that registers afterwards in the same directory. That handoff spans
 * three components -- the hook binding in plugins/codex/hooks/hooks.json, the
 * announced-session join, and the waker's substitution -- and the defect lived
 * exactly in the gap between them, which is the one place no unit test looks.
 *
 * So this test walks the whole chain against the REAL daemon, with a real
 * dibs.toml and a real subprocess:
 *
 *   hook_poll(session_id=<uuid>, cwd=X)   the SessionStart hook, as configured
 *   register(cwd=X)                       adopts that uuid
 *   send(type=question)                   somebody is now blocked
 *   -> the operator's command runs, carrying the UUID and not host-<ppid>
 *
 * It also pins the three decisions that make the feature safe rather than
 * merely working: a `notify` wakes nobody, a burst is one wake and not three,
 * and an agent with no resumable identifier is left alone.
 *
 * Run: DIBD=$PWD/bin/dibd bun internal/mcp/e2e/wake_e2e.ts
 */
import { mkdtempSync, rmSync, mkdirSync, readFileSync, readdirSync } from "node:fs"
import { tmpdir } from "node:os"
import { basename, dirname, join } from "node:path"

const PORT = process.env.PORT ?? "4939"
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

const dir = mkdtempSync(join(tmpdir(), "dibs-wake-e2e-"))
const project = join(dir, "project")
mkdirSync(project)

// The wake command. A real program on argv, never a shell string: the whole
// point of the argv form is that a message body written by a hostile peer is
// one argument and cannot become a command. This recorder appends whatever it
// was handed, so the assertions below read the SUBSTITUTED values rather than
// merely observing that something ran.
const recorder = join(dir, "recorder.ts")
const log = join(dir, "wakes.jsonl")
// It can also FAIL, on demand: a wake command that cannot run is the case the
// cooldown accounting gets wrong, and a recorder that always exits 0 can never
// show it.
// APPENDS, and that word has to be true. It read the whole file and rewrote
// old-contents-plus-line, which is a read-modify-write: if cooldown suppression
// regressed and three processes started at once, all three could read the same
// old content and clobber each other, the file could finish one line longer,
// and the burst check below would pass against the exact defect it exists to
// catch. Each process writes its own file instead, and the count is the number
// of files.
await Bun.write(recorder, `
const line = JSON.stringify(Bun.argv.slice(3)) + "\\n"
await Bun.write(Bun.argv[2] + "." + process.pid + "." + Bun.nanoseconds(), line)
if (await Bun.file(Bun.argv[2] + ".fail").exists()) process.exit(3)
`)

// cooldown doubles as the recency window: an agent that spoke to the board
// within it is certainly running and is never woken. One second keeps this test
// quick while still exercising the real gate.
await Bun.write(join(dir, "dibs.toml"), `
[wake.exec.codex]
argv = ["${process.execPath}", "${recorder}", "${log}", "{thread}", "{message}", "{agent}", "{from}", "{type}"]
cooldown = "1s"
`)

const daemonErr = join(dir, "dibd.err")
const daemon = Bun.spawn({
  cmd: [dibd, "-dir", dir, "-addr", ADDR],
  env: { ...process.env, DIBS_ALLOW_PARALLEL: "1" },
  stdout: "ignore", stderr: Bun.file(daemonErr),
})
const cleanup = () => {
  try { daemon.kill() } catch {}
  try { rmSync(dir, { recursive: true, force: true }) } catch {}
}
process.on("exit", cleanup)

let secret = ""
for (let i = 0; i < 60 && !secret; i++) {
  try { secret = (await Bun.file(`${dir}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
}
if (!secret) {
  // A failing probe is usually a broken probe: say WHY the daemon did not come
  // up rather than leaving a reader to guess. This test spent a run on a
  // refusal it swallowed.
  console.error("daemon never wrote local.secret. Its stderr:")
  try { console.error(readFileSync(daemonErr, "utf8")) } catch {}
  process.exit(1)
}

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

// Every wake recorded so far, newest last.
// One file per wake, so two processes cannot collapse into one observation.
function wakes(): string[][] {
  const dir = dirname(log)
  const base = basename(log) + "."
  return readdirSync(dir)
    .filter(f => f.startsWith(base) && !f.endsWith(".fail"))
    .sort()
    .map(f => JSON.parse(readFileSync(join(dir, f), "utf8").trim()))
}
async function settle(ms = 1500) { await Bun.sleep(ms) }

console.log("\nwake e2e")
console.log("─".repeat(60))

// ── the harness announces its thread, exactly as hooks.json does ──────────
// plugins/codex/hooks/hooks.json binds hook_poll to SessionStart with the
// thread id. This IS that call. The uuid is the only identifier `codex exec
// resume` accepts; nothing else in the system will ever produce it.
const THREAD = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
await call("hook_poll", { session_id: THREAD, event: "SessionStart", cwd: project })

// ── the agent registers, and the bridge names it host-<ppid> ──────────────
const BRIDGE_ID = `host-${process.pid}`
const sleeper = await call("register", {
  name: "sleeper", description: "wake e2e", session_id: BRIDGE_ID,
  cwd: project, harness: "Codex", nonce: "e2e-wake-nonce-0123456789abcdef0123456789abcdef",
})
// The uuid must arrive as an ALIAS and not displace the primary id. That is
// what makes the {thread} assertion below non-trivial: if the two were the same
// value, a waker that substituted SessionID would pass this file while still
// being the bug it was written to catch.
check("the bridge's host-<ppid> stays the agent's primary session id",
  sleeper.session_id === BRIDGE_ID,
  `session_id = ${JSON.stringify(sleeper.session_id)}, want ${BRIDGE_ID}: if the ` +
  `announced uuid became the PRIMARY id, this test would no longer distinguish a ` +
  `waker that reads SessionID from one that resolves the thread`)

const asker = await call("register", {
  name: "asker", description: "wake e2e", session_id: "asker-session",
  nonce: "e2e-asker-nonce-0123456789abcdef0123456789abcdef",
})

// ── an FYI wakes nobody ───────────────────────────────────────────────────
// Not a caveat: starting a process on the operator's machine for a message
// nobody is waiting on is the behaviour that would make this feature something
// an operator turns off.
await Bun.sleep(1200)
await call("send", { token: asker.token, to: "sleeper", type: "notify", body: "fyi, no reply needed" })
await settle()
check("a notify starts nothing", wakes().length === 0,
  `${wakes().length} wake(s) for a message nobody is blocked on`)

// ── a question wakes it, carrying the THREAD ──────────────────────────────
await call("send", { token: asker.token, to: "sleeper", type: "question", body: "are you there?", deadline_s: 600 })
await settle()
const first = wakes()
check("a question starts the operator's command", first.length === 1,
  `${first.length} wake(s); the agent is not running and nothing else can reach it`)
if (first.length === 1) {
  const [thread, message, agent, from, type] = first[0]
  check("{thread} is the harness thread, not the bridge's host-<ppid>",
    thread === THREAD,
    `got ${JSON.stringify(thread)}; a resume against ${BRIDGE_ID} names no thread and fails silently`)
  check("{agent} and {from} name the two ends", agent === "sleeper" && from === "asker",
    JSON.stringify([agent, from]))
  check("{type} is the message type", type === "question", JSON.stringify(type))
  check("{message} says to check the board", /board/i.test(message ?? ""),
    `got ${JSON.stringify(message)}: the woken agent must be told where to look, ` +
    `not merely that mail exists`)
}

// ── a burst is one wake ───────────────────────────────────────────────────
// Three questions arriving together are one reason to start a process, not
// three. Without this an agent that has fallen behind is woken once per unread.
await Bun.sleep(1200)
const before = wakes().length
await Promise.all([1, 2, 3].map(n =>
  call("send", { token: asker.token, to: "sleeper", type: "question", body: `burst ${n}`, deadline_s: 600 })))
await settle()
check("three questions at once are one wake", wakes().length - before === 1,
  `${wakes().length - before} wakes for a burst of three`)

// ── a FAILED wake does not consume the agent's one attempt ────────────────
// The cooldown is taken before the process starts, which is right: two messages
// arriving together must not become two processes. Keeping it after the command
// FAILED spent the single attempt this message was ever going to get on a
// process that woke nobody, and `send` still reported the mailbox written. That
// is this release's recurring shape, on the one path it exists to add.
//
// ITS OWN BOARD, with a long cooldown. On the board above the cooldown is one
// second, so a second wake would be allowed by simple expiry and would prove
// nothing; measuring inside that second works and is a race on a loaded
// machine, which is a flaky gate rather than a check. Sixty seconds makes the
// question unambiguous: a second wake in that window happened because the
// failure released the cooldown, or not at all.
await failedWakeDoesNotConsumeTheAttempt()

async function failedWakeDoesNotConsumeTheAttempt() {
  const d = mkdtempSync(join(tmpdir(), "dibs-wake-fail-"))
  const proj = join(d, "project")
  mkdirSync(proj)
  const rec = join(d, "recorder.ts")
  const lg = join(d, "wakes.jsonl")
  // One file per wake, like the recorder above and for the same reason: a
  // shared file is a read-modify-write, and two processes clobbering each other
  // look exactly like one process.
  await Bun.write(rec, `
const line = JSON.stringify(Bun.argv.slice(3)) + "\\n"
await Bun.write(Bun.argv[2] + "." + process.pid + "." + Bun.nanoseconds(), line)
if (await Bun.file(Bun.argv[2] + ".fail").exists()) process.exit(3)
`)
  await Bun.write(join(d, "dibs.toml"), `
[wake.exec.codex]
argv = ["${process.execPath}", "${rec}", "${lg}", "{thread}"]
cooldown = "60s"
`)
  const port = String(Number(PORT) + 1)
  const a = `127.0.0.1:${port}`
  const dae = Bun.spawn({
    cmd: [dibd, "-dir", d, "-addr", a],
    env: { ...process.env, DIBS_ALLOW_PARALLEL: "1" },
    stdout: "ignore", stderr: "ignore",
  })
  try {
    let sec = ""
    for (let i = 0; i < 60 && !sec; i++) {
      try { sec = (await Bun.file(`${d}/local.secret`).text()).trim() } catch { await Bun.sleep(100) }
    }
    if (!sec) {
      check("a failed wake does not consume the attempt", false,
        "the second daemon never came up, so this measured nothing")
      return
    }
    const c = async (name: string, args: Record<string, unknown>) => {
      const r = await fetch(`http://${a}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": sec },
        body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name, arguments: args } }),
      })
      return JSON.parse(((await r.json()) as any).result.content[0].text)
    }
    // One file per wake, matching the recorder: counting lines in a shared
    // file cannot distinguish one process from three that clobbered each other.
    const count = () => readdirSync(d)
      .filter(f => f.startsWith(basename(lg) + ".") && !f.endsWith(".fail")).length

    await c("hook_poll", { session_id: THREAD, event: "SessionStart", cwd: proj })
    await c("register", {
      name: "sleeper", description: "wake e2e", session_id: `host-${process.pid}`,
      cwd: proj, harness: "Codex", nonce: "e2e-fail-nonce-0123456789abcdef0123456789abcd",
    })
    const from = await c("register", {
      name: "asker", description: "wake e2e", session_id: "asker-session",
      nonce: "e2e-failasker-nonce-0123456789abcdef01234567",
    })
    // A finished turn, so the 60s cooldown does not also read as "recently in
    // touch" and suppress the wake for a reason that is not the one under test.
    await c("hook_poll", { session_id: THREAD, event: "Stop", cwd: proj })

    await Bun.write(lg + ".fail", "x")
    await c("send", { token: from.token, to: "sleeper", type: "question", body: "will fail", deadline_s: 600 })
    await settle(2500)
    if (count() !== 1) {
      check("a failed wake does not consume the attempt", false,
        `the failing command ran ${count()} times, not once, so nothing below ` +
        "says anything about what happens when one fails")
      return
    }

    await Bun.file(lg + ".fail").unlink()
    await c("hook_poll", { session_id: THREAD, event: "Stop", cwd: proj })
    await c("send", { token: from.token, to: "sleeper", type: "question", body: "after the failure", deadline_s: 600 })
    await settle(2500)
    check("a failed wake does not consume the attempt", count() > 1,
      "no second wake inside a 60s cooldown. The failed command woke nobody and " +
      "kept the cooldown, so this agent is unreachable until some unrelated " +
      "event happens to arrive after the window, while send reported success")
  } finally {
    try { dae.kill() } catch {}
    try { rmSync(d, { recursive: true, force: true }) } catch {}
  }
}

// ── an agent with no resumable identifier is left alone ───────────────────
// Its only name is the bridge's host-<ppid>. There is nothing to resume, so
// running the command would start a process that fails: silently, on the
// operator's machine, for every message.
await call("register", {
  name: "nothread", description: "wake e2e", session_id: `host-${process.pid + 1}`,
  harness: "Codex", nonce: "e2e-nothread-nonce-0123456789abcdef0123456789ab",
})
await Bun.sleep(1200)
const beforeNoThread = wakes().length
await call("send", { token: asker.token, to: "nothread", type: "question", body: "anyone?", deadline_s: 600 })
await settle()
check("an agent with no thread id is not woken", wakes().length === beforeNoThread,
  "the command would have been handed host-<ppid>, which resolves to no thread")

console.log("─".repeat(60))
console.log(failures === 0 ? `\x1b[32m${checks} checks passed\x1b[0m` : `\x1b[31m${failures} of ${checks} failed\x1b[0m`)
process.exit(failures === 0 ? 0 : 1)
