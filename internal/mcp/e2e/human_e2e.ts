/**
 * The human flow, end to end, against two real daemons.
 *
 * This suite exists because the panel's human actions were the only surface in
 * Dibs with no automated coverage at all, for an honest reason: they are gated
 * on a fingerprint, and no test can produce one. The `dibdev` build tag adds a
 * scripted verdict so the flow becomes drivable, and the moment it did, the
 * flow had to be tested, because a mock that only ever gets used by hand is just
 * an untested security switch with a convenience feature attached.
 *
 * Two daemons run here, and the second one is the point:
 *
 *   dev     : built with `-tags dibdev`, DIBS_PRESENCE_MOCK set. Every
 *              branch of the flow, including the two failure verdicts that a Mac
 *              with a working sensor cannot otherwise reach.
 *   release : the ordinary build, same environment variable, deliberately set
 *              to the most dangerous value. It must refuse to unlock.
 *
 * Unit tests already assert the tag boundary inside the package. This asserts it
 * where it actually matters: in a shipped binary, over the real transport, with
 * the environment already hostile. If that check ever goes green on the release
 * daemon, an agent that can set an environment variable can speak as the
 * operator, and every other assertion in this file is worthless.
 */
import { copyFileSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

let failures = 0
function check(name: string, ok: boolean, detail = "") {
  if (ok) console.log(`  \x1b[32m✓\x1b[0m ${name}`)
  else { failures++; console.log(`  \x1b[31m✗\x1b[0m ${name}${detail ? ". " + detail : ""}`) }
}

const DEV_ADDR = "127.0.0.1:47811"
const REL_ADDR = "127.0.0.1:47812"

const devBin = process.env.LANESD_DEV ?? `${process.cwd()}/bin/dibd-dev`
const relBin = process.env.DIBD ?? `${process.cwd()}/bin/dibd`

type Node = { dir: string; addr: string; proc: ReturnType<typeof Bun.spawn>; secret: string }
const nodes: Node[] = []

/**
 * Copy a daemon somewhere with no presence helper beside it.
 *
 * The helper is resolved from the daemon's own directory, so a release daemon
 * run out of bin/ finds the real one and raises a Touch ID sheet, which is
 * correct behaviour and completely wrong for a test suite. The first run of this
 * file put a fingerprint prompt in front of a machine whose owner was away, and
 * then recorded their absence as the result. A suite that interrupts somebody is
 * a defect, and one whose outcome depends on whether they happened to be at the
 * desk is not a test.
 *
 * Isolating it also makes the refusal deterministic: no helper means
 * Unavailable, on every machine, in CI as much as here: without weakening what
 * is being asked. The question is whether an environment variable can assert
 * that a human is present, and that is independent of whether a sensor exists.
 */
function isolate(bin: string): string {
  const dir = mkdtempSync(join(tmpdir(), "agents-nohelper-"))
  const dest = join(dir, "dibd")
  copyFileSync(bin, dest)
  return dest
}

function start(bin: string, addr: string, mock: string): Node {
  const dir = mkdtempSync(join(tmpdir(), "agents-human-"))
  const proc = Bun.spawn({
    cmd: [bin, "-dir", dir, "-addr", addr],
    // The variable is passed explicitly rather than inherited, so this file
    // states the hostile condition it is testing under instead of depending on
    // whatever the caller's shell happened to hold.
    env: { ...process.env, DIBS_PRESENCE_MOCK: mock, DIBS_ALLOW_PARALLEL: "1" },
    stdout: "ignore", stderr: "ignore",
  })
  const node: Node = { dir, addr, proc, secret: "" }
  nodes.push(node)
  return node
}

function cleanup() {
  for (const n of nodes) {
    try { n.proc.kill() } catch {}
    try { rmSync(n.dir, { recursive: true, force: true }) } catch {}
  }
}
process.on("exit", cleanup)

let rpcId = 0
async function rpc(node: Node, method: string, params: unknown): Promise<any> {
  const res = await fetch(`http://${node.addr}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": node.secret },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method, params }),
  })
  const body = (await res.json()) as any
  if (body.error) throw new Error(method + ": " + JSON.stringify(body.error))
  return body.result
}
const tool = (n: Node, name: string, args: unknown) =>
  rpc(n, "tools/call", { name, arguments: args })
const textOf = (r: any) => JSON.parse(r.content[0].text)

/**
 * Call a tool and return the payload whether it succeeded or was refused.
 *
 * A malformed call is NOT a refusal, and the distinction is load-bearing. The
 * first draft of this suite reported "a release daemon refuses to unlock" as a
 * pass when the call had actually failed with -32602 for a missing argument: the
 * daemon never reached the presence check at all, and the strongest assertion in
 * the file was being satisfied by a typo. A protocol-level error therefore kills
 * the suite instead of feeding an assertion: a security check that a broken
 * probe can pass is worse than no check, because it reads as evidence.
 */
async function attempt(n: Node, name: string, args: unknown): Promise<any> {
  try { return textOf(await tool(n, name, args)) } catch (e) {
    const msg = String(e)
    if (msg.includes("-32602") || msg.includes("-32601")) {
      throw new Error(`${name} was called wrongly, so nothing was tested: ${msg}`)
    }
    return { _threw: msg }
  }
}

/**
 * Did the call actually succeed?
 *
 * `code === undefined` alone is NOT success. attempt() turns any non-protocol
 * error into `{_threw: ...}`, an object with no `code`, so an internal error, a
 * dropped connection or a daemon that died mid-suite satisfied every success
 * check in this file: join, post and mail all reported green off a transport
 * failure. Both halves have to be true: nothing threw, and nothing was refused.
 */
function succeeded(r: any): boolean {
  return r && r._threw === undefined && r.code === undefined
}

/** Register an agent and fail loudly if it did not work. */
async function enrol(n: Node, name: string, session: string): Promise<any> {
  const r = textOf(await tool(n, "register", {
    name, description: "human-flow e2e", session_id: session }))
  if (typeof r.token !== "string" || !r.token) {
    throw new Error(`register(${name}) returned no token: ${JSON.stringify(r)}`)
  }
  return r
}

async function waitUp(n: Node) {
  for (let i = 0; ; i++) {
    try { await fetch(`http://${n.addr}/`); break } catch {
      if (i > 60) throw new Error(`daemon at ${n.addr} never came up`)
      await Bun.sleep(200)
    }
  }
  n.secret = (await Bun.file(join(n.dir, "local.secret")).text()).trim()
}

try {
  const dev = start(devBin, DEV_ADDR, "verified")
  const rel = start(isolate(relBin), REL_ADDR, "verified")
  await waitUp(dev)
  await waitUp(rel)

  // ── the boundary ─────────────────────────────────────────────────────────
  // First, because if this fails nothing else in the file means anything.
  console.log("\n  the release binary, with the environment already hostile")
  // Enrol first: human_unlock takes the calling agent's own token, so without a
  // real one the call fails on arguments and never reaches the presence check.
  const relCaller = await enrol(rel, "prober", "human-e2e-rel")
  const refused = await attempt(rel, "human_unlock",
    { token: relCaller.token, note: "e2e boundary probe" })
  check("a release daemon refuses to unlock with DIBS_PRESENCE_MOCK=verified",
    refused.unlocked === false,
    `unlocked=${refused.unlocked}: an env var just spoke as the operator`)
  check("and it does not report itself as mocked",
    refused.mocked === undefined,
    "the release build compiled the mock in")
  check("it hands back no token", refused.token === undefined)
  check("it says the machine cannot check, having no helper to ask",
    refused.reason === "unavailable", `reason=${refused.reason}`)

  // ── the three verdicts ───────────────────────────────────────────────────
  console.log("\n  every verdict, including the two a working sensor hides")

  // The dev daemon was started with "verified"; to reach the other branches the
  // daemon has to be started again with a different script, since the verdict is
  // read from its own environment. Two more short-lived daemons, one per branch.
  const devCaller = await enrol(dev, "prober", "human-e2e-dev")
  const ok = await attempt(dev, "human_unlock", { token: devCaller.token, note: "probe" })
  check("verified unlocks", ok.unlocked === true, JSON.stringify(ok).slice(0, 160))
  check("and the result says a human was NOT checked",
    ok.mocked === true && String(ok.mocked_note).includes("NO HUMAN WAS CHECKED"),
    "a mocked unlock is indistinguishable from a real one in the transcript")
  check("it hands back a token", typeof ok.token === "string" && ok.token.length > 0)
  check("and names the identity it handed back", typeof ok.agent === "string")

  for (const [verdict, wants] of [
    ["declined", "declined"],
    ["unavailable", "unavailable"],
  ] as const) {
    const n = start(devBin, `127.0.0.1:${verdict === "declined" ? 47813 : 47814}`, verdict)
    await waitUp(n)
    const caller = await enrol(n, "prober", "human-e2e-" + verdict)
    const r = await attempt(n, "human_unlock", { token: caller.token, note: "probe" })
    check(`${verdict} does not unlock`, r.unlocked === false, JSON.stringify(r).slice(0, 160))
    check(`${verdict} is reported as itself`, r.reason === wants, `reason=${r.reason}`)
    check(`${verdict} hands back no token`, r.token === undefined,
      "a refused unlock returned a credential")
    // Labelling was asserted only on the Verified branch, so stripping it from
    // the two refusals would have left every check green, and those are the
    // branches a reader is most likely to meet while wondering whether the
    // check was real.
    check(`${verdict} still says no human was checked`,
      r.mocked === true && String(r.mocked_note).includes("NO HUMAN WAS CHECKED"),
      JSON.stringify(r).slice(0, 200))
    // The two failures must not give the same advice. Telling somebody with no
    // sensor to try their finger again is the project's named failure mode.
    if (verdict === "unavailable") {
      check("unavailable sends them to `dibs web`, not back to the sensor",
        String(r.hint).includes("dibs web") && !String(r.hint).includes("again"),
        `hint=${r.hint}`)
    } else {
      check("declined says nothing was sent",
        String(r.hint).includes("nothing was sent"), `hint=${r.hint}`)
    }
  }

  // ── what the unlocked token can actually do ──────────────────────────────
  // The token is deliberately NOT a new capability: it is the operator's own
  // ordinary agent identity. So it must be bound by the same membership rules
  // as any agent, which is exactly the thing the panel got wrong once, by
  // offering a Broadcast button that returned E_NOT_MEMBER when pressed.
  console.log("\n  the unlocked token is an ordinary agent, not a superuser")
  const human = ok.token as string

  const worker = textOf(await tool(dev, "register", {
    name: "worker", description: "doing the work", session_id: "human-e2e-1" }))
  await tool(dev, "check_in", { token: worker.token })
  await tool(dev, "open_space", { token: worker.token, agent: "auth-work", topic: "auth" })

  const posted = await attempt(dev, "post",
    { token: human, agent: "auth-work", body: "how is this going?" })
  // A refusal arrives as a SUCCESSFUL tool result carrying an error code, not as
  // a JSON-RPC error, so the assertion names the code. Testing for a thrown
  // exception here silently passed while the post was in fact succeeding.
  check("posting to an agent the human has not joined is refused",
    posted.code === "E_NOT_MEMBER",
    `the human token bypassed membership: ${JSON.stringify(posted).slice(0, 200)}`)
  check("and the refusal tells them how to proceed",
    String(posted.hint).includes("join_space"), `hint=${posted.hint}`)

  const humanAck = await attempt(dev, "check_in", { token: human })
  check("the human can acknowledge the board", succeeded(humanAck),
    JSON.stringify(humanAck).slice(0, 200))
  const joined = await attempt(dev, "join_space", { token: human, agent: "auth-work" })
  check("the human can join the agent", succeeded(joined),
    JSON.stringify(joined).slice(0, 200))

  const posted2 = await attempt(dev, "post",
    { token: human, agent: "auth-work", body: "how is this going?" })
  check("and then posting succeeds", succeeded(posted2),
    JSON.stringify(posted2).slice(0, 200))

  // Mail needs no membership, it is addressed, not broadcast, so the panel is
  // right to offer it unconditionally.
  const sent = await attempt(dev, "send", {
    token: human, to: worker.lane_id, type: "question",
    body: "Are you blocked?", op_id: "human-e2e-mail" })
  check("mail to a specific agent needs no membership",
    succeeded(sent), JSON.stringify(sent).slice(0, 200))

  // ── and it is ledgered like everything else ──────────────────────────────
  // A parallel privileged write path would be invisible to `dibs verify`. The
  // point of routing the human through an ordinary agent token is that there is
  // nothing special to audit, so the audit must show ordinary records.
  const events = await attempt(dev, "events_since", { token: worker.token, since_serial: 0 })
  const stream = JSON.stringify(events)
  // AND, not OR. As an OR either the post or the message could disappear from
  // the event stream while the check still reported the human's actions were
  // ledgered, and a missing write is exactly what this exists to catch.
  check("the human's agent post is in the ledger",
    stream.includes("agent.post"),
    "no agent.post event in the stream at all")
  // The BODY is not in the stream, and that is the point of checking here: a
  // agent post is public to the LANE, not to the board, and space events carry
  // no recipient, so a body in this stream is a body every authenticated agent
  // can read, member or not.
  check("but its body is not, because the stream reaches non-members too",
    !stream.includes("how is this going?"),
    stream.slice(0, 240))
  const laneView = await attempt(dev, "read_space", { token: worker.token, agent: "auth-work" })
  check("a member of the agent can read what the human posted",
    JSON.stringify(laneView).includes("how is this going?"),
    JSON.stringify(laneView).slice(0, 240))
  // Mail is checked in the INBOX, not the event stream, and that distinction is
  // the design rather than a workaround. A message is private to sender and
  // recipient, so a body appearing in a stream any member can read would be the
  // bug. The original OR papered over exactly this: it passed on the post alone
  // and would have kept passing if mail had stopped being delivered entirely.
  const box = await attempt(dev, "inbox", { token: worker.token })
  check("the human's message was delivered to the recipient's inbox",
    JSON.stringify(box).includes("Are you blocked?"),
    JSON.stringify(box).slice(0, 240))
  check("and its body is NOT in the shared event stream, because mail is private",
    !stream.includes("Are you blocked?"),
    "a private message body is readable from the agent event stream")
} catch (e) {
  failures++
  console.log(`\n  \x1b[31m✗ suite threw\x1b[0m. ${e}`)
}

console.log(failures === 0
  ? `\n\x1b[32mhuman flow: all checks passed\x1b[0m`
  : `\n\x1b[31mhuman flow: ${failures} failed\x1b[0m`)
process.exit(failures === 0 ? 0 : 1)
