/**
 * Dibs extension for pi.
 *
 * pi has no MCP client, so unlike every other harness Dibs cannot ride in on a
 * server config. It has something better: `pi.registerTool()`, plus a
 * `before_agent_start` hook that can inject a message into the turn. That is
 * exactly the two halves Dibs needs: a tool surface, and a place to deliver
 * mail, so this extension provides both by talking to the daemon directly.
 *
 * The tool surface is NOT hand-copied. It is fetched from the running daemon
 * with tools/list and registered verbatim, so `dibs` and this file can never
 * drift: add a tool to the server and pi gets it on next start. A hand-written
 * mirror of 25 tools would be wrong within a week.
 *
 * Install: copy to ~/.pi/agent/extensions/dibs.ts (global) or
 * .pi/extensions/dibs.ts (project-local). Both are auto-discovered and can be
 * hot-reloaded with /reload.
 *
 * Env: DIBS_ADDR (default 127.0.0.1:4777; a full https:// origin for a joined board),
 *      DIBS_DIR (default ~/.dibs), DIBS_BIN (the dibs binary, default: on PATH; see machineIdentity)
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent"
import { Type } from "typebox"
import { execFile } from "node:child_process"
import { existsSync, realpathSync } from "node:fs"
import { readFile } from "node:fs/promises"
import { homedir, hostname } from "node:os"
import { dirname, isAbsolute, join, resolve } from "node:path"
import { promisify } from "node:util"

const run = promisify(execFile)

/**
 * The daemon's origin. DIBS_ADDR is what `dibs mcp-config` writes for the
 * bridge, and for a hub on another machine that is a full HTTPS origin
 * (`https://hub:4777`); this prefixed `http://` to whatever it found, so a
 * joined board became `http://https://hub:4777/mcp` and every hook failed
 * silently while the bridge beside it connected fine. A scheme given is
 * kept; a bare host:port is the loopback daemon's plaintext. Round nineteen
 * of the pre-release review.
 */
const ADDR = process.env["DIBS_ADDR"] ?? "127.0.0.1:4777"
const ORIGIN = /^https?:\/\//i.test(ADDR) ? ADDR.replace(/\/+$/, "") : `http://${ADDR}`
/**
 * Where the daemon keeps its local secret, resolved the way the daemon
 * resolves it: `~/.dibs`, falling back to a legacy `~/.agents` only when that
 * is the directory that actually exists.
 *
 * This used to default to `~/.agents` alone, and that name was never one Dibs
 * chose: the 0.0.3 rename swept `~/.lanes` up with every other "lane", and the
 * daemon then moved to `~/.dibs` and kept reading the old name for anyone who
 * had one. The plugins never moved. So on every install made since, the secret
 * was read from a directory that does not exist, `secret()` swallowed the
 * failure and returned null, and every hook here returns null on a null key.
 * The agent registered no delivery hook and nothing said so: mail simply never
 * arrived, which is the silent failure this whole plugin exists to prevent.
 */
function dataDir(): string {
  const current = `${homedir()}/.dibs`
  if (existsSync(current)) return current
  const legacy = `${homedir()}/.agents`
  return existsSync(legacy) ? legacy : current
}

const DIR = process.env["DIBS_DIR"] ?? dataDir()

/** Tool names are prefixed so they never collide with pi's built-ins. */
const PREFIX = "dibs_"

/**
 * Read once and remember. The daemon rewrites the secret only when the data
 * directory is recreated, which cannot happen mid-session.
 *
 * `undefined` means "not looked yet"; `null` means "looked, and there is no
 * daemon here": a distinction that matters because the miss must not be
 * retried on every single turn.
 */
let secretCache: string | null | undefined

async function secret(): Promise<string | null> {
  if (secretCache !== undefined) return secretCache
  try {
    secretCache = (await readFile(`${DIR}/local.secret`, "utf8")).trim() || null
  } catch {
    secretCache = null // daemon never started here: stay quiet
  }
  return secretCache
}

/**
 * What a board's certificate is checked against, beyond the runtime's own
 * roots: the certificates `dibs trust` recorded beside the secret, and the
 * CA the daemon in that directory signs with (`tls-ca.pem`), which is
 * trusted without a `dibs trust` step because the machine that generated
 * it is the one authority there is on it. The same two files the bridge
 * dials with (cmd/dibs/trust.go), and reading only the first left a hub's
 * own plugin refusing the daemon its bridge accepted. The runtime's roots
 * are kept so a board fronted by a real certificate still works. Empty for
 * a plaintext daemon or an unjoined directory. Rounds nineteen and twenty
 * of the pre-release review.
 */
let trustCache: string[] | null | undefined

async function trust(): Promise<string[] | null> {
  if (trustCache !== undefined) return trustCache
  if (!ORIGIN.startsWith("https://")) return (trustCache = null)
  const extra: string[] = []
  for (const name of ["trusted-certs.pem", "tls-ca.pem"]) {
    try {
      const pem = (await readFile(`${DIR}/${name}`, "utf8")).trim()
      if (pem) extra.push(pem)
    } catch {
      // not recorded here
    }
  }
  if (extra.length === 0) return (trustCache = null)
  let roots: string[] = []
  try {
    roots = [...((await import("node:tls")).rootCertificates ?? [])]
  } catch {
    // a runtime without them: the recorded certificates alone, as before
  }
  return (trustCache = [...roots, ...extra])
}

/**
 * What this machine and checkout are, as the stdio bridge would stamp them.
 *
 * Asked of `dibs identity` (the same binary the bridge is), once per
 * process, rather than re-derived here. This extension is pi's whole MCP
 * client, so it has to say what the bridge says, and it had been growing a
 * TypeScript copy of the bridge's answers one review round at a time: the
 * host, then canonical paths, then the trust store, and still not the
 * repository identity, without which a remote pi agent registered with no
 * checkout and two clones of one repository on two machines were both
 * granted an exclusive claim on the same tracked file. The Go answers
 * change together; a copy drifts. Round twenty-two of the pre-release
 * review.
 *
 * `dibs` is found on PATH or named by DIBS_BIN. Without it (a machine with
 * only this extension), the host is read from the data directory the way
 * the opencode plugin reads it, and no repository identity is sent, which
 * is what this extension sent before. An empty answer is not cached: a
 * bridge may publish the host a moment later.
 */
type Identity = { host_id: string; cwd: string; repo: Record<string, string> | null }
const identityCache = new Map<string, Identity>()

async function machineIdentity(): Promise<Identity> {
  return identityFor(process.cwd())
}

/**
 * The identity of one directory: this process's for most calls, and the
 * directory an `update` names when the agent moves. The repository was
 * cached once, for the process's own directory, and stamped on every
 * update: an agent moving from /work/a to /work/b recorded b's directory
 * beside a's repository, so its claims under b had no repository-relative
 * path and collided with nothing. Round twenty-three of the pre-release
 * review. Cached per directory, and only when a host was established.
 */
async function identityFor(cwd: string): Promise<Identity> {
  const cached = identityCache.get(cwd)
  if (cached) return cached
  const bin = process.env["DIBS_BIN"] ?? "dibs"
  try {
    const { stdout } = await run(bin, ["identity", "--cwd", cwd], { timeout: 10_000 })
    const id = JSON.parse(stdout) as Identity
    if (id && typeof id.host_id === "string" && typeof id.cwd === "string") {
      const stated = process.env["DIBS_HOST_ID"]?.trim()
      if (stated) id.host_id = stated
      if (id.host_id) identityCache.set(cwd, id)
      return id
    }
  } catch {
    // no dibs here, or one too old to answer: the files below
  }
  const fallback: Identity = { host_id: process.env["DIBS_HOST_ID"]?.trim() ?? "", cwd, repo: null }
  if (!fallback.host_id) {
    for (const name of ["resolved_host_id", "node_id", "host_id"]) {
      try {
        const v = (await readFile(`${DIR}/${name}`, "utf8")).trim()
        if (v) {
          fallback.host_id = v
          break
        }
      } catch {
        // not this file; the next one, or none
      }
    }
  }
  if (fallback.host_id) identityCache.set(cwd, fallback)
  return fallback
}

async function host(): Promise<string> {
  return (await machineIdentity()).host_id
}

/** The bridge's pathArgs: per tool, the arguments that are paths here. */
const PATH_ARGS: Record<string, string[]> = {
  claim: ["path"],
  release: ["path"],
  force_release: ["path"],
  guard_path: ["path", "cwd"],
  hook_poll: ["cwd"],
  hook_session: ["cwd"],
  hook_blocked: ["cwd"],
}

/**
 * A path as the daemon compares it: absolute, every symlink resolved, the
 * deepest existing ancestor resolved and the rest re-attached for a file
 * that does not exist yet. Mirrors internal/paths.Canonical, as the
 * opencode plugin's does.
 */
function canonical(p: string): string {
  if (!p) return p
  let cur = isAbsolute(p) ? resolve(p) : resolve(process.cwd(), p)
  let rest = ""
  for (;;) {
    try {
      return rest ? join(realpathSync(cur), rest) : realpathSync(cur)
    } catch {
      const parent = dirname(cur)
      if (parent === cur) return resolve(p)
      rest = rest ? join(cur.slice(parent.length + 1), rest) : cur.slice(parent.length + 1)
      cur = parent
    }
  }
}

let rpcId = 0

/**
 * One MCP call over the daemon's streamable-HTTP endpoint.
 *
 * `timeoutMs` is a parameter rather than a constant because the two callers
 * have opposite risk profiles: a tool call is work the agent asked for and may
 * legitimately block, while the mail poll sits in front of the user's turn and
 * must never be what makes it feel slow.
 */
async function rpc(
  method: string,
  params: unknown,
  timeoutMs: number,
): Promise<any | null> {
  const key = await secret()
  if (!key) return null
  if (method === "tools/call" && params && typeof params === "object") {
    const p = params as Record<string, unknown>
    const id = await machineIdentity()
    const meta: Record<string, unknown> = { ...((p["_meta"] as Record<string, unknown> | undefined) ?? {}) }
    if (id.host_id) meta["com.dibs/host"] = id.host_id
    // WHICH CHECKOUT, as this machine sees it: a hub on another computer
    // cannot ask Git about a path that exists only here, and the repository
    // rule for two clones on two machines needs the answer. The bridge
    // sends it on every call; this reads it on the calls that record it,
    // for the directory THAT call names when it names one (an update that
    // moves the agent), spelled as the bridge would spell it.
    if (p["name"] === "register" || p["name"] === "update") {
      const args = (p["arguments"] ?? {}) as Record<string, unknown>
      const named = typeof args["cwd"] === "string" && args["cwd"] !== "" ? (args["cwd"] as string) : ""
      const at = named ? await identityFor(named) : id
      if (named) args["cwd"] = at.cwd
      if (at.repo) meta["com.dibs/repo"] = at.repo
      p["arguments"] = args
    }
    // AND EVERY OTHER PATH, as the bridge spells them (its pathArgs table):
    // a hub does not resolve a remote caller's paths, so a claim on
    // `/tmp/repo/pkg/x.go` from an agent registered at `/private/tmp/repo`
    // had no repository-relative key and collided with nothing. Round
    // twenty-four of the pre-release review.
    const pathArgs = PATH_ARGS[p["name"] as string]
    if (pathArgs) {
      const args = (p["arguments"] ?? {}) as Record<string, unknown>
      for (const k of pathArgs) {
        if (typeof args[k] === "string" && args[k] !== "") args[k] = canonical(args[k] as string)
      }
      p["arguments"] = args
    }
    if (Object.keys(meta).length > 0) p["_meta"] = meta
  }
  const text = await post(
    `${ORIGIN}/mcp`,
    JSON.stringify({ jsonrpc: "2.0", id: ++rpcId, method, params }),
    { "content-type": "application/json", "X-Dibs-Local": key },
    timeoutMs,
    await trust(),
  )
  if (text === null) return null
  const body = JSON.parse(text) as { result?: unknown; error?: unknown }
  if (body.error) return { __error: body.error }
  return body.result ?? null
}

/**
 * One POST, through node:http or node:https rather than fetch, and the
 * difference is the whole TLS story: pi runs under Node, whose fetch is
 * undici and ignores a `tls` option, so a joined board's self-issued
 * certificate stayed untrusted however carefully `dibs trust` had recorded
 * it, and startup installed no tools. Node's request API takes `ca`; so
 * does Bun's. Returns the body on a 2xx and null on anything else, which
 * every caller reads as "stay quiet". Round twenty of the pre-release
 * review.
 */
async function post(
  url: string,
  body: string,
  headers: Record<string, string>,
  timeoutMs: number,
  ca: string[] | null,
): Promise<string | null> {
  const u = new URL(url)
  const mod = u.protocol === "https:" ? await import("node:https") : await import("node:http")
  return new Promise((resolve) => {
    // ELAPSED TIME, not inactivity. req.setTimeout measures the socket's
    // idle time, so a response that kept trickling bytes never timed out,
    // and before_agent_start awaits this in front of the user's turn: a
    // stalled daemon could hold the turn open indefinitely. Round
    // twenty-four of the pre-release review.
    let done = false
    const finish = (v: string | null) => {
      if (done) return
      done = true
      clearTimeout(deadline)
      resolve(v)
    }
    const req = mod.request(
      u,
      { method: "POST", headers: { ...headers, "content-length": String(Buffer.byteLength(body)) }, ...(ca ? { ca } : {}) },
      (res) => {
        const chunks: Buffer[] = []
        res.on("data", (c: Buffer) => chunks.push(c))
        res.on("end", () => {
          const ok = (res.statusCode ?? 0) >= 200 && (res.statusCode ?? 0) < 300
          finish(ok ? Buffer.concat(chunks).toString("utf8") : null)
        })
        res.on("error", () => finish(null))
      },
    )
    const deadline = setTimeout(() => {
      // Settled BEFORE the destroy: under bun, destroying the request emits
      // the response's end synchronously, with whatever had trickled in as
      // if it were the whole body.
      finish(null)
      req.destroy(new Error("timeout"))
    }, timeoutMs)
    req.on("error", () => finish(null))
    req.end(body)
  })
}

type McpTool = {
  name: string
  description?: string
  inputSchema?: Record<string, unknown>
}

async function listTools(): Promise<McpTool[]> {
  // The daemon accepts tools/list without a prior initialize on this transport,
  // so there is no handshake to keep alive between turns.
  const r = await rpc("tools/list", {}, 4000)
  const tools = r?.tools
  return Array.isArray(tools) ? (tools as McpTool[]) : []
}

/**
 * Ask Dibs what this session has waiting. Read-only: hook_poll never consumes
 * mail, so a dropped response loses nothing and the poll is safe to repeat.
 */
async function pollMail(sessionID: string): Promise<string | null> {
  const r = await rpc(
    "tools/call",
    { name: "hook_poll", arguments: { session_id: sessionID, event: "before_agent_start", cwd: (await machineIdentity()).cwd } },
    1500,
  )
  const text = r?.content?.[0]?.text
  if (!text) return null
  try {
    const payload = JSON.parse(text) as {
      hookSpecificOutput?: { additionalContext?: string }
    }
    return payload.hookSpecificOutput?.additionalContext ?? null
  } catch {
    return null
  }
}

/**
 * What this agent is, observed rather than asked for.
 *
 * Every other harness gets this from the `dibs mcp-stdio` bridge, which fills
 * in blank identity fields on the way past. pi talks to the daemon directly, so
 * it has no bridge, and the first real pi run registered an agent with a
 * completely empty `agent`, while the opencode agents beside it on the same board
 * carried harness, host, cwd and branch.
 *
 * The rule is the same one SPEC §5.0 states: identity is observed, never
 * self-reported. Models leave these fields blank when asked, every time.
 */
async function gitBranch(cwd: string): Promise<string> {
  try {
    const { stdout } = await run("git", ["-C", cwd, "symbolic-ref", "--short", "-q", "HEAD"])
    const br = stdout.trim()
    if (br) return br
  } catch {
    /* not a repo, or an unborn branch: fall through to the sha */
  }
  try {
    const { stdout } = await run("git", ["-C", cwd, "rev-parse", "--short", "HEAD"])
    const sha = stdout.trim()
    if (sha) return "detached@" + sha
  } catch {
    /* not a repo at all */
  }
  return ""
}

/**
 * pi's own session id, via the documented accessor.
 *
 * There is no `ctx.sessionId`: the field this originally reached for. Guessing
 * it cost a whole wake test: registration silently fell back to a per-process
 * id, the poll asked about a different session, and the injected mail never
 * appeared. `ctx.sessionManager.getSessionId()` is the real API.
 */
/**
 * The agent this session belongs to, or null.
 *
 * hook_poll names the agent whether or not it has news, precisely so a caller
 * that wants the RELATIONSHIP rather than the mail can ask for it.
 */
async function spaceOf(sessionID: string): Promise<string | null> {
  const r = await rpc(
    "tools/call",
    { name: "hook_poll", arguments: { session_id: sessionID, event: "tool_call", cwd: (await machineIdentity()).cwd } },
    1500,
  )
  const text = r?.content?.[0]?.text
  if (!text) return null
  try {
    const id = (JSON.parse(text) as { agent?: string }).agent
    return id && id.length > 0 ? id : null
  } catch {
    return null
  }
}

/**
 * Whether a command launches an agent, and whether a leading assignment is
 * safe to put in front of it.
 *
 * Mirrors cmd/dibs/hookspawn.go, which is the same judgement for Claude Code
 * and Codex. pi's bash tool takes `command` and `timeout` and no environment,
 * so, unlike opencode, which hands over the env map, the stamp has to ride on
 * the command string, and every shape a prefix would change has to be refused:
 * a subshell, a leading redirect, an expansion deciding the program, an
 * assignment already leading, or a multi-line script where the prefix binds to
 * line one only. Only the FIRST command counts, since `cd /x && codex exec …`
 * would bind the assignment to `cd`.
 *
 * A refusal costs a weaker attribution. A mangled command costs the agent's
 * work, which is not a trade worth making.
 */
function stampable(cmd: string): boolean {
  const t = cmd.trim()
  if (t === "" || /[\n\r]/.test(t)) return false
  if (/^[({<>#!$`"']/.test(t)) return false
  const head = t.split(/\s/)[0] ?? ""
  if (head.includes("=")) return false
  let first = t
  for (const sep of ["&&", "||", ";", "|"]) {
    const i = first.indexOf(sep)
    if (i >= 0) first = first.slice(0, i)
  }
  const exe = (first.trim().split(/\s+/)[0] ?? "").split("/").pop() ?? ""
  const args = first.trim().split(/\s+/)
  if (exe === "codex") return args[1] === "exec" || args[1] === "app-server"
  return exe === "claude" || exe === "opencode" || exe === "pi"
}

function sessionIdOf(ctx: any): string {
  const id = ctx?.sessionManager?.getSessionId?.()
  return typeof id === "string" && id !== "" ? id : sessionIdFallback()
}

/**
 * Stable per-process session id, for the case where pi hands the extension no
 * session id of its own (`--no-session`).
 *
 * PID alone would be wrong: PIDs are recycled, and a recycled one would
 * reattach a fresh session onto a dead agent and its mail. Mirrors the
 * bridge's `bridge-<pid>-<random>`.
 */
let fallbackSession: string | undefined

function sessionIdFallback(): string {
  if (!fallbackSession) {
    const rand = Math.floor(Math.random() * 0xffffffff).toString(16).padStart(8, "0")
    fallbackSession = `pi-${process.pid}-${rand}`
  }
  return fallbackSession
}

/** Read a `--flag value` / `--flag=value` pair out of pi's own argv. */
function argvFlag(flag: string): string {
  const argv = process.argv
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === flag && i + 1 < argv.length) return argv[i + 1]!
    if (argv[i]!.startsWith(flag + "=")) return argv[i]!.slice(flag.length + 1)
  }
  return ""
}

let registerCache: Record<string, string> | undefined

/**
 * The identity fields that travel as tool ARGUMENTS.
 *
 * `harness` and `version` are deliberately NOT here: the server takes those
 * only from the handshake's clientInfo, precisely because the client states
 * them and the model cannot. Putting them in the arguments silently does
 * nothing: the first pi agent came back with no harness at all for exactly
 * that reason.
 */
async function identity(): Promise<Record<string, string>> {
  if (registerCache) return registerCache
  const cwd = (await machineIdentity()).cwd
  const out: Record<string, string> = {
    host: hostname().replace(/\.local$/, ""),
    surface: "cli",
    cwd,
  }
  const br = await gitBranch(cwd)
  if (br) out["branch"] = br
  // pi is the one harness that genuinely knows its own model, because the user
  // names it on the command line. That makes it observable, not self-reported.
  const model = argvFlag("--model")
  if (model) out["model"] = model
  const provider = argvFlag("--provider")
  if (provider) out["provider"] = provider
  registerCache = out
  return out
}

/** clientInfo for the handshake half of identity: harness name and version. */
let clientInfoCache: { name: string; title: string; version: string } | undefined

async function clientInfo() {
  if (clientInfoCache) return clientInfoCache
  let version = ""
  try {
    const { stdout } = await run("pi", ["--version"])
    version = stdout.trim().split(/\s+/).pop() ?? ""
  } catch {
    /* version is a nicety; never let it stop registration */
  }
  clientInfoCache = { name: "pi", title: "pi", version }
  return clientInfoCache
}

export default function (pi: ExtensionAPI) {
  let registered = false

  async function registerDibsTools(notify?: (m: string, level: string) => void) {
    if (registered) return
    const tools = await listTools()
    if (tools.length === 0) {
      // No daemon, or it is not answering. Register nothing: a tool that always
      // fails is worse than an absent one, because the model will keep trying it.
      return
    }
    for (const t of tools) {
      pi.registerTool({
        name: PREFIX + t.name,
        label: t.name,
        description: t.description ?? `Dibs ${t.name}`,
        // The server's JSON Schema is passed through untouched. Type.Unsafe is
        // the supported bridge for a schema pi did not author: rebuilding these
        // as typebox literals would be a second source of truth for argument
        // shapes that the server already validates.
        parameters: Type.Unsafe<Record<string, unknown>>(
          t.inputSchema ?? { type: "object", properties: {} },
        ),
        async execute(_toolCallId: string, params: unknown, _signal: unknown, _onUpdate: unknown, ctx: any) {
          let args = (params ?? {}) as Record<string, unknown>
          const callParams: Record<string, unknown> = { name: t.name }
          if (t.name === "register") {
            // An OBSERVED value overrides whatever the model typed. This is not
            // the bridge's "the agent knows better" rule, and deliberately so:
            // the first pi run reported `model: "gpt-4"` while actually running
            // gpt-oss-120b. A field we can measure is never improved by asking.
            const merged: Record<string, unknown> = { ...args, ...(await identity()) }
            // Without a session_id, re-registering after a context loss forks a
            // sibling agent and the original's mail becomes unreachable.
            if (typeof merged["session_id"] !== "string" || merged["session_id"] === "") {
              merged["session_id"] = sessionIdOf(ctx)
            }
            args = merged
            // harness/version reach the server ONLY through clientInfo.
            callParams["clientInfo"] = await clientInfo()
          }
          callParams["arguments"] = args
          const r = await rpc("tools/call", callParams, 30_000)
          if (r === null) {
            throw new Error(
              "Dibs daemon is not reachable at " + ADDR + ": is dibd running?",
            )
          }
          if (r.__error) {
            // Throwing is how pi marks a tool result as failed; returning the
            // error text would read to the model as a successful call.
            throw new Error(JSON.stringify(r.__error))
          }
          const text = r.content?.[0]?.text ?? JSON.stringify(r)
          return { content: [{ type: "text", text }], details: r }
        },
      })
    }
    registered = true
    notify?.(`Dibs: ${tools.length} coordination tools available`, "info")
  }

  pi.on("session_start", async (_event, ctx) => {
    try {
      await registerDibsTools((m, l) => ctx.ui?.notify?.(m, l as any))
    } catch {
      // A coordination board that is down must never stop pi from starting.
    }
  })

  /**
   * Deliver mail at the top of the turn.
   *
   * This is the pi equivalent of the opencode `chat.message` hook: Dibs stays a
   * service the agent pulls from, and the extension only decides *when* to pull.
   * No subprocess, no polling loop, no driving of the harness. See PHILOSOPHY.md.
   */
  /**
   * Stamp a spawned subagent with the agent that spawned it.
   *
   * pi's own type says it plainly: "To modify arguments, mutate `event.input`
   * in place instead." So the command is rewritten here, the same way the
   * Claude Code and Codex hooks rewrite theirs: pi's bash tool has no
   * environment argument, so there is nothing cleaner available. opencode is
   * the only harness that offers one.
   *
   * The variable is inherited at fork and survives reparenting and
   * daemonisation, so it reaches every descendant however deep, which is the
   * point, since a detached child's PPID is 1 and ancestry tells you nothing.
   *
   * Never throws and never blocks: this runs in front of every bash call the
   * agent makes, and a coordination extension that can break a command is
   * worse than no extension.
   */
  pi.on("tool_call", async (event: any, ctx: any) => {
    try {
      if (event?.toolName !== "bash") return
      const cmd = event?.input?.command
      if (typeof cmd !== "string" || cmd.includes("DIBS_PARENT=")) return
      if (!stampable(cmd)) return
      const agent = await spaceOf(sessionIdOf(ctx))
      if (agent) event.input.command = `DIBS_PARENT=${agent} ${cmd}`
    } catch {
      // Leave the command exactly as the agent wrote it.
    }
  })

  pi.on("before_agent_start", async (_event, ctx) => {
    // Tools may not be registered yet if the daemon started after pi did.
    try {
      await registerDibsTools()
    } catch {
      /* keep going: mail delivery does not depend on the tool surface */
    }
    const sessionID = sessionIdOf(ctx)
    let context: string | null = null
    try {
      context = await pollMail(sessionID)
    } catch {
      return // daemon down, timed out, malformed: never break the turn
    }
    if (!context) return // no mail: inject nothing at all
    return {
      message: {
        customType: "agents-mail",
        content: context,
        display: true,
      },
    }
  })
}
