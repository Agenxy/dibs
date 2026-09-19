/**
 * End-to-end test for a board that spans two machines, on one.
 *
 * SPEC §16 has shipped remote agents since v1: bind `--addr` to a reachable
 * address and the daemon serves HTTPS with a certificate it made, a second
 * machine copies the secret, pins the fingerprint with `dibs trust`, and its
 * bridge asserts which computer it is on. docs/NETWORK.md §3 then promises two
 * things about claims that only mean anything with two hosts: the same
 * absolute path on two machines is NOT a collision, and the same file of one
 * repository in two clones IS. Both were built and unit-tested in the fold,
 * and neither had ever been exercised through the real transport, the real
 * trust store, the real bridge and the real host id. This does that, with two
 * data directories standing in for two machines: the hub is bound to this
 * machine's non-loopback address, so nothing here arrives over loopback and
 * every caller's host is what its bridge asserted.
 *
 * Run: DIBS_ALLOW_PARALLEL=1 DIBD=bin/dibd DIBS=bin/dibs bun internal/mcp/e2e/remote_e2e.ts
 */
import { mkdtempSync, rmSync, mkdirSync, copyFileSync, chmodSync, readFileSync, readdirSync, writeFileSync, existsSync } from "node:fs"
import { networkInterfaces, tmpdir } from "node:os"
import { join } from "node:path"
import { daemonReady } from "./ready.ts"

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
const PORT = process.env.PORT ?? "4941"

// A reachable address: the first non-loopback IPv4 this machine has. Loopback
// would be stamped local by the daemon, which is the case every other suite
// already covers; this one needs the daemon to be unable to tell.
function lanAddress(): string | undefined {
  for (const list of Object.values(networkInterfaces())) {
    for (const a of list ?? []) {
      if (a.family === "IPv4" && !a.internal) return a.address
    }
  }
  return undefined
}
const IP = lanAddress()
if (!IP) {
  // A failure, not a skip: this suite is in the gate, and a gate that passes
  // while exercising nothing is the shape of every false green this project
  // has had. A machine with no address at all is not one the gate runs on.
  console.log("  \x1b[31m✗\x1b[0m this machine has no non-loopback IPv4 address, so a two-host board cannot be stood up here; nothing was checked")
  process.exit(1)
}
const ADDR = `${IP}:${PORT}`
const URL = `https://${ADDR}`

const root = mkdtempSync(join(tmpdir(), "dibs-remote-e2e-"))
const hubDir = join(root, "hub")
const clientDir = join(root, "client")
mkdirSync(hubDir); mkdirSync(clientDir)

// The address goes in dibs.toml, as SPEC §16 has it, rather than on the command
// line: the CLI on the hub reads the file to tell that machine's own agents
// where the daemon is, and a flag leaves it nothing to read.
writeFileSync(join(hubDir, "dibs.toml"), `addr = "${ADDR}"\n`)
// The daemon's log is its stderr when it is run by hand; kept, because the
// wake path below is proved by what the hub says it observed.
const daemon = Bun.spawn({ cmd: [dibd, "-dir", hubDir], stdout: "ignore", stderr: "pipe" })
let hubLog = ""
void (async () => {
  const reader = daemon.stderr.getReader()
  const dec = new TextDecoder()
  for (;;) {
    const { value, done } = await reader.read()
    if (done) return
    hubLog += dec.decode(value, { stream: true })
  }
})()
const bridges: ReturnType<typeof Bun.spawn>[] = []

// Stop every child and WAIT for it before the directories go: a signal sent is
// not a process gone, and a daemon still writing into a directory being
// removed is how a suite reports success with children left running. Bounded:
// a child that ignores SIGTERM for two seconds gets SIGKILL.
async function stop(p: ReturnType<typeof Bun.spawn>) {
  try { p.kill() } catch { return }
  const gone = await Promise.race([p.exited.then(() => true), Bun.sleep(2_000).then(() => false)])
  if (!gone) { try { p.kill("SIGKILL") } catch {} ; await p.exited }
}
let cleaned = false
async function cleanup() {
  if (cleaned) return
  cleaned = true
  for (const b of bridges) await stop(b)
  await stop(daemon)
  try { rmSync(root, { recursive: true, force: true }) } catch {}
}
async function finish(code: number): Promise<never> {
  await cleanup()
  process.exit(code)
}
for (const sig of ["SIGINT", "SIGTERM"] as const) process.on(sig, () => { void finish(130) })
process.on("uncaughtException", (err) => { console.log(`  \x1b[31m✗\x1b[0m ${err}`); void finish(1) })
process.on("unhandledRejection", (err) => { console.log(`  \x1b[31m✗\x1b[0m ${err}`); void finish(1) })

const secret = await daemonReady(hubDir, URL, { proc: daemon, label: "hub" })
const hubNode = readFileSync(join(hubDir, "node_id"), "utf8").trim()
check("the hub has a node id", hubNode.length > 0)

// ── the second machine joins, by the documented recipe ───────────────────
// `dibs mcp-config --board` prints these three steps; this performs them.
copyFileSync(join(hubDir, "local.secret"), join(clientDir, "local.secret"))
chmodSync(join(clientDir, "local.secret"), 0o600)

function run(cmd: string[], env: Record<string, string>, cwd?: string) {
  const p = Bun.spawnSync({ cmd, cwd, env: { ...process.env, ...env }, stdout: "pipe", stderr: "pipe" })
  return { code: p.exitCode, out: p.stdout.toString() + p.stderr.toString() }
}
const trust = run([dibsBin, "trust", ADDR], { DIBS_DIR: clientDir })
check("`dibs trust` on the joining machine records the hub's certificate", trust.code === 0, trust.out.trim())
const fp = run([dibsBin, "fingerprint"], { DIBS_DIR: hubDir })
const sha = (s: string) => (s.match(/[0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){31}/) ?? [""])[0].toLowerCase()
check("the fingerprint trust printed is the one the hub prints", sha(trust.out) !== "" && sha(trust.out) === sha(fp.out),
  `trust: ${sha(trust.out) || "none"}  hub: ${sha(fp.out) || "none"}`)

// The pin path: what the hub would advertise through Supgang (`dibs
// fingerprint` prints it) is what a joiner checks the served certificate
// against. The right pin records; a wrong one is refused and records nothing.
const pin = (fp.out.match(/key pin ([0-9a-f]{64})/) ?? ["", ""])[1]
check("`dibs fingerprint` on the hub prints the key pin to advertise", pin.length === 64 && fp.out.includes("supgang advertise dibs"), fp.out.trim())
const pinDir = join(clientDir, "pinned")
const pinned = run([dibsBin, "trust", ADDR, "--pin", pin], { DIBS_DIR: pinDir })
check("`dibs trust --pin` with the hub's own key pin records the certificate", pinned.code === 0 && existsSync(join(pinDir, "trusted-certs.pem")), pinned.out.trim())
const wrongDir = join(clientDir, "wrong-pin")
const wrong = run([dibsBin, "trust", ADDR, "--pin", "00".repeat(32)], { DIBS_DIR: wrongDir })
check("`dibs trust --pin` with another key refuses and records nothing", wrong.code !== 0 && !existsSync(join(wrongDir, "trusted-certs.pem")) && wrong.out.includes("not that board"), wrong.out.trim())

// ── one repository, cloned on both machines ──────────────────────────────
const git = (cwd: string, ...args: string[]) => {
  const r = run(["git", ...args], {
    GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t",
    GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1",
  }, cwd)
  if (r.code !== 0) throw new Error(`git ${args.join(" ")}: ${r.out}`)
}
const remoteRepo = join(clientDir, "repo")
const hubRepo = join(hubDir, "repo")
mkdirSync(remoteRepo)
git(remoteRepo, "init", "-q")
writeFileSync(join(remoteRepo, "probe.go"), "package probe\n")
git(remoteRepo, "add", "-A")
git(remoteRepo, "commit", "-q", "--no-gpg-sign", "-m", "probe")
git(root, "clone", "-q", remoteRepo, hubRepo)

// ── the real bridge, once per machine ────────────────────────────────────
// A bridge run from a data directory that holds a node_id asserts that id;
// one run from a directory without asserts the host_id it generates there.
// The hub machine's bridge is given only DIBS_DIR, which is what `dibs
// mcp-config` prints there: the address comes from the daemon's dibs.toml,
// and the certificate from the CA beside it, with no trust step.
class Bridge {
  proc: ReturnType<typeof Bun.spawn>
  pending = new Map<number, (m: any) => void>()
  next = 0
  constructor(dir: string, cwd: string, addr?: string, hostId?: string) {
    this.proc = Bun.spawn({
      cmd: [dibsBin, "mcp-stdio"], cwd,
      env: { ...process.env, DIBS_DIR: dir, ...(addr ? { DIBS_ADDR: addr } : {}), ...(hostId ? { DIBS_HOST_ID: hostId } : {}) },
      stdin: "pipe", stdout: "pipe", stderr: "ignore",
    })
    bridges.push(this.proc)
    void this.read()
  }
  async read() {
    const reader = this.proc.stdout.getReader()
    const dec = new TextDecoder()
    let buf = ""
    for (;;) {
      const { value, done } = await reader.read()
      if (done) return
      buf += dec.decode(value, { stream: true })
      let nl: number
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl); buf = buf.slice(nl + 1)
        if (!line.trim()) continue
        let msg: any
        try { msg = JSON.parse(line) } catch { continue }
        const w = this.pending.get(msg.id)
        if (w) { this.pending.delete(msg.id); w(msg) }
      }
    }
  }
  send(method: string, params: unknown): Promise<any> {
    const id = ++this.next
    this.proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n")
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => { this.pending.delete(id); reject(new Error(`${method}: no reply in 15s`)) }, 15_000)
      this.pending.set(id, (m) => { clearTimeout(t); resolve(m) })
    })
  }
  async init(name: string) {
    await this.send("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name, version: "e2e" } })
    this.proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n")
  }
  async call(name: string, args: Record<string, unknown>): Promise<any> {
    const m = await this.send("tools/call", { name, arguments: args })
    if (m.error) return { error: m.error }
    const text = m.result?.content?.[0]?.text ?? ""
    try { return JSON.parse(text) } catch { return { raw: text } }
  }
}

// The joining "machine" states its host id: both machines of this suite run
// on one computer, and on a Supgang member every bridge would otherwise carry
// the same identity (#118), which makes one machine of two and the whole
// cross-host path unreachable. DIBS_HOST_ID is the operator's word for this
// (NETWORK.md §2), and the host bridge below states the same one.
const CLIENT_HOST = "c".repeat(64)
const remote = new Bridge(clientDir, remoteRepo, URL, CLIENT_HOST)
await remote.init("remote-harness")
const remoteReg = await remote.call("register", { name: "remote-worker", description: "on the joining machine", pid: 0, nonce: "e2e-remote" })
check("the joining machine's bridge registers over TLS with the pinned certificate", !!remoteReg.token, JSON.stringify(remoteReg).slice(0, 200))

const hub = new Bridge(hubDir, hubRepo)
await hub.init("hub-harness")
const hubReg = await hub.call("register", { name: "hub-worker", description: "on the hub machine", pid: 0, nonce: "e2e-hub" })
check("the hub machine's bridge registers over the same address, trusting the CA its daemon made, with no trust step",
  !!hubReg.token, JSON.stringify(hubReg).slice(0, 200))

// ── check in, then read which computer each agent is on ─────────────────
for (const [b, tok] of [[remote, remoteReg.token], [hub, hubReg.token]] as const) {
  const ci = await b.call("check_in", { token: tok })
  check("check_in works over the joined transport", ci.ok === true, JSON.stringify(ci.error ?? ci.raw ?? "").slice(0, 160))
}
const full = await hub.call("check_in", { token: hubReg.token, detail: true })
const rows: Record<string, any> = {}
for (const a of full.board?.agents ?? []) rows[a.id] = a
const clientHost = CLIENT_HOST
check("the remote agent's row carries the host id its bridge asserted", rows[remoteReg.agent_id]?.agent?.host_id === clientHost,
  `row: ${rows[remoteReg.agent_id]?.agent?.host_id}  asserted: ${clientHost}  (${Object.keys(rows).length} rows${full.raw ? ", raw: " + String(full.raw).slice(0, 120) : ""})`)
// The hub's own agent carries the hub's identity: its Supgang node id on a
// member (#118), else the ledger's node id.
// Its Supgang node id (64 hex) on a member; the ledger's node id elsewhere.
const hubHost = rows[hubReg.agent_id]?.agent?.host_id ?? ""
check("the hub agent's row carries the hub's identity, which is not the joiner's",
  (hubHost === hubNode || hubHost.length === 64) && hubHost !== clientHost, `row: ${hubHost}  node: ${hubNode}`)
check("the two agents are on two different machines, as far as the board knows", clientHost !== hubHost)

// ── claims across hosts: NETWORK.md §3, through the real path ────────────
const shared = "/tmp/dibs-remote-e2e/shared.go"
const c1 = await remote.call("claim", { token: remoteReg.token, path: shared, mode: "exclusive" })
const c2 = await hub.call("claim", { token: hubReg.token, path: shared, mode: "exclusive" })
check("the same absolute path on two machines is not a collision", c1.granted === true && c2.granted === true,
  `remote: ${JSON.stringify(c1).slice(0, 160)}  hub: ${JSON.stringify(c2).slice(0, 160)}`)

const r1 = await remote.call("claim", { token: remoteReg.token, path: join(remoteRepo, "probe.go"), mode: "exclusive" })
const r2 = await hub.call("claim", { token: hubReg.token, path: join(hubRepo, "probe.go"), mode: "exclusive" })
check("the same file of one repository, in two clones on two machines, IS a collision", r1.granted === true && r2.granted === false,
  `remote: ${JSON.stringify(r1).slice(0, 160)}  hub: ${JSON.stringify(r2).slice(0, 240)}`)
const overlap = (r2.overlaps ?? [])[0] ?? {}
check("and the refusal says which rule fired: the repository, not the path", overlap.rule === "repo" && overlap.repo_path === "probe.go",
  JSON.stringify(overlap).slice(0, 240))

// ── waking an agent on the joining machine: NETWORK.md §5, both halves ───
// The hub decides THAT; the joining machine decides HOW. Its [wake.exec] is
// in ITS data directory, run by `dibs host-bridge` there, and the hub never
// sees the argv. The recorder is the same one wake_e2e uses: it appends what
// it was handed, so the assertion reads the substituted values.
const recorder = join(clientDir, "recorder.ts")
const wakeLog = join(clientDir, "wakes.jsonl")
await Bun.write(recorder, `
const line = JSON.stringify(Bun.argv.slice(3)) + "\\n"
await Bun.write(Bun.argv[2] + "." + process.pid + "." + Bun.nanoseconds(), line)
`)
writeFileSync(join(clientDir, "dibs.toml"), `
[wake.exec.codex]
argv = ["${process.execPath}", "${recorder}", "${wakeLog}", "{thread}", "{message}", "{agent}", "{from}", "{type}"]
`)
// The agent registers under the thread its harness would quote (the shape
// wake_e2e's Claude Code block uses), so a resume command has something to
// name; the hub finds it the way it finds any thread.
const THREAD = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
// Its own bridge, named as the harness it stands in for: the daemon records
// the harness from the MCP client that registered, not from a word in the
// call, and a directory of its own, where the wake will run.
const sleeperRepo = join(clientDir, "sleeper")
mkdirSync(sleeperRepo)
const sleeperBridge = new Bridge(clientDir, sleeperRepo, URL, CLIENT_HOST)
await sleeperBridge.init("Codex")
const sleeper = await sleeperBridge.call("register", {
  name: "remote-sleeper", description: "asleep on the joining machine", pid: 0, session_id: THREAD,
  cwd: sleeperRepo, nonce: "e2e-remote-sleeper",
})
check("a wakeable agent registers on the joining machine", !!sleeper.token, JSON.stringify(sleeper).slice(0, 200))

const hostBridge = Bun.spawn({
  cmd: [dibsBin, "host-bridge"], cwd: clientDir,
  env: { ...process.env, DIBS_ADDR: URL, DIBS_DIR: clientDir, DIBS_HOST_ID: CLIENT_HOST },
  stdout: "ignore", stderr: "pipe",
})
bridges.push(hostBridge)
// The hub lists what is attached; the bridge is attached when it appears.
async function attachedHosts(): Promise<any[]> {
  try {
    const r = await fetch(`${URL}/api/hosts`, { headers: { "X-Dibs-Local": secret }, tls: { rejectUnauthorized: false } } as any)
    return ((await r.json()) as any).hosts ?? []
  } catch { return [] }
}
let attached: any[] = []
for (let i = 0; i < 50 && !attached.some((h) => h.host === CLIENT_HOST); i++) { await Bun.sleep(200); attached = await attachedHosts() }
check("`dibs host-bridge` attaches for the joining machine, stating the harness its own [wake.exec] can start",
  attached.some((h) => h.host === CLIENT_HOST && (h.harnesses ?? []).includes("codex")), JSON.stringify(attached).slice(0, 300))

// A question the sleeper is blocked on: the hub decides to wake it, and the
// only route is the bridge on its machine.
await hub.call("send", { token: hubReg.token, to: "remote-sleeper", type: "question", body: "still there?", deadline_s: 600 })
function wakes(): string[][] {
  return readdirSync(clientDir).filter((f) => f.startsWith("wakes.jsonl.")).sort()
    .map((f) => JSON.parse(readFileSync(join(clientDir, f), "utf8").trim()))
}
for (let i = 0; i < 100 && wakes().length === 0; i++) await Bun.sleep(100)
const woke = wakes()
const after = await hub.call("check_in", { token: hubReg.token, detail: true })
const sleeperRow = (after.board?.agents ?? []).find((a: any) => a.id === sleeper.agent_id)
check("the joining machine ran ITS OWN wake command for its agent, handed the thread the hub found", woke.length === 1 && woke[0]?.[0] === THREAD,
  `wakes: ${JSON.stringify(woke).slice(0, 300)}  sleeper: ${JSON.stringify({ session: sleeperRow?.session_id, aliases: sleeperRow?.session_aliases, host: sleeperRow?.agent?.host_id, harness: sleeperRow?.agent?.harness, status: sleeperRow?.status })}  hub-log: ${hubLog.split("\n").filter((l) => l.includes("wake")).slice(-3).join(" | ").slice(0, 400)}`)
check("with the one fixed sentence, the agent, the sender and the mail type substituted",
  woke[0]?.[1] === "Dibs: check the board." && woke[0]?.[2] === "remote-sleeper" && woke[0]?.[3] === "hub-worker" && woke[0]?.[4] === "question",
  JSON.stringify(woke[0] ?? null))
for (let i = 0; i < 50 && !hubLog.includes("reports the wake ran"); i++) await Bun.sleep(100)
check("and the hub took the bridge's report as the wake's outcome", hubLog.includes("the agent's host reports the wake ran"),
  hubLog.split("\n").filter((l) => l.includes("wake")).slice(-4).join(" | ").slice(0, 600))

// ── doctor on both machines knows which half is where ────────────────────
const hubDoc = run([dibsBin, "doctor"], { DIBS_DIR: hubDir }, hubDir)
check("doctor on the hub lists the attached bridge and what it can start", hubDoc.out.includes("host bridge(s) attached") && hubDoc.out.includes("(codex)"),
  hubDoc.out.split("\n").find((l) => l.includes("host bridge")) ?? "no such line")
// A scratch HOME: this check is about the joined directory, and doctor also
// reads the operator's own harness configs under HOME (Codex, Claude Desktop,
// the Claude Code plugin record). On the machine this is developed on those
// can legitimately be in a state doctor reports, which is doctor working and
// not the join failing.
const scratchHome = mkdtempSync(join(tmpdir(), "dibs-remote-e2e-home-"))
const doc = run([dibsBin, "doctor"], { DIBS_ADDR: URL, DIBS_DIR: clientDir, DIBS_HOST_ID: CLIENT_HOST, HOME: scratchHome }, clientDir)
check("doctor on the joining machine reports no problems", doc.code === 0, doc.out.split("\n").filter((l) => l.includes("✗")).join(" | ").slice(0, 300))
check("and says the board is served by the hub's daemon, and this machine's wake command reaches its agents through the bridge",
  doc.out.includes(`served by another daemon (node ${hubNode})`) && doc.out.includes("through the host bridge attached for it"),
  doc.out.split("\n").find((l) => l.includes("another daemon")) ?? "no such line")
check("and that the ledger is not here", doc.out.includes("joined board: the ledger lives on the daemon serving it"))

console.log(`\n${checks - failures}/${checks} checks passed`)
await finish(failures === 0 ? 0 : 1)
