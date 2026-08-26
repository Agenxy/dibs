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
import { mkdtempSync, rmSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { basename, dirname, join } from "node:path"
import { daemonReady } from "./ready.ts"

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

// ── a harness session the daemon can actually reach ──────────────────────
// The socket wake is the route that needs no [wake.exec] entry, so proving it
// needs a session that really is listening. This stands up what Claude Code
// publishes: a key file, a sidecar, and a bound socket.
//
// THIS PROCESS'S OWN PID, because the discovery guard checks the pid is alive
// before offering a session. An invented one is correctly refused, so a fixture
// that used one would be asserting nothing.
const PEER_PID = process.pid
const PEER_SESSION = "5e6d7c8b-9a01-4b2c-8d3e-4f5a6b7c8d9e"
const peerHome = mkdtempSync(join(tmpdir(), "wakehome-"))
mkdirSync(join(peerHome, ".claude", "sessions"), { recursive: true })
writeFileSync(
  join(peerHome, ".claude", "sessions", `${PEER_PID}.${"c".repeat(64)}.key`),
  JSON.stringify({ peerToken: "d".repeat(32) }))
writeFileSync(
  join(peerHome, ".claude", "sessions", `${PEER_PID}.json`),
  JSON.stringify({ pid: PEER_PID, sessionId: PEER_SESSION, cwd: project }))
// A SHORT runtime dir: a unix socket path is bounded near 104 bytes, and the
// default temp dir is long enough on macOS that binding under it fails.
const peerRun = mkdtempSync("/tmp/wr")
mkdirSync(join(peerRun, "cc-socks"), { recursive: true })
const peerSock = join(peerRun, "cc-socks", `${PEER_PID}.sock`)
let peerGot = ""
let peerResolve: (v: string) => void = () => {}
const peerHeard = new Promise<string>((r) => { peerResolve = r })
Bun.listen({
  unix: peerSock,
  socket: {
    data(_s, d) {
      peerGot += d.toString()
      if (peerGot.split("\n").filter(Boolean).length >= 2) peerResolve(peerGot)
    },
  },
})

const daemonErr = join(dir, "dibd.err")
const daemon = Bun.spawn({
  cmd: [dibd, "-dir", dir, "-addr", ADDR],
  env: {
    ...process.env, DIBS_ALLOW_PARALLEL: "1",
    HOME: peerHome, XDG_RUNTIME_DIR: peerRun,
  },
  stdout: "ignore", stderr: Bun.file(daemonErr),
})
const cleanup = () => {
  try { daemon.kill() } catch {}
  try { rmSync(dir, { recursive: true, force: true }) } catch {}
}
process.on("exit", cleanup)

// Waits for the LISTENER, not for local.secret: see ready.ts.
let secret = ""
try {
  secret = await daemonReady(dir, `http://${ADDR}`, { proc: daemon, label: "wake" })
} catch (err) {
  // A failing probe is usually a broken probe: say WHY the daemon did not come
  // up rather than leaving a reader to guess. This test spent a run on a
  // refusal it swallowed.
  console.error(`${(err as Error).message}. Its stderr:`)
  try { console.error(readFileSync(daemonErr, "utf8")) } catch {}
  process.exit(1)
}

let rpcId = 0
async function call(name: string, args: Record<string, unknown>, meta?: Record<string, unknown>): Promise<any> {
  const params: Record<string, unknown> = { name, arguments: args }
  if (meta) params._meta = meta
  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params }),
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

// ── a burst does not scale into processes ────────────────────────────────
// Six questions arriving together are one reason to start a process, not six.
// Without this an agent that has fallen behind is woken once per unread.
//
// SIX, not three, so the count distinguishes coalescing from arithmetic: three
// would be indistinguishable from "one per message" the moment a re-ask is also
// legitimate, and six is not.
//
// A PROPERTY, covered by two suppressors, and a passing run is not proof that
// both work. `running` excludes a second process while one is alive and the
// cooldown excludes one started too recently; either alone coalesces this
// burst, so disabling one leaves this green. Verified by disabling BOTH, which
// produces exactly six. Anything that needs to know which mechanism fired is a
// unit test in internal/engine, and both have one.
//
// One or two, and the second is deliberate. Mail that arrives while a wake is
// running is re-asked when that wake EXITS, because the command reads its inbox
// near the start of a turn bounded at two hours and anything arriving after
// that read would otherwise sit until an unrelated event. This recorder never
// reads anything, so the mail is still blocking at the exit and the board
// correctly tries once more; a real agent answers, hasBlockingMail goes false,
// and nothing fires. The re-ask lands inside this window only because the
// cooldown here is one second, and it happens at most once because no new mail
// arrives during the second command.
await Bun.sleep(1200)
const before = wakes().length
await Promise.all([1, 2, 3, 4, 5, 6].map(n =>
  call("send", { token: asker.token, to: "sleeper", type: "question", body: `burst ${n}`, deadline_s: 600 })))
await settle()
const burst = wakes().length - before
check("a burst of six is one wake, and at most one re-ask", burst >= 1 && burst <= 2,
  `${burst} wakes for a burst of six: one per message is the failure this ` +
  `catches, and more than two means the exit re-check is looping rather than ` +
  `asking once`)

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
    try {
      sec = await daemonReady(d, `http://${a}`, { proc: dae, label: "second-wake" })
    } catch (err) {
      check("a failed wake does not consume the attempt", false,
        `the second daemon never came up, so this measured nothing: ${(err as Error).message}`)
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

// ── mail arriving DURING a long turn reaches the agent ───────────────────
// The ordering the wake path has been rewritten four times for, and the one
// nothing here has ever run: every recorder above exits at once, so the whole
// question of what happens during a two-hour turn was answered only by unit
// tests that call the pieces in the order they expect.
//
// The three defects those unit tests missed were all in that gap. Mail arriving
// after the woken agent read its inbox was discarded, because reading the inbox
// is a call to Dibs and made the agent look busy. Then it was recorded and the
// re-check still could not get past the same test. Then the two facts the exit
// produces were published separately and a message landing between them saw
// neither.
//
// So: a recorder that CHECKS IN, like a real woken agent, and then keeps
// running. Mail arrives while it is alive. It exits. Nothing else happens.
await mailDuringALongTurnIsNotLost()

async function mailDuringALongTurnIsNotLost() {
  const name = "mail during a long turn reaches the agent after it ends"
  const d = mkdtempSync(join(tmpdir(), "dibs-wake-slow-"))
  const proj = join(d, "project")
  mkdirSync(proj)
  const rec = join(d, "recorder.ts")
  const lg = join(d, "wakes.jsonl")
  const tokenFile = join(d, "sleeper.token")
  const port = String(Number(PORT) + 2)
  const a = `127.0.0.1:${port}`

  // Records the wake, then behaves like the agent it stands in for: calls
  // check_in, which is what makes it "recently in touch", and stays alive.
  await Bun.write(rec, `
await Bun.write(Bun.argv[2] + "." + process.pid + "." + Bun.nanoseconds(), "woken")
const tok = (await Bun.file(Bun.argv[3]).text()).trim()
const sec = (await Bun.file(Bun.argv[4]).text()).trim()
// A real agent coordinates THROUGHOUT its turn, not only at the start. The
// second one, just before exiting, is what makes the agent "recently in touch"
// at the moment the command ends: without it the recency simply lapses while
// the process sleeps, and the exit's turn-end record is never load-bearing.
const checkIn = () => fetch("http://${a}/mcp", {
  method: "POST",
  headers: { "content-type": "application/json", "X-Dibs-Local": sec },
  body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call",
    params: { name: "check_in", arguments: { token: tok } } }),
})
await checkIn()
await Bun.sleep(3000)
await checkIn()
`)
  await Bun.write(join(d, "dibs.toml"), `
[wake.exec.codex]
argv = ["${process.execPath}", "${rec}", "${lg}", "${tokenFile}", "${join(d, "local.secret")}"]
cooldown = "6s"
`)
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
    if (!sec) { check(name, false, "the third daemon never came up"); return }
    const c = async (tool: string, args: Record<string, unknown>) => {
      const r = await fetch(`http://${a}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": sec },
        body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method: "tools/call", params: { name: tool, arguments: args } }),
      })
      return JSON.parse(((await r.json()) as any).result.content[0].text)
    }
    const count = () => readdirSync(d).filter(f => f.startsWith(basename(lg) + ".")).length

    await c("hook_poll", { session_id: THREAD, event: "SessionStart", cwd: proj })
    const sleeper = await c("register", {
      name: "sleeper", description: "wake e2e", session_id: `host-${process.pid}`,
      cwd: proj, harness: "Codex", nonce: "e2e-slow-nonce-0123456789abcdef0123456789ab",
    })
    await Bun.write(tokenFile, sleeper.token)
    const from = await c("register", {
      name: "asker", description: "wake e2e", session_id: "asker-slow",
      cwd: proj, nonce: "e2e-slow-asker-0123456789abcdef0123456789ab",
    })
    // Its turn is over: this is an agent that has stopped.
    await c("hook_poll", { session_id: THREAD, event: "Stop", cwd: proj })

    await c("send", { token: from.token, to: "sleeper", type: "question", body: "first", deadline_s: 600 })
    await settle(1200)
    if (count() !== 1) {
      check(name, false, `the first question started ${count()} command(s), not one, ` +
        "so there is no running turn for the second to arrive during")
      return
    }

    // The command is alive and has checked in. This is the moment that has
    // never been exercised: the agent looks busy, and it is about to stop.
    await c("send", { token: from.token, to: "sleeper", type: "question", body: "during the turn", deadline_s: 600 })
    await settle(1000)
    if (count() !== 1) {
      check(name, false, "a second command started beside the first, which is two " +
        "activations of one thread: the coalescing the running check exists for")
      return
    }

    // Let it exit, plus the cooldown, plus room for the re-check.
    await settle(9000)
    check(name, count() > 1,
      "the question that arrived during the turn woke nobody after it ended. " +
      "The agent read its inbox before that message existed, so it never saw it, " +
      "and send reported it delivered: this is the case the exit re-check exists " +
      "for and the one no recorder here could reach")
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

// ── DELIVERY REACHES THE AGENT THAT OWNS THE MAILBOX ─────────────────────
// The property Dibs exists for, asserted end to end against a real daemon,
// because everything above tests the WAKE COMMAND and none of it tests who the
// notification is actually about.
//
// This is the failure that made Dibs' own messaging worse than the harness's
// native channel on this project's board: an agent registered under one session
// id while its lifecycle hooks quoted another, so hook_poll resolved it to
// nobody, or worse to a DIFFERENT agent, and its mail surfaced in that agent's
// context instead. Three agents spent a night on it. Nothing here would have
// caught it, because no check asked "does the digest name the right agent".
//
// Both harness shapes are covered. Codex is the block far above: the thread
// uuid arrives as an alias and the wake command resolves it. This is the Claude
// Code shape, where the hooks quote the harness SESSION id and the agent must
// be reachable by exactly that.
const CC_SESSION = "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"
const cc = await call("register", {
  name: "cc-agent", description: "wake e2e", session_id: CC_SESSION,
  cwd: project, harness: "Claude Code",
  nonce: "e2e-cc-nonce-0123456789abcdef0123456789abcdef01",
})
check("an agent registers under the session id its hooks will quote",
  cc.session_id === CC_SESSION,
  `session_id = ${JSON.stringify(cc.session_id)}, want ${CC_SESSION}`)

// Nothing waiting yet: the digest must not invent news.
const quiet = await call("hook_poll", { session_id: CC_SESSION, event: "Stop", cwd: project })
const quietText = JSON.stringify(quiet ?? {})
check("a hook for an agent with no mail announces none",
  !/unread message/.test(quietText),
  `got ${quietText.slice(0, 200)}`)

await call("send", { token: asker.token, to: "cc-agent", type: "question",
  body: "does delivery reach the right mailbox?", deadline_s: 600 })
await settle()

const mine = JSON.stringify(await call("hook_poll",
  { session_id: CC_SESSION, event: "Stop", cwd: project }) ?? {})
check("the hook names the agent that owns the mailbox",
  /cc-agent/.test(mine) && /unread message/.test(mine),
  `the digest for ${CC_SESSION} does not announce cc-agent's mail: ${mine.slice(0, 300)}`)
check("and it names who the message is from",
  /asker/.test(mine),
  `the digest does not say who is waiting: ${mine.slice(0, 300)}`)

// THE HALF THAT ACTUALLY BROKE. Another agent's hook must not carry this
// mailbox. On the live board a swept row freed a session id, the next agent in
// that directory inherited it, and one agent's unread list was rendered into
// another's context for hours.
const theirs = JSON.stringify(await call("hook_poll",
  { session_id: THREAD, event: "Stop", cwd: project }) ?? {})
check("another agent's hook names ITS OWN agent, not this mailbox",
  /sleeper/.test(theirs) && !/cc-agent/.test(theirs),
  `the digest for sleeper's session should name sleeper and never cc-agent: ` +
  `${theirs.slice(0, 300)}. Anything else is one agent's mail announced into ` +
  `another agent's context`)

// And an id nobody holds resolves to nobody, rather than to whoever is nearest.
const stranger = JSON.stringify(await call("hook_poll",
  { session_id: "5d9e1c77-0000-4000-8000-000000000000", event: "Stop", cwd: project }) ?? {})
// Asserted on ATTRIBUTION in either shape it can take, not on any particular
// NAME. Three versions of this check were decorative before this one. The first
// asked only that cc-agent was absent, which a resolver handing the stranger a
// different neighbour passes. The second asked for "for your agent", which
// misses the {"agent": ...,"queued": ...} shape the same endpoint returns when
// it declines to extend a turn. Both were verified by mutating hook resolution
// to answer every session with whichever agent had mail: the suite reported all
// checks passing while every hook carried the wrong mailbox.
check("an unheld session id resolves to nobody, not to a neighbour",
  !/for your agent/.test(stranger) && !/"agent":"/.test(stranger),
  `an unknown session was attributed to an agent: ${stranger.slice(0, 300)}. ` +
  `The directory fallback must not hand a stranger somebody else's mailbox`)

// ── THE CALLER'S OWN SESSION BEATS THE DIRECTORY GUESS ───────────────────
// When no alias arrives, the engine INFERS a session by directory: it takes an
// id announced from this cwd recently and assumes the agent registering now is
// that session. It skips ids an agent already holds, which is not the same as
// ids still in USE. So a swept row frees a LIVE session's id and the next agent
// registering in that directory inherits it, along with its wake stream. That
// happened on this project's own board and took three agents a night to find.
//
// The stdio bridge already sends the session it runs inside on every call, so
// there is nothing to infer for anything behind it. This proves the ordering:
// an id that arrives with the call wins, and the guess is only for callers that
// send none.
const GHOST = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"   // announced, then abandoned
const OWN = "1f2e3d4c-5b6a-4978-8695-a4b3c2d1e0f9"      // this caller's real one

// A session announces from the project directory and its agent never registers,
// which is exactly the state a sweep leaves behind.
await call("hook_poll", { session_id: GHOST, event: "SessionStart", cwd: project })
await Bun.sleep(300)

const claimed = await call("register", {
  name: "own-session", description: "wake e2e", cwd: project, harness: "Claude Code",
  nonce: "e2e-own-nonce-0123456789abcdef0123456789abcdef",
}, { "com.dibs/session": OWN })

// ASSERTED ON WHAT A HOOK RESOLVES TO, not on the register result. Two earlier
// versions of this check were wrong about where to look. The first asked only
// that the stranger's id was absent from the result, which binding NOTHING also
// satisfies, so it passed against the unfixed commit. The second asked the
// result to echo the caller's id, and the result echoes the PRIMARY session id
// while this binds an alias, so it failed against the fix. The behaviour that
// matters is not in that field at all: it is which agent a hook reaches.
const ghostHook = JSON.stringify(await call("hook_poll",
  { session_id: GHOST, event: "Stop", cwd: project }) ?? {})
// A REGRESSION GUARD, not a reproduction, and worth labelling as such. Against
// the commit before this fix it also passes, because there the agent bound
// nothing at all rather than binding the stranger. It earns its place by
// failing if preferring the caller's id is ever removed in a way that lets the
// directory guess capture a live session; it does not by itself demonstrate the
// original defect. The check below is the one that fails without the fix.
check("registering does not capture a stranger's announced session",
  !/own-session/.test(ghostHook),
  `a hook quoting ${GHOST}, announced by a DIFFERENT session in this directory, ` +
  `resolves to own-session: ${ghostHook.slice(0, 300)}. That agent has taken ` +
  `over somebody else's wake stream without asking for it`)

// And its own id must actually reach it, or the wake path is no better off.
await call("send", { token: asker.token, to: "own-session", type: "question",
  body: "does the caller's own id resolve?", deadline_s: 600 })
await settle()
const own = JSON.stringify(await call("hook_poll",
  { session_id: OWN, event: "Stop", cwd: project }) ?? {})
check("and a hook quoting that id reaches it",
  /own-session/.test(own) && /unread message/.test(own),
  `a hook for ${OWN} did not reach own-session: ${own.slice(0, 300)}`)

// ── A WAKE OVER THE HARNESS SOCKET, WITH NO COMMAND CONFIGURED ───────────
// The route that removes the operator from the loop. dibs.toml above declares a
// command for `codex` and nothing else, so an agent on another harness has no
// command at all: before this existed it simply could not be woken, which is
// the state most agents on a real machine are in.
//
// Everything here is the real thing: a real daemon, a real unix socket, a real
// message through send. What is synthetic is only the session fixture, and its
// pid is this process's own so the liveness guard is genuinely satisfied.
const socketAgent = await call("register", {
  name: "socket-sleeper", description: "wake e2e", cwd: project,
  harness: "Claude Code", session_id: PEER_SESSION,
  nonce: "e2e-socket-nonce-0123456789abcdef0123456789abcd",
})
check("an agent on a harness with no configured command still registers",
  socketAgent.session_id === PEER_SESSION,
  `session_id = ${JSON.stringify(socketAgent.session_id)}`)

const beforeSocket = wakes().length
await Bun.sleep(1200)
await call("send", { token: asker.token, to: "socket-sleeper", type: "question",
  body: "can you be reached without a wake command?", deadline_s: 600 })

const heard = await Promise.race([
  peerHeard,
  new Promise<string>((r) => setTimeout(() => r(""), 8000)),
])

check("a question reaches it over its own harness socket",
  heard !== "",
  "nothing arrived on the session socket within 8s. An agent whose harness has " +
  "no [wake.exec] entry is unreachable again, which is the defect this route exists to remove")

if (heard !== "") {
  const lines = heard.split("\n").filter(Boolean)
  let auth: any = {}, msg: any = {}
  try { auth = JSON.parse(lines[0]) } catch {}
  try { msg = JSON.parse(lines[1] ?? "{}") } catch {}
  check("the connection authenticates before it speaks",
    auth.type === "auth" && auth.token === "d".repeat(32),
    `first line was ${JSON.stringify(lines[0])}; the harness refuses a connection ` +
    `whose first line is not the auth line`)
  check("and the notice tells it to check the board",
    msg.type === "user" && /board/i.test(msg.message?.content ?? ""),
    `second line was ${JSON.stringify(lines[1])}`)
  check("the socket wake carries no message body",
    !/reached without a wake command/.test(heard),
    "the question's text was delivered to the harness socket. A wake says mail " +
    "EXISTS; the agent reads it with its own token, which is why mail is encrypted at rest")
}

check("and no process was spawned for it",
  wakes().length === beforeSocket,
  `${wakes().length - beforeSocket} command(s) ran for an agent whose harness has ` +
  `none configured: the socket route must not also spend a process`)

console.log("─".repeat(60))
console.log(failures === 0 ? `\x1b[32m${checks} checks passed\x1b[0m` : `\x1b[31m${failures} of ${checks} failed\x1b[0m`)
process.exit(failures === 0 ? 0 : 1)
