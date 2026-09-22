/**
 * Dibs extension for pi.
 *
 * pi has no MCP client, so unlike every other harness Dibs cannot ride in on a
 * server config. It has something better: `pi.registerTool()`, plus a
 * `before_agent_start` hook that can inject a message into the turn. That is
 * exactly the two halves Dibs needs: a tool surface, and a place to deliver
 * mail.
 *
 * THIS FILE IS A TRANSPORT, NOT A CLIENT. It speaks JSON-RPC over a pipe to
 * `dibs mcp-stdio`, the same bridge every other harness connects through, and
 * that binary decides everything about what a call carries: which daemon to
 * dial (including a hub that has moved), the local secret, the TLS trust
 * store, which computer this is, which checkout, how a path is spelled and
 * resolved, and which arguments are paths at all.
 *
 * It did not used to. This extension re-implemented all of that in about four
 * hundred lines of TypeScript, and the pre-release review then spent rounds
 * nineteen through fifty-five handing it, one at a time, rules the Go bridge
 * already had: the host stamp, the repository stamp, canonical paths, the
 * portable spelling of a Windows path, the path-argument table, the exception
 * for a path named on another agent's behalf, an HTTPS trust store Node's
 * fetch cannot be given, an origin that has to be re-read because the hub
 * moves. Each arrived a release late, and each was found in behaviour rather
 * than by a test. One implementation of a rule is the fix; this is it.
 *
 * What stays here is what only pi can know: its own session id, the model and
 * provider the user named on ITS command line, its version for the handshake,
 * and the two hooks that make pi a participant at all.
 *
 * Install: copy to ~/.pi/agent/extensions/dibs.ts (global) or
 * .pi/extensions/dibs.ts (project-local). Both are auto-discovered and can be
 * hot-reloaded with /reload.
 *
 * Env: DIBS_BIN (the dibs binary, default: on PATH). DIBS_ADDR and DIBS_DIR
 * are read by that binary rather than by this file, so whatever
 * `dibs mcp-config` writes for the bridge works here unchanged.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent"
import { Type } from "typebox"
import { execFile, spawn, type ChildProcessWithoutNullStreams } from "node:child_process"
import { promisify } from "node:util"

const run = promisify(execFile)

const PREFIX = "dibs_"
const BIN = process.env["DIBS_BIN"] ?? "dibs"

type McpTool = {
  name: string
  description?: string
  inputSchema?: Record<string, unknown>
}

/**
 * One call to the daemon, through a `dibs mcp-stdio` child of its own.
 *
 * A CHILD PER CALL, not a long-lived one. The first cut kept one bridge for
 * the life of the plugin, and under bun a piped child keeps the parent's
 * event loop alive however it is unref'd (`stdin.unref` does not even exist
 * there): the harness finished its turn and would not exit, and the child
 * outlived it as an orphan. Measured rather than argued: a whole cold call,
 * spawn included, is 8-17ms against a running daemon, which is nothing
 * beside the 1500ms a guard is allowed, and it leaves no lifecycle to get
 * wrong. The child is killed when the call settles, timeout included.
 *
 * EXCEPT ONE, and it is the reason `linger` exists. Registering starts the
 * bridge's index shipper, which is what gives a checkout the daemon cannot
 * read (a peer machine, a directory macOS withholds from a launchd process)
 * any semantic matching at all, and its first look at the daemon's verdict
 * is three seconds out. Killing that child at the answer meant pi
 * registered successfully and shipped nothing, ever. opencode does not have
 * this problem because it ALSO runs `dibs mcp-stdio` as its MCP server, and
 * that process lives for the session; pi has no such process, because this
 * extension is its whole surface. Found by the pre-release review, round
 * fifty-eight, in this transport's own round.
 *
 * So a lingering child is let go of rather than killed: its stdout is
 * destroyed, because that pipe is what holds the parent's event loop, and
 * the handle is unref'd. Measured on bun 1.3.14 and on node: the parent
 * exits at once and the child is still running two seconds later. It ends
 * when pi does, since the write end of its stdin closes then and a stdio
 * server reads that as goodbye, so the shipper's life is the session's and
 * there is nothing to leak. At most one is held; a second replaces it.
 *
 * The handshake rides in front of the call on the same pipe. Lines are
 * processed in order, so there is nothing to wait for; it exists to state
 * which harness this is, which the server takes from clientInfo and from
 * nowhere else.
 */
let lingering: ChildProcessWithoutNullStreams | undefined

function bridgeCall(
  method: string,
  params: unknown,
  timeoutMs: number,
  client: { name: string; title: string; version: string },
  linger = false,
): Promise<any | null> {
  let proc: ChildProcessWithoutNullStreams
  try {
    proc = spawn(BIN, ["mcp-stdio"], { stdio: ["pipe", "pipe", "ignore"] })
  } catch {
    return Promise.resolve(null) // no dibs here: the plugin is inert, the harness unharmed
  }
  return new Promise((resolve) => {
    let settled = false
    let buf = ""
    const finish = (v: any) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      // A lingering child is kept only when it ANSWERED: one that timed out
      // or died has no shipper to run and would just be a stray process.
      if (linger && v !== null) {
        try {
          lingering?.kill()
        } catch {
          /* already gone */
        }
        lingering = proc
        try {
          proc.stdout.destroy() // the pipe that would hold pi's event loop
          proc.unref()
        } catch {
          /* nothing to let go of */
        }
      } else {
        try {
          proc.kill()
        } catch {
          /* already gone */
        }
      }
      resolve(v)
    }
    const timer = setTimeout(() => finish(null), timeoutMs)
    proc.stdout.setEncoding("utf8")
    proc.stdout.on("data", (chunk: string) => {
      buf += chunk
      for (;;) {
        const nl = buf.indexOf("\n")
        if (nl < 0) break
        const line = buf.slice(0, nl).trim()
        buf = buf.slice(nl + 1)
        if (line === "") continue
        try {
          const msg = JSON.parse(line)
          if (msg?.id === 2) finish(msg)
        } catch {
          /* a line this plugin did not ask for */
        }
      }
    })
    proc.on("error", () => finish(null))
    proc.on("exit", () => finish(null))
    try {
      proc.stdin.write(
        JSON.stringify({
          jsonrpc: "2.0",
          id: 1,
          method: "initialize",
          params: { protocolVersion: "2025-11-25", capabilities: {}, clientInfo: client },
        }) + "\n",
      )
      proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n")
      proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: 2, method, params }) + "\n")
    } catch {
      finish(null)
    }
  })
}

/** pi's own harness identity for the handshake, measured once. */
let clientCache: { name: string; title: string; version: string } | undefined

async function client(): Promise<{ name: string; title: string; version: string }> {
  if (clientCache) return clientCache
  let version = ""
  try {
    const { stdout } = await run("pi", ["--version"])
    version = stdout.trim().split(/\s+/).pop() ?? ""
  } catch {
    /* version is a nicety; never let it stop registration */
  }
  clientCache = { name: "pi", title: "pi", version }
  return clientCache
}

/**
 * `_meta com.dibs/session` carries pi's OWN session id, which the bridge
 * keeps rather than replacing it with the one it derives from its process
 * tree: a harness that knows its session is believed, and pi is one. Every
 * other field of `_meta`, and every path in the arguments, is the bridge's
 * business and is not touched here.
 */
async function rpc(
  method: string,
  params: unknown,
  timeoutMs: number,
  sessionID?: string,
  linger = false,
): Promise<any | null> {
  let p = params
  if (method === "tools/call" && sessionID && params && typeof params === "object") {
    const call = { ...(params as Record<string, unknown>) }
    const meta = { ...((call["_meta"] as Record<string, unknown> | undefined) ?? {}) }
    meta["com.dibs/session"] = sessionID
    call["_meta"] = meta
    p = call
  }
  const msg = await bridgeCall(method, p, timeoutMs, await client(), linger)
  if (msg === null) return null
  if (msg.error) return { __error: msg.error }
  return msg.result ?? null
}

async function listTools(): Promise<McpTool[]> {
  const r = await rpc("tools/list", {}, 4000)
  const tools = r?.tools
  return Array.isArray(tools) ? (tools as McpTool[]) : []
}

/**
 * Ask Dibs what this session has waiting. Read-only: hook_poll never consumes
 * mail, so a dropped response loses nothing and the poll is safe to repeat.
 *
 * No cwd is passed. The bridge fills in the directory it runs in, resolved
 * the way the daemon compares it, exactly as it does for every other harness;
 * a cwd supplied from here is the spelling that used to miss its own agent.
 */
async function pollMail(sessionID: string): Promise<string | null> {
  const r = await rpc(
    "tools/call",
    { name: "hook_poll", arguments: { session_id: sessionID, event: "before_agent_start" } },
    1500,
    sessionID,
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
 * Which agent this session is, for attributing the subagents it spawns.
 *
 * hook_poll names the agent whether or not it has news, precisely so a caller
 * that wants the RELATIONSHIP rather than the mail can ask for it.
 */
async function spaceOf(sessionID: string): Promise<string | null> {
  const r = await rpc(
    "tools/call",
    { name: "hook_poll", arguments: { session_id: sessionID, event: "tool_call" } },
    1500,
    sessionID,
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
    const rand = Math.floor(Math.random() * 0xffffffff)
      .toString(16)
      .padStart(8, "0")
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

/**
 * The identity fields only pi can observe.
 *
 * Host, working directory, branch and the repository belong to the bridge,
 * which measures them on the machine it runs on and spells them the way the
 * daemon compares them. What is left is what the user named on pi's command
 * line, and pi is the one harness that genuinely knows its own model because
 * of that. `harness` and `version` are deliberately absent: the server takes
 * those only from the handshake's clientInfo, and putting them in the
 * arguments silently does nothing.
 */
function observedIdentity(): Record<string, string> {
  const out: Record<string, string> = { surface: "cli" }
  const model = argvFlag("--model")
  if (model) out["model"] = model
  const provider = argvFlag("--provider")
  if (provider) out["provider"] = provider
  return out
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
        async execute(
          _toolCallId: string,
          params: unknown,
          _signal: unknown,
          _onUpdate: unknown,
          ctx: any,
        ) {
          let args = (params ?? {}) as Record<string, unknown>
          const sessionID = sessionIdOf(ctx)
          if (t.name === "register") {
            // An OBSERVED value overrides whatever the model typed. This is not
            // the bridge's "the agent knows better" rule, and deliberately so:
            // the first pi run reported `model: "gpt-4"` while actually running
            // gpt-oss-120b. A field we can measure is never improved by asking.
            const merged: Record<string, unknown> = { ...args, ...observedIdentity() }
            // Without a session_id, re-registering after a context loss forks a
            // sibling agent and the original's mail becomes unreachable.
            if (typeof merged["session_id"] !== "string" || merged["session_id"] === "") {
              merged["session_id"] = sessionID
            }
            args = merged
          }
          // register is the one call whose bridge is kept: see bridgeCall.
          const r = await rpc("tools/call", { name: t.name, arguments: args }, 30_000, sessionID,
            t.name === "register")
          if (r === null) {
            throw new Error(
              `Dibs is not reachable through \`${BIN} mcp-stdio\`: is dibd running, and is ` +
                `${BIN} on PATH (or named by DIBS_BIN)?`,
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

  /**
   * Deliver mail at the top of the turn.
   *
   * This is the pi equivalent of the opencode `chat.message` hook: Dibs stays
   * a service the agent pulls from, and the extension only decides *when* to
   * pull. No polling loop, no driving of the harness. See PHILOSOPHY.md.
   */
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
