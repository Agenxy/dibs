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
import { mkdtempSync, rmSync, mkdirSync, copyFileSync, chmodSync, readFileSync, writeFileSync, existsSync } from "node:fs"
import { homedir, networkInterfaces, tmpdir } from "node:os"
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
const daemon = Bun.spawn({ cmd: [dibd, "-dir", hubDir], stdout: "ignore", stderr: "ignore" })
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
  constructor(dir: string, cwd: string, addr?: string) {
    this.proc = Bun.spawn({
      cmd: [dibsBin, "mcp-stdio"], cwd,
      env: { ...process.env, DIBS_DIR: dir, ...(addr ? { DIBS_ADDR: addr } : {}) },
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

const remote = new Bridge(clientDir, remoteRepo, URL)
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
// On a computer that is a Supgang member, every bridge and the daemon carry
// that computer's Supgang node id (#118) and the generated host_id file is
// never written; elsewhere the file is the identity. Both machines of this
// suite are this one computer, so both then assert the same id.
function supgangNodeId(): string {
  for (const bin of ["supgang", join(homedir(), ".local/bin/supgang"), "/usr/local/bin/supgang", "/opt/homebrew/bin/supgang"]) {
    try {
      const r = Bun.spawnSync({ cmd: [bin, "--json", "status"], stdout: "pipe", stderr: "pipe" })
      if (r.exitCode !== 0) continue
      const id = JSON.parse(r.stdout.toString()).node_id
      if (typeof id === "string" && id.length === 64) return id
    } catch { /* not this one */ }
  }
  return ""
}
const supgangId = supgangNodeId()
const clientHost = existsSync(join(clientDir, "host_id")) ? readFileSync(join(clientDir, "host_id"), "utf8").trim() : supgangId
check("the joining bridge asserted a host id (its own file, or this computer's Supgang identity)", clientHost.length > 0,
  `file: ${existsSync(join(clientDir, "host_id"))}  supgang: ${supgangId || "none"}`)
check("the joining bridge generated a host id beside the secret it was given", clientHost.length > 0)
check("the remote agent's row carries the host id its bridge asserted", rows[remoteReg.agent_id]?.agent?.host_id === clientHost,
  `row: ${rows[remoteReg.agent_id]?.agent?.host_id}  file: ${clientHost}  (${Object.keys(rows).length} rows${full.raw ? ", raw: " + String(full.raw).slice(0, 120) : ""})`)
const hubHost = supgangId || hubNode
check("the hub agent's row carries the hub's identity (its Supgang node id, else its ledger node id)", rows[hubReg.agent_id]?.agent?.host_id === hubHost,
  `row: ${rows[hubReg.agent_id]?.agent?.host_id}  node: ${hubNode}`)
// Two data directories on one computer: without Supgang each bridge invents
// a host id and the board sees two machines; on a Supgang member both carry
// the computer's one identity and the board, correctly, sees one.
check(supgangId ? "on a Supgang member both agents carry the one identity this computer has" : "the two agents are on two different machines, as far as the board knows",
  supgangId ? clientHost === hubHost : clientHost !== hubNode, `client: ${clientHost}  hub: ${hubHost}`)

// ── claims across hosts: NETWORK.md §3, through the real path ────────────
const shared = "/tmp/dibs-remote-e2e/shared.go"
const c1 = await remote.call("claim", { token: remoteReg.token, path: shared, mode: "exclusive" })
const c2 = await hub.call("claim", { token: hubReg.token, path: shared, mode: "exclusive" })
// On a Supgang member both bridges are, truthfully, one machine: the same
// absolute path then IS the same file, and the board says so.
check(supgangId ? "the same absolute path on one machine is a collision, whichever data directory claims it"
  : "the same absolute path on two machines is not a collision",
  c1.granted === true && c2.granted === !supgangId,
  `remote: ${JSON.stringify(c1).slice(0, 160)}  hub: ${JSON.stringify(c2).slice(0, 160)}`)

const r1 = await remote.call("claim", { token: remoteReg.token, path: join(remoteRepo, "probe.go"), mode: "exclusive" })
const r2 = await hub.call("claim", { token: hubReg.token, path: join(hubRepo, "probe.go"), mode: "exclusive" })
check("the same file of one repository, in two clones on two machines, IS a collision", r1.granted === true && r2.granted === false,
  `remote: ${JSON.stringify(r1).slice(0, 160)}  hub: ${JSON.stringify(r2).slice(0, 240)}`)
const overlap = (r2.overlaps ?? [])[0] ?? {}
check("and the refusal says which rule fired: the repository, not the path", overlap.rule === "repo" && overlap.repo_path === "probe.go",
  JSON.stringify(overlap).slice(0, 240))

// ── doctor on the joining machine knows whose board this is ──────────────
const doc = run([dibsBin, "doctor"], { DIBS_ADDR: URL, DIBS_DIR: clientDir }, clientDir)
check("doctor on the joining machine reports no problems", doc.code === 0, doc.out.split("\n").filter((l) => l.includes("✗")).join(" | ").slice(0, 300))
check("and says the board is served by the hub's daemon, whose wake configuration lives there",
  doc.out.includes(`served by another daemon (node ${hubNode})`), doc.out.split("\n").find((l) => l.includes("another daemon")) ?? "no such line")
check("and that the ledger is not here", doc.out.includes("joined board: the ledger lives on the daemon serving it"))

console.log(`\n${checks - failures}/${checks} checks passed`)
await finish(failures === 0 ? 0 : 1)
