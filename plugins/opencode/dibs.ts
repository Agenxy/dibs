/**
 * Dibs plugin for opencode.
 *
 * Delivers Dibs mail into the session at a natural boundary: the moment a new
 * user message is assembled: by appending a synthetic text part.
 *
 * This is an in-process `fetch` from opencode's own plugin runtime. No
 * subprocess, no CLI, no polling loop. Dibs stays a service the agent pulls
 * from; this plugin only decides *when* to pull, using a hook opencode already
 * fires. See ../../PHILOSOPHY.md.
 *
 * Install: copy to ~/.config/opencode/plugin/dibs.ts (global) or
 * .opencode/plugin/dibs.ts (project-local). opencode scans
 * {plugin,plugins}/*.{ts,js}.
 *
 * Env: DIBS_ADDR (default 127.0.0.1:4777; a full https:// origin for a joined board), DIBS_DIR (default ~/.dibs),
 *      DIBS_HOST_ID (which machine this is; see host() below)
 */
import { existsSync, realpathSync } from "node:fs"
import { readFile } from "node:fs/promises"
import { dirname, isAbsolute, join, resolve } from "node:path"

import type { Plugin } from "@opencode-ai/plugin"

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
  const home = process.env["HOME"] ?? "."
  const current = `${home}/.dibs`
  if (existsSync(current)) return current
  const legacy = `${home}/.agents`
  return existsSync(legacy) ? legacy : current
}

const DIR = process.env["DIBS_DIR"] ?? dataDir()

/**
 * The name Dibs knows this session by.
 *
 * NOT opencode's `input.sessionID`. That id is real, but the agent was not
 * registered under it: registration goes through the `dibs mcp-stdio` bridge,
 * which opencode spawns as a subprocess and hands no session identifier at all.
 * So the bridge names the session after the process that spawned it, and that
 * process is opencode itself, this one. `process.pid` here is the bridge's
 * `os.Getppid()` there; both sides observe the same number without ever talking.
 *
 * Sending opencode's own id instead is what made the guard useless in practice:
 * the daemon could not match it to an agent, fell open the way it is designed to,
 * and the agent overwrote a file another agent held exclusively.
 *
 * Note this is deliberately per-PROCESS, not per-conversation. `opencode run`
 * is one process for one session; the TUI can hold several conversations in one
 * process, and they share a bridge, an agent and therefore an id. That is the
 * bridge's model of a session, and the two halves agreeing matters more than
 * either half being subtler than the other.
 */
const SESSION = `host-${process.pid}`

/** Read once and remember; the daemon rewrites it only on data-dir recreation. */
let secretCache: string | null | undefined

async function secret(): Promise<string | null> {
  if (secretCache !== undefined) return secretCache
  try {
    const f = Bun.file(`${DIR}/local.secret`)
    secretCache = (await f.text()).trim() || null
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

/** fetch options that carry the trust store when there is one. */
async function tlsOptions(): Promise<Record<string, unknown>> {
  const ca = await trust()
  return ca ? { tls: { ca } } : {}
}

/**
 * Which machine this session is on, said the way the stdio bridge says it.
 * The bridge resolves it (DIBS_HOST_ID, Supgang, the daemon's node id, the
 * id it minted itself) and publishes the answer as `resolved_host_id`
 * beside the secret, because this plugin runs no subprocess and cannot ask
 * Supgang: reading node_id here answered differently from the bridge on a
 * Supgang member, and the daemon's host-scoped guard then resolved the
 * plugin's call and the bridge's registration to two machines. The
 * operator's DIBS_HOST_ID still wins, and node_id then host_id stand in for
 * a bridge that has not published yet. Read, never minted: an unknown host
 * makes the daemon compare paths as it did before hosts existed, which is
 * safe, and a second id minted here would make one machine look like two.
 *
 * Sent on every call, because the daemon scopes hook and guard lookups by
 * it. A call without one that arrives on loopback is stamped as the
 * daemon's own machine, so through the documented `ssh -L` forward this
 * plugin's guard resolved to nobody and the edit went ahead past an
 * exclusive claim held on the hub. Round seventeen of the pre-release
 * review.
 */
let hostCache: string | undefined

async function host(): Promise<string> {
  if (hostCache !== undefined) return hostCache
  const stated = process.env["DIBS_HOST_ID"]?.trim()
  if (stated) return (hostCache = stated)
  // Only the bridge's published answer is kept. A hook can run before the
  // bridge has published it, and the first version cached whatever it
  // found then, "" or the daemon's file, for the life of the process: it
  // never read the bridge's eventual answer, and through an ssh forward an
  // empty assertion is the hub's identity, so every later guard resolved
  // nobody and allowed the edit. The stand-ins are re-read on every call,
  // which is two small files, until the bridge has spoken. Round
  // twenty-three of the pre-release review.
  try {
    const id = (await Bun.file(`${DIR}/resolved_host_id`).text()).trim()
    if (id) return (hostCache = id)
  } catch {
    // not published yet
  }
  for (const name of ["node_id", "host_id"]) {
    try {
      const id = (await Bun.file(`${DIR}/${name}`).text()).trim()
      if (id) return id
    } catch {
      // not this file; the next one, or none
    }
  }
  return ""
}

/**
 * One tool call over the local MCP endpoint, the envelope every hook here
 * shares. Returns the tool's text payload, or null on anything short of it:
 * the daemon down, slow, unauthorised, or answering in a shape this plugin
 * does not know. Every caller treats null as "stay quiet".
 */
async function call(name: string, args: Record<string, unknown>, timeoutMs: number): Promise<string | null> {
  const key = await secret()
  if (!key) return null
  const hid = await host()
  const res = await fetch(`${ORIGIN}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": key },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "tools/call",
      params: { name, arguments: args, ...(hid ? { _meta: { "com.dibs/host": hid } } : {}) },
    }),
    signal: AbortSignal.timeout(timeoutMs),
    ...(await tlsOptions()),
  })
  if (!res.ok) return null
  const body = (await res.json()) as { result?: { content?: Array<{ text?: string }> } }
  return body.result?.content?.[0]?.text ?? null
}

/**
 * A path as the daemon compares it: absolute, with every symlink resolved,
 * the way the stdio bridge spells the paths it claims and registers. The
 * daemon resolves symlinks only for a caller on its own machine, because a
 * remote caller's path names nothing on the hub's disk; so a plugin on
 * another machine that sent the spelling opencode gave it (`/tmp/review`)
 * was compared against a claim the bridge stored as `/private/tmp/review`,
 * and an exclusive claim did not cover the edit. Round nineteen of the
 * pre-release review.
 *
 * A file that does not exist yet has no real path, and `write` creating one
 * is the common case: the deepest ancestor that does exist is resolved and
 * the rest re-attached, which is sound because the missing components
 * cannot themselves be symlinks. Mirrors internal/paths.Canonical.
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

/** Where this process is, spelled as the bridge spelled it at registration. */
function cwd(): string {
  return canonical(process.cwd())
}

/**
 * Ask Dibs whether this session may write a path.
 *
 * This is what makes a claim hold rather than merely inform. Dibs fails open
 * on every unknown, unregistered session, unclaimed path, daemon down, so a
 * deny here means a peer explicitly took an exclusive claim and is still alive.
 */
async function guard(sessionID: string, path: string): Promise<string | null> {
  if (!path) return null
  // This sits in front of every edit. If Dibs is slow the edit proceeds.
  const text = await call("guard_path", { session_id: sessionID, path: canonical(path), cwd: cwd() }, 1500)
  if (!text) return null
  const v = JSON.parse(text) as { decision?: string; reason?: string }
  // Only a hard deny stops the edit. "ask" has nowhere to go in opencode,
  // there is no permission prompt to defer to, and turning a maybe into a
  // block would wedge the fleet behind a crashed agent.
  return v.decision === "deny" ? (v.reason ?? "path is claimed by another agent") : null
}

/**
 * Ask Dibs what this session has waiting. Read-only: hook_poll never consumes
 * mail, so a dropped response loses nothing.
 */
async function poll(sessionID: string): Promise<string | null> {
  // The user is waiting on their turn: never hang it on Dibs being slow.
  const text = await call("hook_poll", { session_id: sessionID, event: "chat.message", cwd: cwd() }, 1500)
  if (!text) return null

  const payload = JSON.parse(text) as {
    hookSpecificOutput?: { additionalContext?: string }
  }
  return payload.hookSpecificOutput?.additionalContext ?? null
}

/**
 * Build a part id in opencode's own format.
 *
 * opencode validates part ids against a schema requiring the "prt" prefix, and
 * a violation does not degrade: it throws inside createUserMessage and 500s the
 * whole turn. An earlier version of this plugin used `agents-<ts>-<rand>` and
 * killed every session it touched.
 *
 * Mirrors packages/opencode/src/id/id.ts: prefix + "_" + 12 hex digits of
 * (millis << 12 | counter) + random base62 out to 26 chars. Replicated rather
 * than imported so the plugin keeps its zero-runtime-dependency property.
 */
/**
 * The agent this session belongs to, or null.
 *
 * Same endpoint as poll() and the same session identity: hook_poll names the
 * agent whether or not it has news, precisely so a caller that wants the
 * RELATIONSHIP rather than the mail can ask for it.
 */
/**
 * A monotonic count of turns this process has taken, reported to Dibs.
 *
 * opencode is the one harness whose progress Dibs cannot observe from outside:
 * sessions live in SQLite, where byte growth measures WAL churn rather than
 * work, and the only append-only file is a single opencode.log SHARED by every
 * run on the machine: watching it would make every opencode agent look busy
 * whenever any one of them was.
 *
 * So this process counts for itself. The unit does not matter and the number is
 * never compared against anything but its own previous value; what matters is
 * that it only goes up while work is happening, and stops when it is not. That
 * is the difference between catching a hard stall, which CPU alone already
 * catches, and catching a slow one.
 *
 * Fire-and-forget. A supervision signal that can delay a turn is worse than a
 * missing one.
 */
let turns = 0

function reportProgress(): void {
  turns++
  void (async () => {
    try {
      await call("hook_session", { session_id: SESSION, event: "chat.message", cwd: cwd(), progress: turns }, 1500)
    } catch {
      // The daemon is down or slow. The turn is not ours to hold up.
    }
  })()
}

async function agent(): Promise<string | null> {
  try {
    // In front of every shell command the agent runs: never hang one on Dibs.
    const text = await call("hook_poll", { session_id: SESSION, event: "shell.env", cwd: cwd() }, 1500)
    if (!text) return null
    const id = (JSON.parse(text) as { agent?: string }).agent
    return id && id.length > 0 ? id : null
  } catch {
    return null // daemon down, secret unreadable, shape changed: stay quiet
  }
}

const B62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
let lastMs = 0
let counter = 0

function partID(): string {
  const ms = Date.now()
  if (ms !== lastMs) {
    lastMs = ms
    counter = 0
  }
  counter++
  const n = (BigInt(ms) * BigInt(0x1000) + BigInt(counter)) & ((BigInt(1) << BigInt(48)) - BigInt(1))
  const hex = n.toString(16).padStart(12, "0")
  let rand = ""
  for (let i = 0; i < 26 - 12; i++) rand += B62[Math.floor(Math.random() * 62)]
  return `prt_${hex}${rand}`
}

export const DibsPlugin: Plugin = async () => {
  return {
    /**
     * Refuse an edit that would trample a peer's exclusive claim.
     *
     * opencode's `tool.execute.before` returns void, so a throw is the only way
     * to stop a call: it surfaces as a tool error the model reads and can act
     * on, which is exactly the outcome wanted: the agent learns who holds the
     * path and can send them a request instead of silently clobbering them.
     *
     * Every failure path stays silent and allows. A coordination plugin that
     * breaks editing when the daemon is down is worse than no plugin.
     */
    /**
     * Stamp every shell command with the agent that issued it, so a subagent it
     * spawns can be attributed to that agent when it stalls.
     *
     * This is the cleanest of the four harness integrations, and opencode is
     * the only one that allows it. Claude Code and Codex expose a shell tool
     * whose arguments are `command`, `workdir` and `timeout`: no environment,
     * so their plugins must PREFIX an assignment onto the command string, and
     * then refuse to do so for subshells, leading redirects, multi-line scripts
     * and `cd /x && codex exec …`, because a prefix changes the meaning of each
     * one. Every refusal is a subagent attributed by a weaker signal.
     *
     * `shell.env` hands over the environment map directly. Nothing is parsed,
     * so nothing can be misparsed and there are no shapes to refuse. The
     * variable is inherited at fork and survives reparenting, daemonisation and
     * process-group changes, so it reaches every descendant however deep,
     * which is the point, since a detached child's PPID is 1 and ancestry then
     * tells you nothing.
     */
    "shell.env": async (_input: unknown, output: { env: Record<string, string> }) => {
      // Never overwrite. The OUTERMOST parent is the one that can act on a
      // stall; re-stamping at each level would reassign a child to its nearest
      // ancestor instead.
      if (output.env["DIBS_PARENT"]) return
      const id = await agent()
      if (id) output.env["DIBS_PARENT"] = id
    },

    "tool.execute.before": async (input, output) => {
      // Deliberately does NOT require input.sessionID. The guard is keyed on
      // SESSION, so demanding an id it never uses would only add a way to skip
      // the check.
      if (!/^(edit|write|patch|multiedit)$/i.test(input?.tool ?? "")) return
      const path = output?.args?.filePath ?? output?.args?.file_path ?? output?.args?.path
      if (typeof path !== "string" || !path) return
      let reason: string | null = null
      try {
        reason = await guard(SESSION, path)
      } catch {
        return // daemon down, timed out, malformed: never break the edit
      }
      if (reason) throw new Error("Dibs: " + reason)
    },

    "chat.message": async (input, output) => {
      // Counted before anything else, so a turn that fails later still counts
      // as work done: the agent was running, which is the question.
      reportProgress()
      if (!input.sessionID) return
      let context: string | null = null
      try {
        context = await poll(SESSION)
      } catch {
        return // daemon down, timed out, malformed: never break the turn
      }
      if (!context) return // no mail: inject nothing at all

      output.parts.push({
        id: partID(),
        sessionID: input.sessionID,
        messageID: output.message.id,
        type: "text",
        text: context,
        synthetic: true,
      } as (typeof output.parts)[number])
    },
  }
}

export default DibsPlugin
