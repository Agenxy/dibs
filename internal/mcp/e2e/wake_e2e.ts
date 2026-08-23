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
import { mkdtempSync, rmSync, mkdirSync, existsSync, readFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

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
await Bun.write(recorder, `
const line = JSON.stringify(Bun.argv.slice(3)) + "\\n"
await Bun.write(Bun.argv[2], (await Bun.file(Bun.argv[2]).text().catch(() => "")) + line)
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
function wakes(): string[][] {
  if (!existsSync(log)) return []
  return readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map(l => JSON.parse(l))
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
