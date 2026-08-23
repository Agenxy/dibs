# Changelog

Notable changes to Dibs. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- **A stolen board session could read every mailbox and grant a role.** Cookies
  are host-scoped and never port-scoped, and `SameSite` does not separate ports
  either, so a second server on `127.0.0.1` receives `dibs_session` as soon as
  the operator visits it and can replay it from outside a browser. That reached
  `/api/messages` and `/api/admin/role`.

  Requiring an `Origin` header, which is what the previous entry here claimed as
  the fix, does not stop it: a process that is not a browser sets its own
  headers and declares the board's own origin. The regression test written with
  that fix asserted the forged request must be **accepted**, so it encoded the
  hole as a requirement.

  Redeeming the magic link now also hands the browser a **page key**, in the
  redirect's fragment: fragments are never sent to a server, and the board keeps
  it in `localStorage`, which is scoped by port. Mail, `/api/me`, `/api/act/*`
  and every `/api/admin/` route require it. The cookie alone still opens the
  board document and `/events` (`EventSource` cannot send a header), and
  neither carries mail. `SECURITY.md` says so plainly rather than claiming the
  exposure is gone.

- **A board session cookie opened the coordination tier, including `/mcp`.**
  `/mcp` is deliberately not a god-view path, so it sat behind "local secret OR
  a valid board session". A local service replaying the host-scoped cookie could
  therefore POST `/mcp`, call `register` (which needs no prior credential) and
  be handed an agent token: the whole coordination surface, without ever holding
  `local.secret`. The tier now takes the local secret, or a board page that has
  proved itself.

- **The page key protected nothing, because the routes it exempted carried all
  the mail.** `SECURITY.md` and the guard both said the board document and
  `/events` carry board state and not mail. Both called `AllMessages`: the
  document embedded every decrypted body in the HTML, and every SSE snapshot
  streamed them. So a cookie thief got the complete mailbox and every update to
  it, straight past the control added for exactly that attacker. The end-to-end
  test *required* a private message body to be present in a cookie-only
  response, so the suite pinned the leak in place. Mail is out of both routes;
  the board fetches it from `/api/messages`, which needs the key.

- **A declared role was granted to whoever registered under the name first.**
  The pin file held every *later* impostor to the first registrant's identity
  and asked the first one nothing, so an agent that read `dibs.toml`, or guessed
  that `admin = ["fleet-lead"]` is a likely line, could register under that name
  before the operator's own agent came up and be handed the god view with every
  agent's mail in it. The two-minute window made that a race, not a safeguard.
  A declared role now requires `[roles.identity]` to name that agent's
  **fingerprint**; without it nothing is granted, and the daemon logs the line
  to paste. **Breaking for anyone using `[roles]`.**

  The first version of this asked for the raw **nonce**, which was worse than
  the hole it closed: a nonce is the whole recovery credential, so any same-user
  process that read `dibs.toml` could reattach *as* the admin, rotate its token
  and take its mailbox at any time, rather than merely winning a two-minute
  race. It takes a 64-character hex fingerprint now, and refuses anything that
  is not one.

- **Published macOS artifacts shipped without the Touch ID helper.** GoReleaser
  built `dibd` and `dibs` and nothing else, and the runtime looks for
  `dibs-presence` beside the executable, so every archive and every `brew
  install` silently fell back to the admin password while the documentation
  described a presence-first flow. The release job already runs on macOS; it
  builds and ships the helper now, and the drift guard that watched only the
  Taskfile watches the release too.

- **An archived coordinator stranded the board.** The claim guard added above
  spelled out its own idea of who coordinates and excluded only closed agents,
  while the resolver everything else uses correctly ignores archived ones.
  Archiving blanks the token and nonce and resume refuses archived identities,
  so a board whose only coordinator was swept had nobody able to coordinate and
  no way to claim it. It asks the one resolver now.

  Round seven found the same defect in a *second* copy, `core.HasCoordinator`,
  which is the one the startup claim path actually consults, so the strand
  survived the first fix. It is defined as `CoordinatorID` now rather than as
  its own scan of the roster: two functions cannot disagree if there is only one
  of them.

- **`DIBS_ADDR` schemes were accepted and then discarded.** The daemon read the
  variable, stripped `http://` or `https://` so `net.Listen` would take it, and
  re-inferred the transport from the bare address, while every client honours
  the scheme. `http://10.0.0.9:4777` served TLS to clients speaking plaintext;
  `https://127.0.0.1:4777` served plaintext to clients speaking TLS. The scheme
  is now carried into the shared transport rule, an uppercase one is lowercased
  rather than failing later inside Go's HTTP transport, and `http://` alongside
  a configured certificate is refused as the contradiction it is.

- **A launch claim stayed usable after the board had a coordinator.** The claim
  file is minted at startup when none exists, and a role declared in
  `dibs.toml` is granted seconds later by the reconciler, so an ordinary
  startup left a live claim in the data directory beside a board that already
  had its coordinator. A second persistent agent that read it, which is
  same-user readable and documented to be, could take broadcast,
  `force_release`, eviction and mailbox adoption. Refused at ingress, never in
  the fold, so replay still accepts claims that were legal when written.

- **The role pin failed open in two ways**, found by the review round after the
  one that added it. `loadRolePins` treated every read error as "no pins yet",
  so a permissions problem on `roles.pinned` silently re-opened every declared
  role to whoever held its name; and `check` recorded a fingerprint in memory
  before `save` succeeded, so a failed write left it there and the next
  reconciliation fifteen seconds later matched against its own unsaved value
  and granted the role with nothing durable behind it. Both now refuse. A
  security decision that survives only in memory is one that disappears on
  restart while behaving as though it had not.

- **A half-configured certificate pair started the daemon on a transport
  nobody asked for.** With `tls_cert` set and `tls_key` absent (or the
  reverse), both were treated as absent: plaintext on loopback, an unrelated
  self-signed certificate off it, and the operator's explicit setting doing
  nothing silently. Refused now, in the shared config so `dibd -check` and
  `dibs mcp-config` both see it.

- **A declared standing role could be taken by any agent that chose the right
  name.** `[roles] admin = ["release-manager"]` in `dibs.toml` authorises a
  STRING, and an agent picks its own name at registration. So an agent that
  could read that file, or guess the name, registered under it and the
  reconciler granted it admin on the next tick: the god view over every
  decrypted mailbox. Both `SECURITY.md` and `docs/CONFIGURATION.md` promised
  "no agent can promote itself", and it did not have to: it only had to be
  called the right thing before the intended agent was. Present in v0.0.5 and
  v0.0.6, and reproduced against a live daemon before it was changed.

  A grant is now pinned to the credential of the agent it first lands on,
  recorded in `<data-dir>/roles.pinned`, and the same name is refused later
  under a different identity. The agent must have registered with a nonce,
  since without one it cannot prove it is itself after a restart, which is the
  whole of what a standing role needs. The grant window also closes about two
  minutes after start, so an unclaimed name stops being a standing invitation
  to whoever registers under it later; it is reported once and left alone.

  Preconditions were narrow: the attacker had to be on the board already, and
  `[roles]` had to be configured at all, which is not the default. That is why
  this is a changelog entry rather than an advisory.


### Added

- **An agent that is not running can be woken: `[wake.exec]`.** Mail arrived for
  agents that were not executing, and sat there. Dibs would deliver it at their
  next activation, which for a dormant agent is whenever a human next happens to
  start them, so a question with a ten-minute deadline expired unread and the
  answer looked like a Dibs failure. That is the difference between a message
  bus and a phone.

  An operator may now name, per harness, the command that resumes a thread:

  ```toml
  [wake.exec.codex]
  argv = ["/Applications/ChatGPT.app/Contents/Resources/codex",
          "exec", "resume", "{thread}", "{message}"]
  ```

  `docs/CONFIGURATION.md` records why that command and not `codex queue`, which
  was measured on the same day and wakes a thread only when one is already
  loaded: pointed at a stopped one it returns `Queued message` and nothing
  stirs.

  Dibs runs it when a message that somebody is waiting on lands for an agent it
  has not heard from recently: `question`, `request` and `handoff`, plus the
  verdicts (`approved`, `denied`, `answered`, `declined`) that resolve one. A
  `notify` never wakes anybody, because nobody is waiting on it. `{thread}` is
  the harness's own thread identifier, taken from the agent's session aliases
  and only when it has the shape a resume command accepts. It is deliberately
  NOT the agent's `session_id`: that names the harness process (`host-92368`)
  and dies with it, so an agent whose only identifier is one of those is not
  woken, because there would be nothing to hand the command. Where an agent has
  reattached and holds several, it is the CURRENT one: aliases are appended, so
  the newest is last, and resuming an older thread would start a real session
  that is not the one holding the mail.

  Wakes are rate-limited per agent (90s by default, `cooldown =`), so a burst of
  three messages is one wake and not three, and an agent that has made an
  authenticated call inside that window is left alone because it is plainly
  running. The command itself is bounded at two hours, not at anything shorter:
  `codex exec resume` runs the agent's whole turn in that process, so a short
  bound is a cap on the work rather than on starting it.

  **The operator decides this, not Dibs and not the agent.** There is no default
  command for any harness: with no `[wake.exec]` section nothing is ever
  executed, which is the behaviour of every release before this one. An agent
  cannot ask to be woken by a command of its choosing, and cannot name the
  argv. PHILOSOPHY rule 5 says Dibs does not drive harnesses; this is the
  operator driving their own, through a line they wrote, and `WAKE-MECHANISMS.md`
  records why the earlier shell-hook version was deleted and why this one is not
  the same thing.

- **The Codex plugin binds `hook_poll` to the thread lifecycle.**
  `plugins/codex/hooks/hooks.json` registers `mcp_tool` handlers on
  SessionStart, Stop and SubagentStop, so a Codex thread that is running
  collects its mail at each of them without polling. Measured against a live
  daemon: three hooks, three deliveries. This covers a thread that is alive;
  `[wake.exec]` above is what covers one that is not.

- **The multi-machine board is documented and has a command.** Everything needed
  for a real-time fleet board already shipped; the operator who runs Dibs for
  that reason nearly gave up on it. `dibs mcp-config --board <addr>` prints the
  join config for another machine's board: the data directory, the secret copy,
  the ssh forward, and the config for both harnesses. `README.md` gains a second
  machine section.

  The recipe was in the binary all along and unreachable: it printed only inside
  the TLS branch, so a plaintext loopback daemon (every fresh install) never
  showed it. It prints unconditionally now.

  The **ssh forward is named as a supported transport**. A loopback daemon is
  unreachable from another host and the documented answer was a routable TLS
  endpoint, which excludes the corporate and lab networks full of hosts that will
  never have one. The forward always worked, because the bridge only ever talks
  to an address, and it is the better shape: the daemon never leaves loopback.
  With it, a paragraph on choosing the hub, since that choice decides whether the
  fleet has a board at all and the laptop is the tempting wrong answer.

### Changed

- **`dibs.toml` has one type and one loader.** The daemon decoded the file into
  its own struct and refused any key it did not recognise; `dibs mcp-config`,
  which describes the daemon an agent will connect to, decoded a four-field
  projection and checked the rest against a hand-kept list of key NAMES. That
  list validated spelling and nothing else, so `[limits] agent_ttl = 10` passed
  the CLI and produced a configuration while `dibd -check` refused the same
  file: the daemon would not start, and the command telling an operator how to
  reach it reported success. Both now call `internal/boardconfig`, and the
  key-name copy and its drift test are gone with the need for them.


- **The transport advice for another machine was backwards.** "Use the url form
  only from ANOTHER machine" sent operators to a url client for the case where a
  forked identity costs most: a remote session is the long-lived unattended one.
  The url form is now scoped to a client that cannot run a process at all, here
  and in the codex and chatgpt-desktop plugin READMEs.

- **`register` documents both of its continuity paths.** It returns `resumed`
  when the agent was still active and this was a retry, and `reattached` when it
  had stopped and the nonce recovered it. The description named only the second,
  so an integrator testing the obvious way lands on the first, sees neither the
  documented key nor an explanation, and concludes identity continuity is broken.
  Both are named now, with the token rotation on reattach: a client that cached
  the old token is holding a dead one. Paid for inside the tools/list budget
  rather than by raising it.

- **The install nudge no longer repeats on every register.** Only `reattached`
  suppressed it, so a still-active agent re-registering with its nonce came back
  `resumed`, was treated as a first connection, and read the four-sentence
  paragraph again every time.

### Fixed

- **Every lifecycle hook announces its session, not just `hook_session`.** The
  announced-session join is how an agent learns the identifier its own harness
  uses, and it is the only source of the thread `[wake.exec]` resumes. The
  Claude Code plugin binds four hook tools and so had one; the Codex plugin
  binds `hook_poll` and only `hook_poll`, so a Codex thread announced nothing
  and the wake command had no thread to resume. `hook_poll` announces too, so a
  harness that binds the obvious single tool gets a working wake path instead of
  a silent no-op. Found by `internal/mcp/e2e/wake_e2e.ts`, which walks the whole
  chain against a real daemon.

- **The second-machine recipe guessed its transport from the address.** An ssh
  forward and a `dibs trust` step both depend on what the daemon serves, and
  both were inferred from where it listens. A LAN daemon with
  `insecure_plaintext = true` was handed an address with no scheme and the
  joiner's bridge re-inferred https against a plaintext port; a loopback daemon
  with a certificate pair also lost its trust step, so the joiner had no way to
  accept the certificate. The recipe is told what is served.

- **The CLI decided its transport from the address alone.** `dibs.toml`
  supports `insecure_plaintext` and an explicit certificate pair, and the daemon
  honours both through the shared resolver; `origin()` looked only at whether
  the host was loopback. A LAN board with `insecure_plaintext = true` was
  contacted over HTTPS, and a loopback board with a certificate over HTTP, by
  `doctor`, `mcp-stdio`, `admin`, `await` and every ordinary request. It asks
  the shared resolver now.

- **One agent named as both coordinator and admin flipped between them** every
  fifteen seconds for the whole startup window, two ledger entries a pass, with
  a gap in which admin-only calls failed. Refused at load.

- **Explicit zero supervision settings, and `nan`, validated and did nothing.**
  `every = "0s"` is not "no limit": the daemon takes these only above zero, so
  it kept the default and nothing said so. `min_duty = nan` passed the range
  check because no comparison against NaN is true. An absent key and an explicit
  zero are the same value in the struct and opposite intentions, so validation
  now asks the decoder which keys were actually written.

- **`mcp-config --board` accepted hosts that cannot become a URL.** The check
  listed forbidden characters, so it caught the ones somebody thought of and
  passed spaces, control characters and invalid escapes. It asks `net/url` the
  same question the failure would ask later.

- **`dibs doctor` ignored the address `dibs configure` wrote.** It built its
  request from the environment alone, so a healthy daemon configured only in
  `dibs.toml` was reported unreachable and every harness was then checked
  against a board that was never running. It resolves the address the same way
  the daemon does, which is what `docs/CONFIGURATION.md` has been promising.

- **One repository failing switched matching off for the whole board.** The
  unreadable-tree and indexing-tree routes were fixed before the tag; the
  ordinary mining and listing failures still replaced the entire global status
  with `off`, while the first repository's scorer stayed installed and went on
  producing results. The board annotated declarations with "matching is off"
  while matching demonstrably worked. The failing tree is named; the phase now
  belongs to the trees that are actually broken.

- **A recovered repository stayed reported as unreadable on the default path.**
  Recovery cleared the diagnosis only on the `ready` phase, which is the phase
  only when a join threshold is configured. The shipped default reports
  `suggest-only`, and a sidecar fallback reports `degraded`, so on an ordinary
  board the operator was told about a permissions problem that had been fixed
  until the daemon restarted.

- **`min_duty` loaded happily at values that could not work.** A negative one
  was ignored and the default silently kept; one above `1` is worse than
  ignored, because the duty check *acquits* a process that clears the
  threshold, so a threshold nothing can clear acquits nobody and every process
  past `min_age` becomes eligible for a stuck verdict. It is a fraction, and it
  is now checked at both ends.

- **`auto_join` was validated against a vocabulary that does not exist.** The
  shared loader accepted `declared`, `predicted` and `off`, while the engine
  implements `declared`, `always` and `never`. So working boards configured
  with `always` or `never` stopped starting, and the error recommended two
  values that silently behave as `declared`. Both vocabularies the loader knows
  are now checked against the engine's own constants.

- **`dibd` could not bind a `DIBS_ADDR` carrying a scheme.** Every other Dibs
  binary accepts one because it says what to speak to a remote board;
  `net.Listen` takes host:port and answered "too many colons in address" after
  the daemon had announced itself. Reading the variable was right, passing its
  scheme to `Listen` was not.

- **The preferred stdio configuration discarded the resolved transport.** The
  bridge rebuilds a scheme from the address alone, so a plaintext daemon off
  loopback was handed a bare address and inferred HTTPS, while the url block
  printed the correct answer. The generator now says the scheme out loud
  whenever the bridge would infer a different one.

- **A refused service install had already created the board directory.** The
  `mkdir` ran before the conflict checks that then refuse, so where a loaded
  unit's directory had been moved or damaged, the command recreated an empty
  board at the old path for the existing job to start against, and then
  reported that it had refused.

- **A failed retry erased the unreadable diagnosis it was reporting.** Scorer
  failure paths publish a repository with no `Unreadable` field, and every such
  status was treated as proof that repository had recovered.

- **The plugin still promised delivery the engine refuses.** `UserPromptSubmit`
  never delivers to the model, and `Stop` does not under `wake = none`, a
  repeated wake, or `stop_hook_active`, so mail arriving after the preceding
  Stop can be absent for a whole turn. The pitch says what the hooks buy and
  what still makes delivery certain.

- **A wildcard bind produced a certificate no client could verify.** The
  wizard's "this machine and others" writes `0.0.0.0`, so the generated
  certificate carried IP SAN `0.0.0.0` and DNS SAN `localhost` and nothing
  else. `mcp-config` then correctly refuses to hand anybody a listen address
  and substitutes this machine's LAN address, which was not in the
  certificate: one unusable answer traded for another. A wildcard bind now
  covers the machine's own addresses. Where none can be detected, the command
  refuses rather than printing `<this-machine>` inside an otherwise complete
  configuration.

- **A URL was accepted as the daemon's listen address.** `addr =
  "https://127.0.0.1:4777"` passed the loader and produced a confident HTTPS
  configuration, while `dibd` hands that value to `net.Listen`, which cannot
  bind a URL. Two grammars were being checked by one validator: a scheme is
  valid on a client's `DIBS_ADDR` and never on an address a daemon binds.

- **More settings that read as applied and were not**: a negative blob store,
  a negative match history, a match deadline that is not a duration, an
  `auto_join` value naming nothing, and negative supervision intervals. Each
  loaded cleanly while the daemon refused or silently ignored it.

- **A recovered repository still stayed reported unreadable.** The failure
  records the agent's working directory and the recovery reports the
  repository root it resolved to, so comparing exactly removed a path nothing
  had recorded. It drops by containment now. The test used one path for both
  ends, which is why the first fix looked right.

- **`E_MSG_FINAL` pointed at a human mailbox that may not exist.** The human
  row is created when the operator first acts, so a headless board has none.

- **Three test defects.** `holdsRole` asked whether an agent held a role by
  calling `GrantRole`, which GRANTS it: every authorization assertion in that
  file rested on a probe that mutated what it inspected. The self-promotion
  test asserted the effect only inside `if err == nil`, so an op that returned
  success while doing nothing passed. And the unreadable-tree test used the
  same path for failure and recovery.

- **The shared config loader validated keys but not values.** It rejected an
  unknown key and stopped there, while the daemon goes on to check that
  durations parse and clear a floor, that ceilings are neither negative nor
  mutually contradictory, and that the wake policy names something. So
  `[limits] agent_ttl = "10"` and `[wake] extend_turn_for = "everything"` both
  loaded here and stopped `dibd`: the same success-that-is-false the shared
  package was created to end, found one round after creating it. The checks
  moved in with the type; what stays with the daemon is the part that needs its
  own defaults, and the comment no longer claims otherwise.

- **`doctor` could not reach its own damaged-ledger diagnosis.** It returned as
  soon as the daemon was unreachable, and a corrupt ledger is usually WHY the
  daemon is unreachable: the operator got "daemon unreachable" and nothing
  else, while the check that names the broken record and says not to delete the
  file sat behind that return. Everything that reads this machine's own files
  now runs when the daemon is down, which is when it matters.

- **A configured local board that lost its daemon files still read as a join.**
  `dibs.toml` is what `dibs configure` writes for a board of its own, and a
  joining directory has no daemon to configure, so its absence from the
  daemon-owned list meant the wizard's ordinary output was mistaken for
  somebody else's board the moment its ledger went missing.

- **A mistyped scheme was refused on one path only.** `DIBS_ADDR=htps://…`
  exited 0 and emitted the typo as both the bridge's address and the MCP url;
  `--board` had rejected exactly that since the last round. One validator now
  serves both, split so the rule that only applies to a board you DIAL (a
  wildcard is a legitimate bind address, and the wizard writes one) does not
  refuse this daemon's own configuration.

- **Two more false delivery promises.** The claude-code plugin's setup step
  still told an operator mail appears "on your next tool call" and blamed a
  PreToolUse hook when it did not, and `send`'s description told every harness
  it reaches a recipient at a turn boundary, which is untrue for Codex, where
  nothing invokes Dibs automatically. The guard added last round read only the
  plugin's summary; it reads every published string now, and refuses that
  phrase by name.

- **`doctor` could call a damaged local TLS board a healthy join.** The
  daemon-owned artifact list omitted `tls-key.pem` and `admin.hash`. A joining
  client holds the board's public certificate and never either of those, so a
  board that had lost its ledger, node id, key and blobs while keeping one of
  them read as a join and skipped the check that would have reported the loss.

- **A repository stayed reported unreadable after its permissions recovered.**
  Unreadable trees survive a phase change on purpose, but the preserve kept the
  whole list and no production caller ever sends the empty slice that clears
  it, so the diagnosis could not be retracted without a restart. A successful
  index now drops that one tree and leaves every other.

- **Every generated Codex configuration supplied half of the documented MCP
  2026 requirement**, omitting `[features] mcp_2026_07_28 = true`, so operators
  following it stayed on the legacy protocol while the prose said otherwise.
  The README also contradicted itself about whether the flag does anything.

- **An unresolvable home directory produced a confident recipe rooted at
  `/home/you`.** On a headless host `mcp-config --board` printed mkdir, scp,
  trust and JSON all targeting a literal path that is nobody's home, with
  nothing saying it was a stand-in. It refuses now.

- **The Codex plugin guide opened by recommending the transport the rest of it
  argues against**, "a plain MCP server over HTTP: no bridge", ahead of a page
  explaining why the per-session bridge is what holds the nonce.

- **The documented release guarantee was not true of the pipeline.**
  `AGENTS.md` said nothing between the tag and the release needs a person; the
  Homebrew cask does, because the tap requires a pull request and the deploy
  key can push but cannot call the API. Until that branch is merged the release
  is out and `brew upgrade` serves the previous build. Said plainly now, and
  the release job prints what is still owed.

- **Three regression guards passed against the behaviour they named.** The
  space-id one rebuilt the corrected expression and compared it with itself;
  the enrichment one read a session sidecar that was not there and checked the
  universal fields instead; the continuity one searched for `resumed`,
  `reattached` and `ROTATED` separately, so swapping the two token rules left
  it green while telling a client to keep a dead token. Each now drives the
  production path, and each was verified by restoring the exact regression and
  watching it fail.

Acting on an independent operator evaluation of v0.0.6, which ran Dibs across two
machines for real work. Their priority order, not ours.


- **An agent on a terminal host could not show the board it was reading.**
  `board` renders to an MCP Apps panel, and a host that renders none fell back to
  one line, "3 agent(s), 1 active", while `dibs board` on the same machine
  printed the board. The fallback is now the board itself, bounded at 20 rows.
  Only where no panel is declared: on a panel host the human is already looking
  at it.

- **`doctor` called a joined board a corrupt ledger.** A data directory that
  joins another machine's board holds a credential; the ledger is on the hub. It
  reported "ledger does not verify ... do NOT delete it, open an issue", a
  data-loss emergency raised against a healthy join, at the operator least able
  to tell it was spurious. Found by following the new join recipe end to end.

- **`dibs configure --service` could not install on a fresh machine.** It wrote
  the unit without creating the data directory the unit names, so the service
  failed at start with nothing pointing at the cause.

- **`doctor` reported a configured suggest-only matcher as a warning**, so a
  deliberate `join_threshold = 0` looked like a fault on every run.

- **`dibs prune` answered "did you mean dibs probe".** The CLI verbs and the
  MCP tools are different sets and nothing mapped them, so a name that is a
  real Dibs verb on the other surface was answered with the nearest unrelated
  word. The CLI now says which surface it lives on. Its copy of the tool names
  is held to the server's listing by a test.

- **`dibs configure` needed a terminal**, and the machines that most need
  configuring are headless and reached by `ssh host command`. A second machine
  in a fleet hit this on its first command. `--non-interactive` takes the
  defaults, writes the file and prints what it wrote. It refuses to overwrite
  an existing config, since there is no prompt on that path to catch it.

- **`dibs upgrade --help` now says it does not fetch.** It moves the running
  daemon onto a build already installed, which is what it should do and not
  what its name suggests; run bare on an up-to-date install it correctly does
  nothing, and that reads as a failure.

- **Five things the pre-release review caught in the above**, before any of it
  shipped: the README copied the board secret to a directory the generated
  configuration did not use, so the documented setup ended at a bridge that
  could not start; `--board` printed no `dibs trust` step for a board that is
  not on loopback, so the configuration looked complete and the bridge would
  reject the certificate; two boards on different ports of one host shared a
  credential directory, so joining the second overwrote the first; `doctor`
  keyed "this is a join" on a missing `node_id` alone, so a local board that
  lost that file but still held a ledger skipped verification and was reported
  healthy; and the successful-claim log still said "claimed by the agent that
  started this daemon", the same false attribution the startup line was
  corrected for, on the record of a privileged role being taken.

- **Two more from the review's second round**: `dibs configure <dir>
  --non-interactive --help` wrote the configuration and ignored the help
  request, which is the third instance of the shape this command's own
  comments document (`configure --service --help` wrote a LaunchAgent, `dibs
  stop --help` stopped the daemon) and the first where a flag added for
  unattended use turned an ignored argument into a silent write. `configure`
  now reads every argument before deciding anything, and refuses an unknown
  flag or a second directory. And the credential directory rewrote dots to
  hyphens, so `hub.example` and `hub-example` shared one: the port collision
  again in another character. Dots are kept.

- **Four more from the review's third round.** The generated `dibs trust`
  command omitted the board's `DIBS_DIR`, so it recorded the certificate under
  the default data directory, reported success, and the bridge went on
  rejecting the board: a step that looks done and is not. The recipe hard-coded
  the hub's secret at `~/.dibs/local.secret`, which only the hub knows and
  which is the wrong board's credential on a hub that runs two. The directory
  key still collided between an IPv6 literal and a hostname spelled like one.
  And `prune` refused self-pruning in its description while its `agent`
  parameter still offered "yours", so an agent reading both was told to make a
  call that cannot succeed.

- **Four more from the review's fourth round.** The hub-side recipe and the
  README still printed `dibs trust` bare after sending the joining bridge to a
  directory of its own, which is round three's bug in the other two places it
  is written. The generated shell lines did not quote the derived path, so a
  home directory containing a space split into two arguments. The
  `claim_coordinator` tool still offered the role to "the agent that started
  this daemon", which under a service manager is nobody, and a tool description
  is the only documentation an agent reads: that can leave a service-managed
  board with no coordinator. And the truncated text board said the rest was
  "in this result" when it is in `_meta`, which is exactly what the model on a
  no-panel host cannot see, so it pointed an agent at rows it could not reach
  and would have had it report them as present.

- **Three more from the review's fifth round**, and the credential directory
  is now keyed on the address verbatim. It collided four times, once per
  round, each fix keeping one more character while the comment above it went
  on claiming every board gets its own: the port was dropped for non-loopback,
  then dots became hyphens, then loopback was renamed "board" and collided
  with the ordinary hostname `board`. The pattern was rewriting the address
  into something that reads nicely, and every such rewrite maps two addresses
  onto one name somewhere. Also: the certificate-refused recovery message told
  an operator to run `dibs trust` without the data directory their failing
  call was using, and the `scp` source was quoted on the half this machine
  controls but not the hub's.

- **Five more from the review's sixth round.** `mcp-config` printed a
  complete-looking stdio configuration that named no address or data
  directory, so an operator running a second daemon got a config for the
  first, reading its secret and its nonce file and joining a board they were
  not asking about. Merging that into the Codex form then produced two
  `env = { ... }` lines in one TOML table, which is a duplicate key: one line
  now, protocol version included. The ssh recipe used this machine's port as
  the hub's, so a forward printed for a board on 5777 pointed at a hub that
  listens on 4777; the local end is named as the joining machine's choice.
  Addresses in pasteable commands were unquoted, and an IPv6 literal is a glob
  in zsh. And `mcp-config` ignored everything after its first positional
  argument, so `mcp-config junk --board hub:4777` printed the local
  configuration and never read the flag.

- **Four more from the review's seventh round.** The forward still used one
  port for both its ends, so `--board 127.0.0.1:5777` could not express a hub
  on 4777; the far port is the hub's to name now. The note on pinning an
  identity told the reader to add a second `env = { ... }` line, which is the
  duplicate TOML key the round before had just removed. `nonDefaultEnv` read
  the address through a helper that strips an explicit scheme, so a
  deliberately plaintext daemon off loopback handed the bridge bare
  `host:port` and the bridge inferred HTTPS. And the hub-side recipe's
  pasteable commands were still unquoted.

- **Three more from the review's eighth round**, and the address's shape is now
  decided in one place. The second-machine recipe still handed the bridge an
  address with the scheme removed, and the branch choosing between a forward
  and a certificate could not read a scheme at all, so a board explicitly named
  as plaintext was told to record a certificate it does not serve. A scheme,
  when the operator writes one, settles what the daemon serves; without one,
  loopback means a forward and anything else means HTTPS. The README's tunnel
  example also still used one port for both ends, in the paragraph that
  describes a machine already running its own board on that port.

- **Two more from the review's ninth round.** `dibs mcp-config --board` was
  refused on a machine with no terminal: the admin gate ran before the flag was
  parsed, so the invocation documented for a second machine, which is typically
  headless and driven by `ssh host command`, printed "needs an interactive
  terminal" and nothing else. `--board` prints a config for somebody else's
  board and reads no secret of this machine's, so it is not what that gate
  protects; the plain form, which prints this daemon's secret, still is. And
  the generated `dibs trust` carried a scheme, which reaches `tls.Dial` as part
  of the host and fails with "too many colons in address": the scheme belongs
  in `DIBS_ADDR`, not in a command that dials.

- **Two more from the review's tenth round.** A board can need BOTH a forward
  and a certificate recorded, and the branch printing step two was a switch, so
  a forwarded HTTPS board got the forward and no trust step: a
  complete-looking configuration that then rejects the certificate. An
  uppercase scheme was also read as plaintext. And the new build-without-mise
  section stopped at `bin/` before telling the reader to run `dibd`, which on
  a fresh machine is command-not-found and on an existing one silently runs
  the previous build; the install step is written out, Launch Services
  registration included.

- **Three more from the review's eleventh round**, the first of them a leak
  introduced by the round before it. Waiving the interactive gate for
  `--board` was scoped to the flag appearing rather than to it having a value,
  so `dibs mcp-config --board=` waived the gate, parsed as empty, fell through
  to the local form and printed this daemon's secret on a headless machine,
  exiting 0. An empty `--board` is refused now, and the waiver requires a
  value. The install recipe also never created `~/.local/bin`, and copied the
  two macOS-only artifacts unconditionally, so it failed on Linux.

- **Four more from the review's twelfth round.** The ordinary `mcp-config`
  recipe still decided the second machine's setup from "did this daemon make a
  certificate", which answers neither of the two questions it has: an HTTPS
  board on loopback needs a forward and a certificate recorded and got only
  the forward, and a board explicitly named `http://` off loopback was called
  loopback and told to tunnel. It reads the address now, as `--board` does. An
  explicit scheme also outranks the certificate file when naming the url. The
  no-panel board dropped `display_name`, which exists because a name that is
  not Latin collapses to a generic id, and silently showed one of an agent's
  declarations; it shows the name, the id, and how many it did not show. Two
  tests of ours were also named for regressions they could not catch, and now
  drive the gate and the wizard rather than the helpers beside them.

- **Three more from the review's thirteenth round.** `dibs configure` writes
  the operator's listen-address choice to `dibs.toml` and ends by telling them
  to run `dibs mcp-config`, which read only `DIBS_ADDR` and so printed a
  configuration for `127.0.0.1:4777`: a confident answer about the wrong
  daemon, from the command the wizard had just sent them to. It reads the
  configured address now, and maps a wildcard bind to something dialable,
  since `0.0.0.0` is a listen address and not one anybody can connect to. The
  second-machine recipe also handed the joining machine THIS daemon's loopback
  address, which on that machine is its own board, in the same output that
  then explains the local end of a forward is that machine's choice. And a
  left-behind `tls-cert.pem` was read as proof of TLS, so a daemon moved back
  to loopback or switched to `insecure_plaintext` was still described as
  serving HTTPS, with instructions to trust a certificate it does not present.

- **Three more from the review's fourteenth round.** The environment handed to
  a bridge still read the address the way that ignores `dibs.toml`, so a daemon
  configured onto a LAN address or a non-default port had its stdio configs
  printed with no address at all and the bridge dialled the default; the url
  block had been fixed a round earlier and this had not. `--board` accepted
  anything non-empty, including a mistyped scheme, which it then classified as
  plaintext and emitted verbatim, and a wildcard listen address no client can
  dial; both exited 0 around a configuration that cannot work. And a
  `dibs.toml` that does not parse was read as no configuration at all, so the
  daemon would refuse to start while this printed a confident config for the
  default address.

- **Five more from the review's fifteenth round.** `--board` did not check
  that the port was a port, and the port goes into the credential directory's
  name: `hub:4777/../../escaped` produced a `mkdir -p /Users/escaped` with a
  secret written into it, and exited 0. The transport was still decided by
  whether `tls-cert.pem` exists, ignoring `insecure_plaintext` and a
  configured `tls_cert`, so a daemon configured for plaintext beside a
  left-behind certificate was described as serving HTTPS. A `dibs.toml` with a
  key `dibd` does not know parses as valid TOML and makes the daemon refuse to
  start, while this printed a configuration for the default address.
  `configure --non-interactive <dir>` then told the operator to run bare
  `dibs configure --service` and `dibd`, both of which act on the DEFAULT data
  directory, so the advertised sequence configured one board and started
  another. And `doctor` called any directory with a secret and no `node_id` or
  ledger a healthy join, including a local board that had lost both but still
  held the key it encrypts with: a directory that has lost its replayable
  state, reported as nothing wrong.

- **The rule for what a daemon serves now lives in one place**, after the
  review's sixteenth round found the CLI's copy of it wrong a third time: a
  leftover certificate made it print HTTPS for a loopback daemon, which serves
  plaintext however many certificates are lying around; `insecure_plaintext`
  was allowed to beat an explicit certificate pair, which the daemon honours
  first; and a `tls_cert` with no `tls_key` was treated as authoritative,
  pointing clients at a certificate the daemon never presents. `dibd` and
  `dibs mcp-config` both call `internal/transport` now. The unknown-key check
  also covered only top-level keys, so `[match] typo_threshold` was fine here
  while `dibd -check` exits 1 on it; the key list is complete and a test reads
  the daemon's own structs, nested tables included, so it cannot drift.

- **`send` promised a wake it cannot deliver.** Its own description said
  question, request and handoff "WAKE the recipient now". They do not: mail is
  pushed by `hook_poll`, which the shipped plugins bind to SessionStart,
  UserPromptSubmit, Stop and SubagentStop, so an agent in the middle of a long
  turn has no event for one to arrive on and sees it when the turn ends.
  `WAKE-MECHANISMS.md` says exactly this under "Honest limits"; the tool
  description, which is the only thing an agent reads, did not. Found when a
  peer sent a question with the default 600-second deadline to an agent working
  a seven-hour autonomous stretch, got "recipient is dormant" back, and
  reported the product broken. The description now says when a message actually
  arrives and what a short deadline costs.

- **`register` failed outright for any Claude Code session with
  `CLAUDE_EFFORT` set.** The stdio bridge fills in identity it can observe, and
  that table is applied to `register` and nothing else. It carried an entry for
  `effort`, which is an `update` field: `register` does not declare it, and
  since v0.0.6 refuses unknown arguments rather than ignoring them, injecting it
  did not add a field, it failed the call with `-32602 register does not take
  "effort"`. No agent was created at all, so no lifecycle hook could resolve
  that session, no mail reached it, and its claim guard returned allow.
  Reproduced against the shipped v0.0.6 binary with the environment of a live
  session. A test now holds every field the bridge injects to `register`'s
  actual schema.

- **The claude-code plugin advertised a delivery moment it does not bind.**
  Its catalogue entry said a PreToolUse hook calls the wake path, so mail
  "appears in your context on your next tool call". PreToolUse binds the claim
  guard and nothing else. That text is what an agent reads when deciding
  whether it still needs to poll, so the one claim that overstates is the one
  that loses mail. A test now holds every plugin's pitch to the events its own
  `hooks.json` actually binds `hook_poll` to.

- **`E_MSG_FINAL` carried no hint**, in breach of the rule that every error
  names the corrective call, and it is the error an agent hits exactly when it
  has come back late to something it missed. It now names the corrective call:
  send a new message, and if that agent is gone, the human's row outlives them.

- **README: building without mise or task.** On a network that allows the Go
  module proxy but not the object store it redirects to, neither tool installs
  and the failure reads like a broken toolchain rather than a blocked host.
  Dibs itself still builds, because every step is a `go build` or a `go run
  ./tools/...` in-tree. The four commands are written down, along with the two
  install rules that are not obvious from them: remove before copying, because
  macOS caches a signature verdict against the inode, and set the codesign
  identifiers, which the Go toolchain leaves as `a.out`.

## [0.0.6] - 2026-08-20

### Security

Found by the pre-release review, which this project requires before every tag and
runs with a model that did not write the code. The first two were present in
v0.0.5 and are covered by GHSA-72hq-r6x6-mjwf; the rest existed only on this
branch and never shipped.

- **The operator's identity was claimable with a guessable credential.** The
  recovery nonce for the human's own agent was `"human:" + <OS username>`, and
  registration reattaches on a matching name and nonce and returns that
  identity's token. So any agent that could run `whoami` could become the person
  at the board: post, announce, send and read their mail as them, and on this
  branch approve its own coordinator grant, which the Touch ID path never sees
  because approving a request does not ask for a fingerprint. On an empty board
  the same call pre-creates the identity, and the operator is somebody else the
  first time they open it.

  `internal/core` has no notion of the human, deliberately, so the fold cannot
  tell that row from any other and a rule there would bind every register op
  already in a ledger. The refusal is at the engine's ingress, beside the other
  authorisation verdicts a caller is not trusted to assert.

- **Any local page could drive the board through the operator's browser.** The
  `Origin` check read the hostname and ignored the port. Cookies are scoped to a
  host and not a port, `SameSite=Strict` does not separate them either, and
  `text/plain` is CORS-safelisted, so a page on any other local port could POST
  JSON carrying a live board session with no preflight. CORS withholds the reply
  and does nothing about the effect, and the effect included `GrantRole`. Any
  local process can bind a port, which on this machine includes the agents the
  daemon exists to coordinate. The origin is now this server's own.

- **The Touch ID prompt spoke the caller's words.** `human_unlock` placed its
  `note` argument verbatim into the biometric sheet, and any agent may call it,
  so the caller chose the sentence a person read at the moment they decided
  whether to hand over their token. The prompt is now the daemon's, names the
  requesting agent, and says what is being given away; `note` is recorded in the
  result instead.

- **A single approval could perform two effects.** `grant` and `adopt` were
  admitted independently and both executed, while the prompt rendered only the
  grant, so a request could read "make X coordinator?" and move a dormant
  agent's whole mailbox on the same yes. Refused as a combination, and the
  prompt no longer depends on that.

- **A question's answers went to disk in the clear.** `choices` is message
  content and became recipient-scoped state exactly like the body beside it, and
  only the body was sealed. A copied ledger showed the alternatives next to
  ciphertext, and the alternatives are frequently the sensitive half.

### Added

- **Codex is configured over stdio, not HTTP, and that is an identity decision
  rather than a preference.** `dibs mcp-config` printed the url form for Codex,
  Codex took it, and the cost was invisible for months: an HTTP client has no
  per-session process, so nothing holds the agent's nonce, so every returning
  session registers as a sibling that cannot read its predecessor's mail. Codex
  has supported stdio all along; in a real config almost every other server uses
  it, and Dibs was the odd one out because this command said to be. The url form
  is still documented for a client on another machine, where a local bridge is
  not an option and a forked identity is the lesser problem.

- **The stdio bridge keeps the nonce, so a returning agent is the same agent.**
  This is the product's central failure, and an agent building on Dibs said it
  better than the source did: "An agent cannot be relied on to carry a secret
  across a context boundary." A persistent agent is told to keep a nonce,
  because it is the only credential that survives a restart. Then its context
  ends, which is the event the nonce exists for, and the nonce ends with it. The
  next session registers under the same name with a fresh one, becomes a
  SIBLING, and cannot read a word of its predecessor's mail.

  Measured on a real board: nine rows for five roles. `dibs-maintainer`, `-2`,
  `-3`. `codex-root`, `-2`. `codex-1`, `-2`. `web-lead`, `-2`. One created while
  fixing the others; one agent reproduced it twice in a day, the second time
  having been warned by the very response that created the first.

  The bridge is the only participant with a memory that spans sessions, so it
  keeps it: per project root and per name, stored 0600 under the data directory
  rather than in a tree somebody might commit. A returning session reattaches
  before the model has done anything. What the agent supplies still wins, and is
  remembered too. This does not help HTTP clients, which have no bridge; that
  gap is named rather than papered over.

- **An agent is told when its request is answered, and what changed.** Approval
  is the most consequential thing that can happen to an agent that asked for
  something: it may now do what it could not a moment ago, and short of
  re-reading a message it had already sent, nothing told it. The notice names
  the effect rather than the disposition, so an approved `grant` says "you now
  hold the coordinator role" and an approved `adopt` says whose mail is now
  yours. A denial says not to retry the same ask without new reasoning.

- **The agents already in a space are told when somebody joins it.** This
  notified the joiner and nobody else, which answers "what did I just join" and
  leaves "who turned up in my space" to whoever re-reads the board. Somebody
  arriving in the work you are doing is a change you did not cause and could not
  infer, which is what a notice is for.

- **`[wake] notices_wake`**, for the cost of the above. Extending a turn revives
  a thread that may be long and whose prompt cache is cold, and on a fleet of
  idle sessions that is a real bill to pay for "somebody joined your space".

  On by default, and the first version had it off, which four end-to-end checks
  caught immediately: "an agent is told what happened to it" is a guarantee this
  project already makes, and one that holds only for operators who found a
  config file is not a guarantee. The zero value is therefore the documented
  behaviour, and the field is stored inverted so that stays true for an engine
  nobody configured. Turning it off costs latency, not delivery: notices still
  queue, still ride along on any other wake, and still arrive in full at the
  agent's own `check_in`.

- **Mail reaches an agent whose harness cannot push anything.** Every
  authenticated mutating result now carries a `waiting` line when the caller has
  unread mail, an unacknowledged announcement, or a pending agent update: counts
  and the corrective call, never content. Push delivery through lifecycle hooks
  is conditional on four things (the harness having hooks, the plugin being
  installed, it having loaded before the session started, and the agent having
  registered with the session id the hook quotes) and each of them is a real way
  to end up believing mail arrives by itself. Measured on a live board: an agent
  registered out of band sat on unread mail while `dibs doctor` reported hooks
  resolving perfectly, because they were, for everybody else. A tool result is
  the one channel that always exists and cannot be misrouted, because it returns
  down the connection the caller authenticated on.

- **Registering with no session id says so, and says what it costs.** That
  registration used to draw the "no lifecycle hook has reached this daemon"
  text, which sends the agent to audit a plugin that is very likely fine. No
  hook can resolve an agent that has no session id, however well the plugin is
  installed, so the result now names that and the way back (re-register with the
  same name and nonce) instead.

- **A human running a terminal harness can see what Dibs is doing.** The wake
  hook returns `systemMessage` alongside the model-facing digest: one line, to
  the person rather than the model, costing the agent no context. The board is
  an MCP Apps panel and terminal hosts do not render those, so everything Dibs
  did for those operators happened in silence.

- **`UserPromptSubmit` joins the Claude Code wake hooks**, so mail that arrived
  while the agent was idle lands when the human's next turn starts rather than
  waiting out the turn after it.

- **`update` revises what an agent says about itself**: `name`, `description`,
  and the self-reported half of its identity (`title`, `branch`, `model`,
  `provider`, `effort`, `surface`). An agent picks its name in its first seconds
  and boards fill with `agent`, `claude-1` and `worker`: nine rows that are all
  synonyms for "an agent". The id never changes, because it is the address every
  message, claim and membership keys on, and a name another live agent holds is
  refused (`E_NAME_TAKEN`) rather than suffixed, since two live agents sharing a
  name redirects mail between them. `harness` and `version` stay unsettable:
  the client states those, which is the one part of the board that is not a
  model's word for itself. `register` now also answers a placeholder name at the
  moment it is chosen.

- **A question to the human is answerable from the notification.** It used to
  raise a banner, which is a notification that the board has something on it:
  the person still had to go and open the board, and the asking agent waited out
  its deadline while they decided whether to. Requests have had approve/deny
  buttons since notifications landed; questions had nothing.

  `send` now takes `choices` (up to four). Up to three become the buttons
  themselves, so answering is one press with nothing to type and no window to
  find. A fourth does not fit on a notification, so that case, and a question
  with no choices at all, offers `Later` and an opt-in, which then opens a list
  or a text box.

  Nothing takes the screen until the person has pressed something asking it to,
  and that ordering is the design rather than a detail: raising a text box on
  arrival is fewer steps and is a coordination service deciding that its
  optional question outranks whatever they were doing. `planAnswer` is split
  from the osascript that performs it so the rule is testable, because the part
  with a rule in it is the part a rewrite loses.

  The choices are on the MESSAGE and therefore in the ledger, not a property of
  the notification that raised them: a question replayed without its options is
  a different question. Agents see them in `inbox` like any other field, so an
  enumerated answer space is worth stating whoever is receiving it.

- **Touch ID opens the web board, and no password is needed where it works.**
  `dibs doctor` warned "no admin password set, so the web board cannot be
  opened" on every Mac with a working sensor, and the remedy it named was to
  invent and store a credential in order to be trusted LESS. The presence
  machinery had existed since the panel's `human_unlock`, whose own comment says
  it: a password proves possession of a secret an agent could in principle have
  been handed, while a fingerprint proves somebody is sitting there. The gate
  went on demanding the weaker of the two, and `guard.go` had carried a note
  saying "presence upgrade can later replace the password" the whole time.

  The check runs in the DAEMON, never in the client. A caller that reported "I
  verified presence" would be asserting it, and every agent on the machine holds
  the local secret needed to make that assertion. Presence is also still the
  SECOND factor: same-user AND a person who consented just now.

  The password stays, because presence is genuinely absent on Linux, on Macs
  without the sensor, and in a headless session. Declined and unavailable are
  answered differently by the daemon, because they mean different things to
  anything calling `/bootstrap`. And a `dibs web` whose stdin is not a terminal
  never raises a sheet at all, since a script piping a password is telling you
  it cannot reach a sensor. `dibs web --password` forces it.

  `dibs web` falls back to the password on EVERY presence failure, not just on
  an unavailable sensor. The tempting rule is to stop on a decline, so that
  somebody who just said no is not immediately asked for a credential; it is
  right about a real decline and wrong about everything it cannot be told apart
  from. The helper reports "declined" for a cancel, a failed match and its own
  timeout alike, so a sheet that never reached the screen looks exactly like a
  refusal. That is not hypothetical: on the machine this was written on,
  `evaluatePolicy` accepted the policy, reported Touch ID present and enrolled,
  and never called back at all. Stopping there would leave the operator with
  ninety seconds of nothing and no way in, on the one command whose job is to
  let them in.

- **Dibs reports its own faults to the coordinator, and asks for a patch.** A
  service that notices something wrong with itself and writes it to a log has
  told nobody: the operator is not tailing it, which is the same premise this
  whole product rests on. Faults now go to whoever holds the coordinator role,
  or to the human when nobody does, as ordinary mail: same envelope, same
  mailbox, same wake path, visible on the board and replayable. A private
  channel for system messages would be a second delivery mechanism to keep
  working and the first thing to rot.

  Every report ends on something the reader can do, and what that is depends on
  whose fault it is. Configuration gets the remedy precisely, because it is
  theirs to apply, and still names the repository in case the remedy does not
  work. A defect gets the repository, the failing path, and an invitation to
  FIX it: the agent reading it is holding a reproducible fault in a Go codebase
  with the path named, which is a better starting point than whoever reads the
  issue later will have. Asking for a bug report gets a bug report; asking for a
  patch sometimes gets a patch, and a contributor.

  Dibs speaks as an ordinary participant to do it, minted on first fault rather
  than at startup, so a board where nothing has gone wrong carries no row for
  the thing that reports what goes wrong. A report without a remedy is refused
  at the door, because that is an alarm and this is the file that exists to not
  produce those. One report per kind per run, so a fault that recurs every sweep
  does not become a message every sweep.

- **An agent can ask for its old identity back, and approving moves the mail.**
  `send(to: "coordinator", type: "request", adopt: "<the abandoned id>")`. The
  approver's yes performs the adoption; there is nothing left to run.

  This is the fix for the duplicates a real board accumulates. `dibs-maintainer`,
  `-2` and `-3`; `codex-root` and `-2`; `codex-1` and `-2`: every one is an agent
  that came back, could not prove it was itself, and started again beside its own
  unread mail. The recovery path already existed and needed the human at the
  machine or a coordinator, which is an authority the returning agent does not
  have and cannot get from where it is standing. So the honest reading of the
  warning was "your mail is gone", and the only reachable action was to carry on
  as a sibling. The warning now ends on something the agent can do unaided.

  Approving one still needs exactly the authority that performing one needs,
  recorded at ingress like every other verdict, so replay applies the decision
  that was made rather than re-deciding it against a board whose roles have
  moved on.

- **`to: "coordinator"` addresses whoever holds the role.** An agent asking for
  its identity back should not have to work out which of sixteen rows is the
  coordinator today, or notice when it changes hands. Resolved at ingress, so
  the ledger records the agent it actually went to: a message addressed to a
  role and replayed after the role moved would otherwise be delivered to
  somebody it was never sent to. A live holder wins over a dormant one, because
  addressing a role has to reach somebody who can answer, and the result is
  stable across calls: map iteration is random, and "the first one found" would
  scatter a role's mail across however many hold it.

- **An agent asks for a role and your Approve grants it.** `send(to: <the human
  row>, type: "request", grant: "coordinator")` raises a notification whose
  button says what pressing it does, and pressing it promotes them. There is no
  second step.

  Before this, the loop stopped one move short of useful: the agent asked, the
  notification appeared, the human pressed Approve, and then had to open a
  terminal and type `dibs admin coordinator <agent>`. Two steps for one
  decision, and the second is where it died: the approval sat answered on the
  board while the agent stayed unable to do the thing it had just been told it
  could. A `request` is free prose, so Dibs could not know that approving one
  meant granting anything; `grant` is the typed field that makes the yes
  actionable.

  Three rules keep it from being self-promotion with extra steps. Only the human
  may receive one, checked in the engine because `core` does not know humans
  exist and that ignorance is what keeps it a pure state machine: without it two
  agents could promote each other by approving in turn. Only a `request` may
  carry one, because it is the only type with an approve. And **admin is refused
  outright**: coordinator is breadth (broadcast, force_release) and cannot read
  anybody's mail, while admin is the god view including every agent's decrypted
  mail, which is not something to hand over on a notification tapped between two
  others.

  The notification's title is composed by the DAEMON from the typed field, not
  from the sender's prose. It is the only line stating the effect of pressing
  Approve, so a request whose body reads "just need to check something" cannot
  carry `grant: coordinator` past somebody who never saw the word.

- **`retitle_space`**, so a topic can be redacted without destroying the space.

- A hygiene guard against the wreckage a find-and-replace leaves when one word
  used to mean two things, which is how the last one went.

- **`task release VERSION=x.y.z`**, because the release pipeline had exactly one
  hand-edited step left and it drifted twice. AGENTS.md says of that pipeline
  that "no source is updated by hand: if the three ever disagree, that is a bug
  in the pipeline, not a chore"; claiming the changelog's Unreleased section and
  stamping four manifests was still a chore. It stamps and stops, because
  tagging publishes signed artifacts, moves the Homebrew cask and writes to the
  MCP registry, and that stays the owner's to perform. `internal/release` is the
  one declaration of what carries a version, used by both the thing that writes
  it and the thing that checks it, so a manifest cannot be stamped and unchecked
  or checked and unstamped.

- **`dibs upgrade`**: one command to move a running fleet onto a new build.
  R12 settled the client half of this (the bridge waits out the restart window
  and re-sends only requests that provably never arrived); this is the operator
  half. It runs the new binary against the ledger first, through a new
  `dibd -check` that replays without serving and is safe against a board another
  daemon is holding, and stops nothing unless that passes: a binary that cannot
  fold the ledger the old one wrote is otherwise discovered only after the
  daemon that could serve the board is gone. Then it repoints a service unit
  pinning the wrong daemon, restarts through the service manager where there is
  one, restores the address the daemon was bound to (a fleet spanning machines
  is not on loopback, and coming back there would take every remote agent off
  the board while every local check passed), and waits for the board before
  reporting the serial and agent count it returned with. Anything that fails
  between the stop and the start restarts the daemon on the build it was already
  running. `--adopt-dir` also renames a data directory an older version named,
  in the one order that works. `dibs doctor` now names this command instead of
  handing back shell steps whose order was load-bearing and unstated.

  Two rules in it were paid for on a live board, by the first real run. A
  service unit is RELOADED before it is restarted, always: launchd reads a plist
  at load time and holds the parsed definition, so rewriting the file changes
  nothing it knows and `kickstart` exits 0 having scheduled the old program.
  Unconditionally, because a plist edited by hand or by an earlier failed run
  drifts the same way and presents identically. And a restart is not believed
  until the BOARD answers: marking it done when the start call returned meant
  the recovery could not fire on the one failure that matters, and the fleet
  stayed down while the command reported the failure and exited.

### Fixed

- **Pinning an agent's identity broke every call that agent then made.** The
  transport nonce counted as a supplied argument for every tool, while the
  unknown-argument check exempts only `token`, so an agent whose harness pinned
  its identity was refused `check_in` with a schema complaint about an argument
  it never sent. That is the call every agent must make at the start of every
  activation, so configuring the feature broke the agent that configured it.

- **`dibd -check` repaired the board it was asked to inspect.** It replays a
  board to answer whether this build could take over from the daemon now
  running, and `dibs upgrade` runs it before the cutover, which means while the
  old daemon may still be serving. It opened the ledger read-write, so it
  created files in a directory it does not own and truncated a torn final
  line, and a torn final line is what a running writer looks like from outside.
  Measured with a 17-byte partial record: the command reported success and left
  the ledger at 0 bytes. It now loads rather than creates, opens read-only, and
  reports a torn tail instead of repairing it.

- **The bridge's memory outranked the operator's configuration.** A remembered
  nonce was injected into the arguments before the pinned identity was attached
  as a header, and the daemon prefers a stated nonce over a transport one, so
  `DIBS_AGENT_NONCE` was silently overruled and the session reattached to
  whichever agent the bridge happened to remember.

- **One checkout was several identities.** The bridge's nonce store said it
  keyed by repository root and nothing supplied one, so every session was keyed
  by the exact subdirectory it started in: the same role launched from the
  repository root and from a subdirectory became two agents. It now walks up for
  the checkout.

- **The nonce store could lose every identity on the machine.** An unlocked
  read-modify-truncate-write of a file every bridge shares, and a decode failure
  reads as an empty map, so one interrupted write discarded the lot. Each lost
  entry is a fresh nonce and therefore a sibling. Serialised and written
  atomically now.

- **A branch-only `update` erased the agent's description.** The tool invites
  branch-only and title-only calls, and an omitted field and an explicitly
  emptied one were the same value by the time the op was built.

- **Approving a request from an agent that had retired still performed it.** The
  role was granted to an agent that cannot act, and an adopted mailbox was moved
  into one whose token has been blanked and which cannot resume, so a
  coordinator approving a rescue moved the rescued mail somewhere unreadable and
  was told it worked.

- **The board could be framed.** Cookies are host-scoped and not port-scoped, so
  a page on another local port could frame the authenticated board with its
  session attached and drive it through its own script, whose origin is this
  daemon's. `frame-ancestors 'none'` and `X-Frame-Options: DENY` now.

- **A fault found before anybody was on the board was never retried.** The
  startup reachability check runs before any agent has registered, so there was
  nobody to tell, and nothing tried again once somebody arrived.

- **Two shipped plugins read the daemon's secret from a directory it left
  behind, and failed silently when it was not there.** `plugins/opencode` and
  `plugins/pi` defaulted `DIBS_DIR` to `~/.agents`. The daemon has used
  `~/.dibs` since 0.0.3 and reads the old name only as a legacy directory, so on
  any install made since, both plugins looked for `local.secret` where nothing
  was. Neither says so: the read is wrapped, returns null, and every hook in
  both files returns null on a null key. The agent registered no delivery hook,
  nothing was logged, and mail simply never arrived, which is indistinguishable
  from a quiet board. Both now resolve the way the daemon resolves, preferring
  `~/.dibs` and falling back to the legacy directory only when that is the one
  that exists.

- **A missing SPACE said "no agent" and sent the agent to look at the roster.**
  Nine call sites keyed on a space id answered `E_NO_AGENT`, while `E_NO_SPACE`
  existed alongside them and was used in six others. The hints were the
  expensive half: `join_space` against a space nobody had opened answered
  `no agent ghost`, hint `open_space it, or list agents first`. Dibs holds that
  every error carries a hint naming the corrective call; this named the wrong
  noun and then the wrong call. `join`, `leave`, `post`, `watch`, `retitle`,
  `close`, `merge` and both watch paths now answer `E_NO_SPACE`. `E_NO_SPACE`
  was also missing from SPEC §12's list of codes, before any of this.

- **Three hints told an agent to "read the agent with `read_space`".** The 0.0.3
  rename left `core.AgentMatch.Agent` holding a space id, so everything written
  about that field came out saying agent when it meant space. The field is now
  `Space`, which is what the two rounds of repair above and below both trace
  back to.

- **The hermes plugin told the reader to install a binary that has never
  existed** (`hermes mcp add agents --command "$(which agents)"`), six lines
  above its own output showing `command: .../bin/dibs`. It and `plugins/pi`
  also advertised a stale count of 25 against a server publishing 44 of them: no
  plugin README was read by the tool-count guard, and hermes stated the number
  in a shape the guard cannot match, so both halves had to be wrong for it to
  survive. The guard now reads every plugin document.

- **The README, the tutorial and SPEC-CHANNELS described matching as something
  you switch on.** It has indexed every repository the fleet works in since
  0.0.5, and measured its own notify threshold since. The README's section was
  headed "Turning it on", SPEC-CHANNELS promised in its fourth line that spaces
  are inert until `-match-repo` is passed, and the tutorial told the reader to
  restart the daemon to set a flag that no longer does anything, including a
  `dibs doctor` transcript showing a check the command has not printed since.

- **A full board refused registrations while naming the wrong ceiling.** One
  static error read "maximum number of agents reached" for both caps, so an
  operator hitting the PERSISTENT limit checked `max_agents`, found the board a
  quarter full, and had been told something true and useless. Found by running
  the thing: a board holding 16 agents of a possible 64 refused a new one,
  because all 16 were persistent and that ceiling is 16. Each cap now names
  itself, its number, and its own remedy, which differ: a full board wants
  finished agents signed off, while a full persistent board usually means
  siblings accumulated and wants them reclaimed.

- **`max_persistent_agents` and `max_agents` are settable**, which the hint
  above tells people to do and which was not previously possible. A persistent
  ceiling above the total is refused rather than accepted, because the lower one
  binds and the setting would otherwise read as applied while doing nothing.

- **One unreadable directory switched work-overlap matching off for the whole
  board.** An agent registering from a tree macOS will not let the daemon read
  set the GLOBAL phase to `off`, replacing a working index for every other
  repository with "matching is off" and a hint pointing at one directory. A
  fleet lost the feature for a day to it. Reported by an agent that had lost it
  and traced the cause correctly.

  A tree that cannot be read is now named in `unreadable` and belongs to the
  agent that registered from it; the phase stays whatever the rest of the board
  earned, and only goes `off` when nothing at all is indexed, because then the
  two statements coincide.

  The same shape two lines above it: every registration set the phase to
  `indexing`, so a fleet that had been matching for an hour reported itself as
  starting up whenever a new agent joined, and anything declaring in that window
  was told matching was not ready.

- **`task install` proves the signature is stable instead of asserting it.**
  macOS ties a Files-and-Folders grant to a program's code-directory hash, so a
  hash that moves silently revokes the permission and the operator is asked
  again by a dialog that explains none of it. The stable identity fixed that;
  nothing checked it was still true. It had been verified once, by hand, by
  somebody who then promised it would not recur, which is precisely the kind of
  claim this repository does not accept anywhere else. `tools/signstable`
  records what each install signed and fails the next one if it changed, naming
  both hashes and the two commands that diagnose it. Ad-hoc builds are exempt
  and say so, because their hash changes by design and failing every install on
  a machine with no identity would teach everyone to ignore the check.

- **The panel no longer pushes anything into model context, and the daemon no
  longer gives it the material to.** `maybeShareMailWithAgent` called
  `ui/update-model-context` with the body of every unread message. It existed as
  the only push a host without lifecycle hooks could offer, and it did not do
  that job: by the Apps contract it does not start a turn, which the code said
  itself, so the mail surfaced when the HUMAN next typed. That is the operator
  acting as the transport, which is the failure this product exists to remove,
  arriving through the one door nobody was watching. What it did in practice was
  put every unread message, in full, into their composer.

  Removed, not trimmed. Waking an agent belongs to the lifecycle hooks and to
  the `waiting` line on every authenticated result; a panel shows a person their
  board.

  Removing it was not enough on its own. MCP Apps templates are cached by the
  host against their `ui://` URI, so a session that loaded the panel before the
  fix keeps running the old JavaScript and keeps leaking, with nothing the
  server can do about it. So the bodies also stop leaving the daemon: the panel
  payload carries serials, senders, types and states, and the text travels only
  on the panel's own authenticated bridge, where a host has no claim to it. A
  cached panel cannot share what it was never given.

  Four e2e checks asserted the old behaviour, carefully: that the push was
  framed as data rather than instruction, that it never claimed to wake anyone,
  and that concurrent pushes coalesced. All true, all of a feature that should
  not have existed. They now assert zero pushes, with the reasoning kept.

- **A guard against the leak that keeps coming back through a new door.** The
  same defect has now appeared three times in three channels: the wake digest
  listing each message with its text, `dibs://inbox` returning the whole
  mailbox, and both reported by the operator watching their own prompt box fill
  with mail addressed to an agent. `TestNoWakeSurfaceLeaksAMessageBody` asserts
  the rule over every surface a host may attach to a human's turn, as a property
  rather than three examples, because the failure keeps arriving somewhere
  nobody thought to look. The rule is one sentence: these say who is waiting and
  what kind, never what was said, and the content is fetched with a
  token-authenticated call.

- **A fault report goes to somebody who can READ it.** It asked
  `CoordinatorID`, which answers "who holds the role". On a board whose only
  coordinator is dormant, that is an agent which may never come back, so the
  report was filed correctly into a mailbox nobody opens: this feature's own
  failure mode, with an extra step. Measured here, where the standing
  coordinator had been dormant for a day while the operator was at the keyboard
  throughout, and Dibs' warning that it could not reach them by notification
  went to the one row guaranteed not to see it. A live coordinator first, then
  the human, then a dormant coordinator as a last resort.

- **A request to a PERSON gets a person's deadline.** The default was ten
  minutes for every recipient. That is right for an agent: it is in a loop, it
  answers in seconds, and a stale question should expire rather than linger. A
  human is not in a loop, which is the premise this entire product rests on, and
  this one default contradicted it.

  Measured here, on the request that would have made the maintainer a
  coordinator: sent, delivered, never seen because a Focus mode swallowed the
  notification, and expired thirty minutes later as
  `expired_recipient_dormant` while the operator was away from the machine. The
  feature worked exactly as built. The clock was set for somebody else.

  A day now, when the recipient is the human and the sender named no deadline.
  Well inside the seven days a persistent recipient already allowed, so a sender
  who wants longer can ask, and an explicit deadline still wins in both
  directions. Agent-to-agent mail keeps the short default, because "every
  deadline is a day" would leave stale questions on every board for a day apiece.

- **A sibling shares the NAME, never the ROLE, and the resend advice did not say
  so.** Mail to a dormant agent returns "X is LIVE under the same name and is
  almost certainly who you meant. Resend to X." That is right when you meant a
  peer doing a job, and wrong when you meant an authority.

  Found in the wild within an hour of the reclaim path shipping, by the first
  agent to use it in earnest. It needed a coordinator to approve an adoption,
  addressed the agent holding that role, found it dormant, followed this advice
  to the live sibling, and asked an agent with no role at all. It opened by
  telling that agent it held the coordinator role, because the note had said so
  in everything but the word. Neither end could see that the authority had been
  dropped in transit. The advice now says when the sibling lacks the role, what
  that costs (approving an adoption or a grant, force_release, evict), and names
  the human as the reachable alternative.

- **Confidentiality: `dibs://inbox` published message bodies to whoever the host
  decided.** An MCP resource is APPLICATION-controlled: the host chooses what to
  do with one, and attaching it to the user's next turn is an ordinary thing for
  a host to do. This resource returned the whole mailbox, bodies included, so
  one agent's private mail was rendered into its operator's prompt box, prefixed
  with the resource's own name. Reported that way: "it starts with `inbox:` and
  a message from another agent."

  Two failures in one change: mail reaching a reader it was not addressed to,
  and the human put back in the loop as a relay. The resource now carries the
  SIGNAL and the tool carries the content: counts, senders, types and serials,
  plus the call that reads them. That is the rule Dibs already applies to the
  human's notification and to the `waiting` line, "counts and senders only,
  never content", and this was the one place it was not applied. The
  subscription still says "there is new mail", which is all a wake needs.

- **Security: a role grant could be approved by an agent, not only by the
  human.** The engine checked that a request carrying `grant` was ADDRESSED to
  the human when it was sent, and approval trusted the recipient from then on.
  Adoption rewrites the recipient of every message in a mailbox, and a
  coordinator may adopt, so: an agent asks the human for coordinator, the
  human's row goes dormant (which needs no arranging, since silence is a
  person's whole liveness model), a coordinator adopts that mailbox, inherits
  the pending request, and approves it. No human anywhere in the story. Closed
  twice: the human's mailbox is not adoptable by anybody else, which the
  coordinator boundary already implied and did not enforce, and approving a
  grant re-checks the human at APPROVAL time.

- **`op_id` dedup ignored the fields that carry the effect** (`grant`, `adopt`,
  `choices`), so a retry reusing an op_id with a changed `grant` returned
  `{"ok": true, "deduplicated": true}` over a message that granted nothing.

- **Three new validation rules sat in `Apply` instead of `Admit`**, which makes
  them retroactive replay rules: the day the accepted roles change is the day
  the daemon refuses to boot on its own history. This is the mistake AGENTS.md
  names first. The tests reinforced it by asserting rejection at `Apply`.

- **Three new engine paths read core state off the single-writer loop**,
  including one called from an HTTP handler. `e.human.mu` guards the cached
  human fields and nothing in core, so those were data races and candidates for
  a concurrent map read-and-write panic.

- **A notification that could not be SHOWN was reported as one nobody
  answered**, so a question the operator could never see was indistinguishable
  from one they ignored, and the asker waited out its deadline.

- **A fault was marked reported before it was delivered**, on four paths. The
  startup reachability check is exactly when that bites, since it runs before
  anybody has registered.

- **The notification says WHO is asking.** "Dibs · make asker coordinator?" is
  not enough to approve a privilege change on: a name is self-chosen and often
  three variations of one word. It leads with the daemon-assigned id and where
  the agent works, placed ABOVE the sender's own text.

- **Tests can no longer notify a real person.** `go test ./...` put alerts on
  the operator's screen carrying fixture text, with buttons that answered a
  process which had already exited, and the product was then reported broken on
  the evidence of its own test suite.

- **`core.Message`'s json tags and two op-kind strings were frozen by nothing**,
  though a message is state and state is a fold over the ledger.

- **Mail no longer rides on the human's prompt.** `UserPromptSubmit` fires when
  a PERSON types, and its `additionalContext` is attached to their message, so
  delivering a wake digest there made the operator the transport: an agent
  learned that a peer was waiting when, and only when, its human happened to say
  something. That is the failure Dibs exists to remove, shipped as a feature.
  Worse, that path had no freshness throttle (only `Stop` did), so the same
  unread message was attached to every prompt they sent until somebody read it.
  Reported from a live fleet: "it's putting it on my plate to take an action for
  them to notice, agents should be notified directly."

  `Stop` still pushes and keeps its loop guard, `SessionStart` still tells a new
  session what is waiting, and the `waiting` line still reaches an agent that
  neither can. None of those need a person to type.

- **The Codex plugin says how to wire its wake path, and stops contradicting
  itself about whether one exists.** Codex reads hooks from `~/.codex/hooks.json`,
  a different file from `config.toml`; nothing said so, so the MCP server got
  configured and the hooks file never did. The README meanwhile carried two
  sections: one announcing that the `mcp_tool` hook variant had arrived, and,
  directly below it, one stating as current fact that "there is no `mcp_tool`
  and no `http` variant". Both were checked in, and the shipped `hooks.json` was
  written against the true one, so a reader had no way to tell which described
  the product. This is the drift class this repository is most expensive at, in
  the file whose whole job is to tell somebody how to install the thing.

- **Pressing "Answer" on a notification now has somewhere to put the answer.**
  The notification comes from Dibs' application bundle, because only that API
  carries buttons and only a bundle carries an identity. The text box that
  opened when somebody pressed Answer did not: it was an osascript
  `display dialog`, and a background LaunchAgent has no foreground application
  for a dialog to belong to. So the press dismissed the notification, osascript
  ran, and nothing appeared. Reported exactly that way: "when I clicked answer
  it just went away, there was nowhere to put an answer." Both halves of
  answering now come from the bundle, as a native alert that activates, because
  stealing focus is correct one gesture after somebody asked for it.

- **Dibs says when it cannot reach you, instead of reporting success into
  silence.** A coordinator request was posted, macOS accepted it, an active
  Focus mode swallowed the banner, and every layer reported success: the board
  said "delivered", the agent waited out its deadline, and the operator asked
  why they had seen nothing. The notifier's "authorisation refused" exit was
  also being read as "the human did not answer", so a silenced Dibs and an
  ignored one were the same value. `dibs doctor` now reports whether a
  notification would actually be SEEN, naming the cause: a Focus mode, a
  revoked grant, permission never asked for, or every alert style switched off.
  A question or request also asks to break through Focus as Time Sensitive,
  since buttons are the tell that somebody is blocked on the answer.

- **`signcheck` stopped refusing installs it no longer needs to refuse.** It
  blocked an ad-hoc install whenever a usable identity sat unused in the
  keychain, which was right when using one meant naming it in an environment
  variable. `tools/signid` resolves it by name now, so that situation cannot
  arise, and the refusal turned a solved problem into a blocked install. Two
  tools that decide the same thing have to decide it the same way; this one was
  left behind by the other, an hour after the other was written.

- **`task install` finds Dibs' own signing identity by name, so the macOS
  permission prompt stops repeating.** macOS keys a Files-and-Folders grant to a
  program's code signature; the Go toolchain signs ad-hoc, so every rebuild is a
  different program and the grant stops applying. `tools/signcheck` has warned
  about this and named the remedy since it was written, but the remedy only
  worked if the operator then passed `DIBS_CODESIGN_IDENTITY` on every install
  forever, and one install that forgot silently went back to ad-hoc and revoked
  the grant again. A fix conditional on remembering is not one.

  `tools/signid` resolves it: the environment variable if set, else Dibs' own
  identity if it is actually in the keychain, else ad-hoc. Create the
  certificate once and every later install keeps the grant, with nothing to set.

  Measured on the machine this was written on: nine installs in one session,
  nine permission prompts, and the operator asking why. The warning printed all
  nine times, into output nobody was reading.

  The install also says which of the two happened, in ONE line. It briefly said
  both: `status:` is a task-level field rather than a per-command one, so two
  guarded commands both ran and consecutive lines claimed the grant would and
  would not survive.

- **The human's row on the board reported `process gone` after every daemon
  restart.** The human registers as a participant so agents have somewhere to
  address a request, and it recorded `os.Getpid()`: the daemon's own pid, which
  is the one pid guaranteed to be alive at the moment it is written and gone by
  the next start. The liveness sweep then found a dead process and honestly
  reported what it saw. A person is not a process, so `Op.NoProcess` now says
  that a participant HAS none, which is different from omitting a pid and is
  why it needed a field: omitting one means "unchanged", so nothing could ever
  clear a pid recorded earlier.

  Boards that already ran the old build heal themselves at the next daemon
  start, because nothing else would: the human's registration is rewritten only
  when they ACT, so an operator who reads their board and closes it would be
  told `process gone` about themselves indefinitely. The repair is an `update`
  rather than a re-registration, which is a distinction that cost a debugging
  round: `register` short-circuits a same-nonce retry inside one TTL and returns
  the original result WITHOUT applying the op, which is right for a retried
  registration and silently a no-op for a correction spelled as one. It fires
  only on a board that already has a human, and only while a pid is recorded, so
  it repairs rather than recruits and does not append a record a day to say
  nothing.

- **The wire-format guard was checking a third of the wire format.** Renaming an
  op's json tag is silent data loss here (`lane_kind` → `agent_kind` demoted
  every persistent agent on upgrade, in every release to v0.0.4), and
  `TestLedgerFieldNamesAreFrozen` exists to catch exactly that. It fingerprinted
  the tags a hand-written fixture happened to populate, so 17 tags, including
  every field of `update` and `adopt_agent`, were free to be renamed by the next
  sweep with the guard passing. It now reflects over `core.Op` itself, so a
  field is guarded because it exists rather than because somebody remembered to
  exercise it.

- **The stdio bridge upgrades itself in place, so a fix no longer waits for
  every harness on the machine to restart.** A bridge is spawned once per
  session and held for its lifetime, so installing a new `dibs` did nothing to
  the one already running: an agent kept talking to the build it started with,
  for days. Every bridge fix was therefore gated on restarting every harness,
  which is precisely the ceremony R12 refuses to charge for a daemon upgrade and
  has no better claim to here. The session-repair above rides in the bridge, so
  the agent whose mail was going undelivered would have kept not receiving it
  until its session ended.

  `syscall.Exec` is the whole mechanism, and it works because of what exec does
  not touch: the process keeps its pid and its file descriptors, so stdin and
  stdout stay the pipes the harness is holding, and from the harness's side
  nothing happened. It fires only between a reply and the next request, only
  when this process's own read buffer is empty (buffered bytes live in memory,
  not in the pipe, so an exec would discard a batched request), and it carries
  the handshake identity and every open subscription across in the environment.
  Subscriptions are re-issued as the caller's own request, never reconstructed,
  which is the rule followStream already follows across a daemon restart.

- **A bridge can no longer outlive its harness, by construction.** One bridge
  exists per session, so a bridge that fails to exit is one orphan per session,
  each holding a stream open against the daemon forever. EOF on stdin cannot
  prevent that, because the bridge sees EOF only when the LAST holder of the
  pipe's write end closes it, and a harness that also spawns shells hands each
  one the same descriptors: a Claude Code killed while a Bash tool is running
  leaves the write end open and the bridge waiting on a pipe nobody will write
  to again. The lifetime is now bound to the PROCESS by the kernel, on both
  supported platforms: `PR_SET_PDEATHSIG` on Linux, which holds even if the
  bridge is wedged, and kqueue `EVFILT_PROC`/`NOTE_EXIT` on macOS. Both re-check
  `getppid` afterwards, because the parent can die in the window before
  registering, and a reparented process is an orphan by definition.

  Those paths exit rather than unwind, which is the part that makes the
  guarantee real: cancelling a context does not interrupt a blocking read on
  stdin, so a bridge parked in that read never looks at the cancellation again.
  Measured: with a sibling holding the write end, a bridge outlived a SIGKILLed
  harness indefinitely with its context already cancelled. Shutdown also waits a
  bounded time for its stream goroutines and then goes anyway, because the
  guarantee has to be that the process exits, not that it exits if its
  goroutines cooperate. SIGTERM and SIGINT end it cleanly, flushing first.

- **A bridge holding a subscription never exited when its harness closed
  stdin.** `followStream` re-issues the listen every time the stream ends, which
  is what keeps a subscription alive across a daemon restart and, with no way to
  stop it, also kept it alive across the session going away: the bridge then
  waited forever on a goroutine that reconnected forever. Found by closing stdin
  on a bridge that had one.

- **`adopt_agent`, for a mailbox nobody can log back into.** An agent that
  registered with neither a nonce nor a session id can never be reattached, and
  its mailbox keeps accepting mail no one can read. Found on this project's own
  board, holding six unreachable messages. It moves that mail onto a live agent;
  the source record and its history stay, because the ledger refers to them, and
  roles do not move with it. Authorised outside the fold, by the human proven
  present at the machine or somebody they promoted, and the verdict is recorded
  in the op the way a coordinator claim already is: taking another agent's mail
  is otherwise the one thing Dibs must never allow, so there is no
  agent-to-agent version.

- **docs/CONFIGURATION.md**: every setting the daemon accepts, in one place,
  with what happens if you leave it alone. Twenty-four keys were spread across
  five documents with no reference anywhere, and an unknown key stops the daemon
  rather than being ignored, so a setting somebody reads about and cannot use is
  not a small slip: it is a daemon that will not start, blamed on the manual
  that suggested it. A guard checks both directions, reading the struct tags
  rather than a list somebody maintains.

- **`man 8 dibd`.** The daemon had no manual. It is what an operator installs as
  a service, points at a listen address and configures with a file, while the
  CLI, which is discoverable by typing `dibs help`, has had a page since it had
  verbs. Generated from the daemon's own flags, which are now declared once for
  both parsing and documentation, and both pages are checked with `mandoc
  -Tlint` in CI. The CLI's page also stopped calling itself `agents.1`.

- **Agents can reach the human, and the human is notified on the machine.** The
  person is the one participant with no loop: no lifecycle hook fires for them
  and no tool result reaches them, so mail addressed to them waited until they
  next opened the board. The board now marks their row `human: true` so an agent
  can find who to write to, and a message to that row raises a desktop
  notification. A `request` carries **Deny / Later / Approve on the banner
  itself**, and the button pressed comes back as an ordinary response from their
  own agent: the sender cannot tell it came from a notification rather than a
  tool call, which is the point.

- **Dibs.app is built by an install from source, not shipped in the archives.**
  Said here because the two entries around this one promise banner buttons and a
  named sender, and a `brew install` or a downloaded archive gets neither: they
  carry `dibs` and `dibd` only. Notifications still arrive; they arrive without
  the actions and under the poster's borrowed identity.

  It is not an oversight in the release build. macOS remembers notification
  authorisation against the SIGNATURE, and the identity Dibs signs with is
  created on the operator's own machine by `task install`. A bundle signed in CI
  would be ad-hoc, which means a different application to macOS on every build,
  which revokes the grant every time: worse than not shipping one. `task install`
  builds it, signs it with your identity, and the grant then survives rebuilds.

- **Dibs.app**, because a notification carries the identity of whoever posts it.
  A daemon shelling out to `osascript` borrows Script Editor's name and icon, so
  every message from an agent arrived branded "osascript"; there is no flag that
  changes that, the poster's bundle IS the identity. The bundle also buys the
  action buttons: `UNUserNotificationCenter` needs a bundle identifier and is
  the only API that puts them on the banner. The product mark is rendered from
  the same nine numbers as `icon.svg` (a rounded tile, three polylines, a dot)
  rather than parsed, so producing an icon needs no build dependency, and a
  guard keeps the two from diverging. `LSUIElement`, so notifying never bounces
  a Dock icon or steals focus.

- **Mail wakes its recipient when it arrives, once.** An agent hearing about a
  message only when somebody next types at it is not situational awareness, and
  a time-sensitive request sitting unseen because nobody was at the keyboard is
  the failure this product exists to prevent.

  This was got wrong in both directions first. `additionalContext` on a `Stop`
  hook "keeps the conversation going", so every unread message extended a
  finished turn, a plain FYI included, and eight in a row could burn eight
  turns. Narrowing delivery to work somebody was blocked on fixed the symptom
  and broke the point: driving a harness means INSTRUCTING it, and the digest
  already says it is coordination data the agent may act on or decline. The
  agency is in the content, not in withholding delivery until a human appears.

  What deserved the name was nagging, and that is a different fix. Each message
  wakes its recipient once; work somebody is blocked on comes back on the same
  retry an unacknowledged announcement uses, because a question nobody has
  answered is a peer waiting rather than a decision; and `stop_hook_active` is
  honoured, so a wake never continues a turn a wake already continued. That last
  one is a loop guard rather than a preference, and no setting switches it off.

  `[wake] extend_turn_for` is `all` by default, `urgent` for an operator who
  would rather an FYI never cost a turn, `none` for one who wants Dibs strictly
  pull-shaped. The alternatives trade awareness for tokens, which is a trade
  only the person paying should make deliberately.

- **A board-visibility report that could not be reproduced is now guarded
  instead.** `codex-primary` reported that an agent which had just joined was
  absent from their `check_in` snapshot and from the board app. By the time the
  report was read the event ring had rolled over, so the two plausible causes
  (their snapshot preceded the registration; the client was rendering a panel it
  had cached for the session, which `dibs doctor` warns about by name) cannot be
  told apart after the fact. Measured against a live board: a fresh registration
  is present in the very next `check_in` and in the panel payload. That property
  is now a test over both surfaces, because they are separate code paths and the
  report named both.

- **Three reports from agents on this board, acted on.** `k7-a` found that
  work-overlap matching being OFF surfaced only in a `matching_hint` on
  `declare`, attributed to whichever cwd the daemon last failed to read, so it
  read as another agent's misconfiguration; an agent that registers, checks in
  and works without declaring never learned at all. That is the one state where
  silence must not be read as safety: the board renders normally, same-path
  overlap still works, and nothing looks different. It is now on `check_in`, the
  call documented as the atomic checkpoint, phrased as a board state.

  `k7-b` found that closing a solo space was a two-step ending in an error:
  close_space refused because the space had one member, which was them, and
  leave_space then removed the empty space so the close they had been told to
  make failed with `E_NO_AGENT`. The sole member may now close its own space,
  because the rule exists so nobody tidies away somebody ELSE's working context.
  They also called `close_space(reason: …)` when the parameter is `note`, and
  were told only that `reason` is not accepted; the refusal now names the word
  this surface uses when the tool has one. Not an alias: two names for one thing
  in a schema that is an agent's only documentation is worse than a sentence.

- **Touch ID had been dead since the rename.** `humanauth.helperName` said
  `agents-presence` while `task presence` compiled and installed
  `dibs-presence`, so `findHelper` looked for a file that has never existed on
  any machine and every presence check answered `Unavailable`. Three spellings
  were in play, which is how it happened: `lanes-presence` (v1),
  `agents-presence` (the intermediate rename), `dibs-presence` (what ships). The
  one assertion in Dibs that must not be forgeable by software was silently off,
  and the product's own message for it, "this build ships without the presence
  helper", reads as a packaging decision rather than a typo, so nobody looked.
  Nothing could catch it: the Go tests never exec the helper and the Taskfile
  never reads the constant. `TestThePresenceHelperIsTheOneThatGetsBuilt` now
  pins the two together.

- **The hint shown when a name is taken named a call that cannot work.** It said
  to ask a coordinator to `merge_spaces <new> into <old>`, which takes SPACE ids
  where those are AGENT ids: following it fails with `E_NO_SPACE`. Lane-era
  residue, printed at the one moment mail becomes unreachable, which is the
  worst place in the product for a hint to be wrong. Found by following it.

- **A role held by an agent nobody can become now shows on the board and in
  `dibs doctor`.** The coordinator role is what `force_release`, `close_space`
  and clearing another agent's debris all key on, and held by an unreattachable
  agent it is a power the board shows as filled and nobody can use. Not a
  deadlock, which is what it looks like from inside: `dibs admin coordinator
  <agent>` moves it and a `[roles]` block reapplies it on every start. The gap
  was that nothing pointed at either.

- **The wake path could not reach an agent that had no session, and nothing
  said so.** A lifecycle hook names an agent by the session id its harness
  quotes, and `AgentForHook` deliberately refuses the cwd fallback when a
  supplied session matches nothing, because without that refusal any
  unregistered session in a shared directory was handed another agent's private
  mail. Correct, and it means an agent that registered outside its harness's MCP
  connection carries no session and can never be woken, however well the plugin
  is installed. The stdio bridge sent the session id on `register` alone, which
  is the one call such an agent never made through it; it now rides every tool
  call, and the first authenticated one repairs the binding. The engine refuses
  to overwrite a session an agent already has, so this is a repair and never a
  redirection.

  The second half is why it went unnoticed for days. `poll_unresolved` was
  counted from the day the health check was written and never reached a verdict:
  the check asked whether ANY call resolved, which a machine running several
  agents always answers yes to. So one agent's wake path being completely dead
  read `ok`, and `dibs doctor` printed "harness hooks resolving" while nine
  consecutive polls for that session found nobody. A count nothing reads is not
  a diagnostic, and a partial failure that reads as success is worse than one
  that reads as nothing.

- **A declaration no longer publishes its own prose as a space id.** An
  auto-opened space took its id from the words of the declaration, so a private
  repository's hostnames, service accounts and internal paths became durable
  board objects readable by agents in unrelated repositories, with no way to
  take them back. Ids now come from a ref where there is one (`issue:42` →
  `issue-42`) and otherwise from the project plus a digest.

- **`task install` no longer offers another project's signing identity.**
  `tools/signcheck` listed every code-signing identity in the keychain and
  proposed whichever came first. That is a cross-project dependency established
  by accident, because a macOS privacy grant keyed to a certificate another
  project owns is revoked the moment they rotate it; and printing the list is a
  disclosure, since a keychain holds identities for work that has not been
  announced and this output is the kind of thing that lands in an issue. It now
  looks for `Dibs Local Codesign` by name, names nothing else, and says how to
  create one.

- **The Claude Desktop manifest said version 0.0.0**, and no test could see it:
  it had never been on anybody's list of things to stamp. A list of things to
  keep in sync is itself a thing that falls out of sync, so the list is no
  longer trusted. `TestNoVersionedManifestEscapesTheStamp` goes looking for the
  thing it describes, failing on any JSON in the tree that states a version and
  is neither stamped nor explicitly somebody else's. It found this one
  immediately. The same manifest still carried the retired product name
  `io.agents/agents` and described spaces as "shared agents".

- **A tag is now checked against the changelog it ships.** The version guards
  held the manifests to the changelog, which left the changelog itself
  unverified: forgetting to claim the Unreleased section would publish a release
  whose every manifest named the previous version, with a green gate, because
  the manifests and the changelog agreed with each other about the wrong number.
  The release workflow runs the gate against the tagged commit, so the check
  fails the release rather than the developer, and is silent on an ordinary
  checkout where there is no tag to disagree with.

- **SPEC-CHANNELS.md is readable again.** The `lane` → `agent`/`space` rename
  ran over the document that defines the split, leaving a terminology table
  whose two rows were identical, a sentence with the same plural noun twice in a
  row, and a passage warning about careless renames whose own example had been
  renamed into two identical halves. The
  same sweep reached `internal/web/act.go` and both board templates, which is
  what the operator reads.

### Changed

- **The board panel, first pass: quiet instrument.** It had four nested rail
  systems down the left, an orbiting conic gradient with a blurred breathing
  core for a connection indicator, a diagonal hatch under every line of type,
  two coloured pools of light, glow on the figures and a halo on every live pip.
  Read cold it was instrument-shaped costume, and this file already argued
  twice, about the rail's cross-ticks and the metrics' corner brackets, that
  ornament imitating instrumentation costs a surface the trust it is imitating.
  The same argument finishes the job.

  Monospace now means data and nothing else. It had been marking the node id,
  "read only", group headings, badges, agent names, paths and counts: seven
  roles, which is none. Headings and badges are words, so they are set as words.

  Colour is spent once. "Out of touch" was warm on the heading, its count, every
  affected row's edge and the figure in the summary; it is now the count at the
  top, which says how many, and the edge on each row, which says which.

  The four-cell metric deck is a sentence: `1/16 live · 0 unanswered · 11
  declared · 1 out of touch`. The accessible label already read that way and
  read better than what sighted readers were given.

  And each agent is one line, opened on request. Sixteen agents spending four to
  twelve lines each answered "who else is here and what are they on" only for
  whoever scrolled: a well-written twelve-line declaration pushed the two agents
  that needed attention off the screen. A native `details` carries it, so it is
  keyboard operable and needs no script, and the text is clamped in CSS rather
  than sliced, so the whole declaration stays selectable, searchable and read in
  full by a screen reader. Which rows are open is held by each surface, because
  both rebuild the roster wholesale on every board change and a `details` does
  not survive that: expanding an agent and having the board tick would have
  snapped it shut.

  Two things were simply wrong and are fixed on the way past. The window title
  read `Dibs) Board`, and `Dibs (3 waiting` with mail. And the tab whose id says
  `agents` and whose label said `Dibs` renders SPACES: three names for one
  thing, in the surface a person reads.

- **A permission error now names the route to permission.** `E_NOT_COORDINATOR`
  and `E_NOT_ADMIN` stated the rule and stopped, which leaves a capable agent
  with nothing to do but give up or ask for the tool again. Every honesty hint
  in Dibs names the corrective call; these had none to name, because the
  corrective call is a human's. So they now name the human: send them a
  `request`, which raises a notification with Approve on it and returns their
  answer as an ordinary response.

- **Every message type says what it DOES to its recipient.** An agent choosing
  between `notify`, `question`, `request` and `handoff` was choosing by tone, on
  four labels that read as synonyms for "send". They now say which types wake
  the recipient now and which arrive at their next activation, so an agent can
  pick for the effect and structure the message for what it triggers.

- `tools/list` is 34.0k characters for 44 tools, down from 36.2k for 42. Every
  agent pays it on every cold connection, and the reasoning behind a rule
  belongs in `dibs://skills`, which is fetched once, rather than in the
  description of every tool that follows from it. No corrective detail was
  dropped; the war stories moved.

- Published to the official MCP Registry as `io.github.agenxy/dibs`, on the same
  tag trigger as the release, so the registry entry, the GitHub release and the
  Homebrew cask cannot drift from each other. The registry is where a harness or
  an agent looks up a server it has never heard of, which for this product is
  the audience that matters: the tap serves people who already know the name.
  The publisher is pinned and checksummed rather than piped from a `latest`
  redirect, because that job holds `id-token: write`.

## [0.0.5] - 2026-08-14

### Added

- `GET /livez`, unauthenticated, answering only that the daemon is up.
  Everything else needs the coordination secret, which is right for anything
  that reveals the board and wrong for liveness: supervising Dibs meant handing
  a monitoring system the same secret every agent authenticates with. It leaks
  strictly less than opening a TCP connection to the port already does.

- **A fleet can span machines.** `dibs trust <host:port>` records the certificate
  a remote daemon serves, and `dibs fingerprint` prints what that daemon serves,
  so the two can be compared by eye before anything relies on it. The pairing is
  ssh's, for the same reason: the daemon signs its own certificate and stands up
  no CA, so the first connection has nothing to verify against and the answer is
  to look once and record it. It costs the operator nothing extra, because a
  second machine already needs the coordination secret carried across by hand
  and the fingerprint travels on the same trip. Trusting one daemon trusts only
  that daemon: a machine holding the secret but not the certificate is still
  refused, and so is one that trusts a different certificate.

- **Codex agents can be woken.** Mail is delivered into the session instead of
  waiting for the agent to poll, using Codex's `mcp_tool` hook handler: it calls
  Dibs over the MCP connection the model already holds, with no subprocess, so
  nothing here lets Dibs drive a harness. This was refused for as long as the
  only reachable handler was `command`; that variant now exists, and the doc
  that said it did not ends with the instruction to re-check, which is how it
  was found. Deliberately limited to `SessionStart` and `Stop`: a tool matcher
  guessed wrong fails silently, and Codex's tool names are not verified here yet.

- `task install` honours `DIBS_CODESIGN_IDENTITY`, signing both binaries with a
  persistent identity. macOS keys a privacy grant to a code signature, and the
  Go toolchain signs ad-hoc, so every rebuild is a different program to the
  system and any Files-and-Folders or Full Disk Access grant silently stops
  applying. That matters when checkouts live under Desktop, Documents or
  Downloads, where the daemon needs permission to read them at all.

- `dibs doctor` reports an ad-hoc signed daemon and says what it costs, because
  the symptom (matching worked, then quietly stopped after an install) points at
  everything except the signature.

- `task smoke`, in the gate: it runs the built binaries and asserts on what they
  actually print, against expectations written by hand rather than generated
  from the source. Everything above was invisible to a green suite because the
  rename edited the fixtures and the code together, so the tests agreed with the
  bug: `doctor_test.go` asserted `[mcp_servers.agents]`. A check the sweep cannot
  reach is the only kind that can catch the sweep.

### Changed

- **Matching calibrates its own notify threshold.** The daemon already found the
  repository, indexed it unprompted, and held the scorer and the corpus; it then
  stopped one step short and asked a human to run `dibs calibrate`, read a
  number, and type it into a TOML. Measured cost of the step it declined to
  take: 120ms on this repository, against the indexing it had already done.

  What made that worse than it looks: an unset notify threshold is ZERO, and a
  zero bar mentions every scored match, related or not. The untouched default
  was not "off pending calibration", it was the loudest possible setting, so the
  feature's first impression was noise. Measuring is strictly safer.

  Only the notify bar. `join` stays at 0 unless asked for, because auto-JOINING
  on a measured-but-unreviewed number is a different risk: a wrong mention costs
  a glance, a wrong join costs an agent's membership. What was adopted is logged
  with the false-mention rate it buys and the flag that overrides it.

- **A board written by v0.0.4 or earlier is no longer opened.** `dibd` now says
  so on startup and names the record it will not read. Set the file aside
  (`mv ~/.dibs/ledger.jsonl ~/.dibs/ledger.jsonl.old`) and start it again; your
  work is untouched, the coordination history is not. This is the same clean
  break the 0.0.2 notes describe, applied to the half of the rename that was
  missed, and it is the last one.

- **Work-overlap matching is on by default, and indexes every repository your
  agents work in.** It used to be gated behind `-match-repo`, so the feature
  this product exists for was silent on every install that did not know to set
  a flag. There was no constant to default that flag to, because the daemon
  serves agents across every project open on the machine.

  The fleet already knew the answer: every agent registers with a working
  directory, and the tree containing it is exactly the history worth mining. So
  each repository is indexed the first time an agent turns up in it, up to
  sixteen, and there is one index per repository. An agent is scored by the tree
  it is working in; an agent in a tree that is not indexed gets no semantic
  suggestions rather than someone else's, because a co-change model asked about
  another project's sentence answers confidently and wrongly.

  `-match-repo` remains only as a pre-warm, for a daemon started at login that
  should have an index ready before the first agent arrives. Closes #7.

### Fixed

- **An 11.6 MB compiled binary was committed at the repository root.** A bare
  `go build ./cmd/dibs` writes `./dibs`, and the next `git add -A` swept it in.
  An operator auditing Dibs before trusting it with a fleet called it "the single
  strongest trust smell available": an unexplained committed executable is the
  shape of a supply-chain compromise and nothing in the tree lets a reader verify
  it, so they deleted it and built from source. It also dominated the source
  tarball. Untracked, ignored, and a hygiene test now detects committed
  executables by CONTENT rather than by name, because the file nobody meant to
  add has no extension to match on.

- **Test fixtures inherited the operator's global git config.** They set
  `GIT_CONFIG_NOSYSTEM=1`, which suppresses only the *system* config, so
  `~/.gitconfig` still applied: on a machine that signs commits, `go test ./...`
  popped a GUI credential prompt mid-run and failed naming a signer the
  contributor had never heard of. The fixtures now pin `GIT_CONFIG_GLOBAL` too.

- **`dibs calibrate` mislabelled where its notify number came from.** It always
  said "(median of the same)", but Notify is `max(median, join/2)` and a
  well-discriminating scorer drags the median to zero, so on this repository the
  floor produced 0.199 while the label named a rule that had not been applied.
  An operator read it, correctly inferred from "median" that about half of
  unrelated pairs must clear the bar, and filed that as a finding: sound
  reasoning from a false premise that was ours. The label now names the rule
  that actually ran.

- `dibs calibrate` said what to set and not what it costs. It now prints the
  false-mention rate the suggested `notify_threshold` buys, measured on the same
  population the threshold came from: on this repository, 39% of unrelated pairs
  clear it. That number was always computed and never shown, so the only way to
  learn it was to run a fleet and watch unrelated work get mentioned.

- `declare` reported `matching: "off"` during the first second after an agent
  registers, while the repository was still being indexed. "Off" plus "no
  repository indexed yet" reads as "you have not configured this", so the honest
  response is to go configure something; the correct response was to wait. The
  `indexing` state already existed and nothing ever entered it.

- Repository-hygiene tests failed with `exit status 128` when run from a release
  tarball rather than a checkout, which reads like a broken machine. They skip
  outside a work tree, where the property they assert cannot exist.

- The `type` parameter on `send` carried its enum with an empty description, so
  a client rendering per-parameter help showed a blank for the one parameter
  whose values need explaining.

- The README said `go build` needs "nothing but Go 1.26.5" while `go.mod` pins
  toolchain 1.26.6, which triggers a download that fails hard on a
  restricted-egress network. It names 1.26.6 and mentions `GOTOOLCHAIN=local`.

- **A remote agent's pid was probed on the wrong machine.** The sweep and the
  board both asked this kernel whether a pid was alive, with no check that the
  agent was on this host, so a healthy agent on another machine was declared
  dead and its claims released, and an unrelated local process holding the same
  number reported it alive on evidence about a different program. The stdio
  bridge registers with its own pid, so every remote agent arrives carrying one
  that means nothing on the server: the fault was armed by the same change that
  made remote agents possible. A pid is now evidence only where it can be
  observed; a remote agent falls through to the lease, where silence is judged
  by the clock. An unknown host still counts as local, so existing boards are
  unaffected.

- **The CLI could not talk to its own daemon off loopback.** Every request was
  built as `"http://" + addr()`, in eighteen places, while the daemon serves TLS
  on any address another machine can reach. So the moment a daemon was moved to
  serve a fleet, `dibs board`, `dibs doctor` and the rest failed against a daemon
  that was working perfectly. The client now derives the scheme from the same
  rule the server applies, so the two agree by construction.

- **The self-signed certificate was refused by every Apple client.** It was
  issued for ten years, and macOS and iOS reject any TLS server certificate
  valid for more than 398 days, reporting it as "certificate is not standards
  compliant" and declining to connect at all. The one path that exists to let a
  second machine reach the daemon without a CA therefore did not work on the
  operating system Dibs is mostly run from. Now 365 days, with replacement 30
  days before expiry, because a bounded life that nothing renews is just a later
  outage.

- A refused certificate was reported as **"dibd not running"**. Those need
  opposite actions, and the wrong one was given on exactly the path where
  somebody is bringing up a second machine and has no other signal: they would
  go hunting for a dead process that is alive and well.

- **Upgrading silently demoted every persistent agent to ephemeral.** The
  vocabulary rename changed op field names as well as op kinds, and a renamed
  field does not fail: `lane_kind` was simply not read, so the op applied with
  the field zero and replay reported success over a board that had quietly lost
  its persistent agents (no nonce resume, no coordinator eligibility) while a
  replayed `sweep` that recorded `dead_lanes` marked nobody dead. Every release
  up to v0.0.4 wrote those names. `state == fold(ledger)` was broken with
  nothing raised anywhere, which is the one outcome a hash-chained ledger exists
  to prevent, so the daemon now refuses the ledger instead of misreading it.

  The test that froze the on-disk field names had been guarding this since
  v0.0.0. The sweep rewrote its frozen list to match the new tags, and the
  comment explaining why the list must never be rewritten, which it left saying
  "renames the participant from `Agent` to `Agent`". The list is now
  fingerprinted, so a sweep that rewrites the words fails on the hash it cannot
  recompute.

- The startup check for an outdated board listed the vocabulary it had just been
  renamed *to*, so it recognised `register` and `declare` as obsolete words: on
  any replay failure it told the owner their current board came from 0.0.2 and
  to move it aside. It also only ran when replay had already failed, which the
  case above never does. It now runs before the fold, reads the whole ledger
  rather than the first fifty records, and its words are checked against the
  core rather than a second hand-maintained list.

- Every subcommand's `--help` said `usage: agents <verb>`, and every bad-flag
  error told you to run `agents <verb> --help`. The string lives in one shared
  helper that no named smoke check goes through, so eleven checks on individual
  commands all passed over it. The harness now runs `--help` for every verb the
  binary reports, plus the bad-flag path, matching a stale name only in command
  position: a looser rule failed two verbs on prose that legitimately says
  "agents".

- `dibs doctor` did not notice that the service starts a different daemon than
  the one installed. A unit records an absolute path, so installing from a Go
  workspace once and from `task install` later leaves the service starting the
  first build forever: the daemon answers, every other check passes against it,
  and every fix shipped since is not running. It is checked now, with the `rm`
  and the re-run that fixes it.

- **`declare` told agents that an AGENT had been opened for their work.** Agents
  are not opened; spaces are. The result key was `agents`, each entry carried
  its space id under `agent`, and the hint read "no existing agent cleared the
  match threshold, so one was opened for this work", leaving a declaring agent
  to work out whether Dibs had just invented a peer for it. They are `spaces`,
  `space` and `spaces_hint` now, matching the board resource, which has said
  `spaces` all along. Found by declaring work and reading the answer.

- **`dibs log` silently dropped every registration.** The reader typed the op's
  `agent` field as a string, but on a `register` it is the descriptor object
  (harness, model, cwd), so those lines failed to parse; a line that failed to
  parse was skipped without a word. On the board this was written against, 100
  ledger records rendered as 86 rows. An agent joining is the event people come
  to the log to confirm, and a peer reported a new agent it could corroborate
  nowhere. An unreadable record now says so and costs one line instead of
  vanishing.

- **`board` told the agent the human had seen the board, on hosts that render no
  panel.** The sentence was appended unconditionally: the function was not even
  passed the answer. So the agent reports "I've shown you the board" and the
  human is looking at nothing. It now says what is actually known, which is that
  the board was SENT and whether the host claims it can draw it. It does not
  claim the opposite either: the reference host declares nothing and renders
  from `_meta` regardless.

- `board(detail: true)` returned a summary instead of the board on the host most
  likely to be running. `detail` reached only `content`, and a host that shows
  the model `structuredContent` INSTEAD of `content` drops exactly that, so the
  one documented way for an agent to read the board on purpose answered with one
  sentence and the agent's own token. Found by an agent that wanted the board,
  asked for detail, got nothing usable, and went back to querying the daemon
  over plain HTTP: the tool taught it not to use the tool.

- `subscriptions/listen` did not work through `dibs mcp-stdio`, which is how the
  Claude Code plugin and every other stdio harness connects. The bridge read
  each response to completion before writing it, so a stream that never ends
  hung until a 75-second timeout killed it, silently. Push notifications were
  therefore a direct-HTTP-only feature while the plugin path polled, and nothing
  on either side said so. The bridge now streams that one call on its own
  goroutine, unwrapping SSE frames into JSON-RPC lines, with stdout serialised
  because notifications interleave with replies.

- An unknown resource answered `-32002` to every caller. 2026-07-28 moved
  resource-not-found to `-32602`, on the grounds that JSON-RPC already has
  "invalid params". Dibs serves both revisions from one handler, so the code is
  now the one the calling revision expects, rather than a constant that is wrong
  for half of them.

- A `prune` was never written down, so the record came back on the next restart
  holding its old token. `prune_own` closed the agent in memory and returned
  without advancing the serial, and the engine ledgers exactly when the serial
  moves. The admin `prune` carried a comment about this same fault, three
  functions further down the same file. Every op that changes replayable state
  is now covered by one test that asserts the serial moved, because saying it in
  prose has failed three times: `prune`, `claim_coordinator`, `prune_own`.

- The coordinator could not clear another agent's debris, which is the thing its
  own rationale names. `prune` routes to `prune_own`, which refused every peer,
  and the admin `prune` op is reachable only from the human path, so the role
  granted nothing an ordinary agent did not already have. A coordinator may now
  prune a record that has stopped. Not a live one: an agent that can delete a
  working peer's row can delete the evidence that somebody else is already on
  the objective, and no role gets that.

- The vocabulary rename left `LANES(1)` as the title of the generated man page
  and `agents` as the command it documents, four wire error codes reading
  `E_LANE_*` on a surface with no lanes in it, and their messages rewritten into
  nonsense: closing a space refused with "an agent with agents in it is
  somebody's working context". The codes are `E_SPACE_*` and the messages talk
  about spaces again.

- An argument that did not decode was refused with no `hint`, the one rule this
  surface exists to keep: the agent was told what was wrong and nothing about
  what to do instead. A `register` carrying the pre-0.0.3 nested `agent` object
  got "agent must be a string, got object" and no way to learn the current
  shape. Those three protocol errors now name the call that answers the
  question, as do an unknown resource and an unknown method.

- A coordinator claim that presented the right secret spent it even when the op
  was then refused, leaving the board with no coordinator and no way to appoint
  one short of restarting the daemon. Checking the secret and spending it are
  now separate: the claim is consumed once the grant is ledgered.

- The bound on git calls made the permission it needed impossible to grant. A
  first call against a macOS protected folder puts a dialog on the user's
  screen, and macOS shows that dialog on behalf of the requesting process: a
  20-second timeout killed git while the dialog was still up, leaving the prompt
  with nothing to grant to. The deadline is now four minutes, which is a person
  reading a dialog rather than a hang, and the error says that answering it and
  registering again is all that is needed.

- The daemon identified itself to macOS as `a.out`, the Go toolchain's default,
  so any privacy grant was recorded against a name shared with every other
  ad-hoc binary. `task install` now always sets `org.agenxy.dibs`, whether or
  not a signing identity is configured.

- A repository the daemon could not read was written off for the life of the
  process. Dedup lived in two places: the daemon's, which releases a tree it
  failed to read so a later attempt retries, and the engine's, which never
  cleared. Granting the daemon access and registering again therefore did
  nothing, which is the exact situation the retry existed for.

- `task install` removed `$DEST/agents`, a name that has never existed, and then
  copied over the live `dibs`. That reuses the inode, macOS invalidates the
  cached signature, and every later run is SIGKILLed with no message: precisely
  the failure the comment directly above that line warns about.

- The data directory really is `~/.dibs` now. The 0.0.3 notes below said so and
  the code did not: the vocabulary rename turned every "lane" into an "agent"
  and took `~/.lanes` with it, so two releases shipped writing to `~/.agents`, a
  generic name in the user's home that any number of other tools could claim.
  The same slip put a `.agents` directory inside your repository for monitor
  state. An existing directory is still found and used, under either old name,
  so no board moves and nothing is lost; `dibs doctor` names the one it opened
  and gives you the `mv` if you want it.

- `dibs mcp-config` published the Codex/TOML server block as `[mcp_servers.agents]`
  while the JSON block correctly said `dibs`. Half of that command's output has
  been wrong since 0.0.3.

- The Claude Code plugin did not work at all. Its `.mcp.json` declared
  `"command": "agents"`, a binary that has never existed, so the harness spawned
  it, failed, and showed a server that never started. The Claude Desktop manifest
  and the OpenCode README named the same missing binary.

- The panel resource was advertised as `ui://agents/board` while every other
  resource is `dibs://`.

- The signature-verification command in the README passed
  `--certificate-identity .../Agenxy/agents/...`, so anyone checking a release
  signature got a mismatch and had every reason to think the artifact was bad.

- On Linux `dibs configure --service` wrote `agents.service` and told the
  operator to run `systemctl --user enable --now agents`. systemd also had no
  guard against installing a second unit beside an old one, which launchd has
  had since the `com.agents.dibd` incident: two units on one data directory
  means the second fails the directory lock and reads as a service that will
  not start.

- `dibs doctor` told you to move an inherited data directory without mentioning
  that a service unit pins that path, so following the advice left the daemon
  starting against a directory that was gone.

- `dibs mcp-config` panicked with a Go stack trace on a `local.secret` shorter
  than 16 bytes, which is what a truncated or hand-edited one looks like.

- The README's install line said `brew install agenxy/tap/agents`, naming a cask
  that has never existed, so the first command a new reader runs did nothing.
  It also omitted `brew trust agenxy/tap`, which Homebrew 6 requires before it
  will load a cask from a third-party tap; without it the install fails with a
  trust error.

- The tap offered a stale `lanes` cask beside `dibs`, so `brew install
  agenxy/tap/lanes` still resolved to 0.0.2. It is now a `tap_migrations.json`
  entry, which moves an existing install across on the next `brew update`
  instead of leaving it on a version that gets no releases.

- A `git` the daemon ran against a tree could hang forever, and matching would
  wait on it in silence. On macOS `/usr/bin/git` dispatches into Xcode, and from
  a launchd agent against a protected folder (Desktop, Documents, Downloads) it
  blocks on an access prompt that can never be shown to a background process, so
  it never returns rather than failing. Found on a real machine: a `rev-parse`
  child sat there for four minutes, the indexing goroutine never finished, and
  because that repository was already latched as in-progress every later
  registration was deduplicated against work that would never complete.

  Every git the daemon runs is now bounded, and a tree it cannot read is named
  in the log and in `dibs doctor` along with the likely cause, instead of
  leaving matching quietly off. Unbounded work behind a deduplicating latch is a
  permanent silent failure, which is the shape this codebase keeps paying for.

### Security

- Go 1.26.6, which closes six standard-library advisories the 1.26.5 toolchain
  is subject to, four of them reachable from code this daemon runs
  (`http.Server.Serve`, `ServeTLS`, `http.Client.Do`).

## [0.0.4] - 2026-08-11

### Fixed

- The rename left the old name in places only running the binary would show:
  `dibs version` printed `agents`, `dibs doctor` looked for a binary called
  `agents` on PATH and for an MCP server of that name in harness configs,
  `mcp-config` generated a server block named `agents`, every error prefix and
  the "did you mean" suggestion said `agents`, the shell completions declared
  `#compdef agents`, and the daemon's self-signed certificate carried it as its
  common name. Found by exercising the CLI rather than reading the diff, which
  is how it should have been found before 0.0.3.

## [0.0.3] - 2026-08-11

### Changed

- **Lanes is now Dibs.** The name collided with an established project in the
  same niche, and described the opposite of what this does: everywhere else a
  "lane" is an isolated parallel workstream, while here it was an agent's
  identity on a shared board.
  - The CLI is `dibs`, the daemon is `dibd`, the environment is `DIBS_*`, the
    data directory is `~/.dibs`, and resources are `dibs://`.
  - `brew install agenxy/tap/dibs`. The old cask still installs 0.0.2 and is
    not updated further.
- **A lane is now an agent, and a lane of work is now a space.** Both concepts
  were called lanes, which is why the tool names never quite made sense. A space
  is where semantically-related work congregates, and what draws agents into one
  is a match rather than a rule, so "lane" was the wrong shape for it.
- **Tool names are verb-first and drop the noun where nothing else could be
  meant.** `register`, `resume`, `update`, `sign_off`, `check_in`, `board`,
  `declare`, `undeclare`, `open_space`, `join_space`, `leave_space`,
  `close_space`, `read_space`, `merge_spaces`, `watch_space`, `lock_space`,
  `unlock_space`, `post`, `announce`, `ack_announcement`, `admit`, `evict`,
  `send`, `read_mail`, `ack`.

### Breaking

- **A 0.0.2 board cannot be replayed by 0.0.3.** The ledger records op kinds by
  name and those names changed, so the daemon refuses to start rather than serve
  a board that disagrees with its own history. It says which word it found, the
  one command that fixes it, and asks the agent reading it to tell you what was
  set aside. Sorry: one clean break at 0.0.x beat two later.

### Fixed

- A ledger line that was valid JSON but carried no op panicked the daemon
  instead of being reported as corruption.
- `verify --json` dropped the corrective hint the prose keeps, so a board that
  had never run reported `open …: no such file` and nothing else on the surface
  agents read. Thanks to @shaurya703 (#17).

## [0.0.2] - 2026-08-11

### Added

- `dibs completion <bash|zsh|fish>` prints a completion script, generated from
  the verb table the CLI dispatches on so it cannot drift from the commands that
  exist. Thanks to @shaurya703 (#15).
- `dibs man` renders the manual page from the same help text `dibs help`
  prints, and releases ship `lanes.1` in the archive and the Homebrew cask, so
  `man dibs` answers after an install. Thanks to @shaurya703 (#16).

- Nine real Git configurations are now regression tests. Each builds actual
  repositories, registers agents through the actual server, and asserts on the
  warning an agent would receive: an orphan `--single-branch` clone, a vendored
  subtree, shallow clones with removed origins, case-variant remotes, a stale
  url after a rename, a `git replace` graft. Synthetic values proved the
  decision and never exercised the resolver, which is how several of the defects
  fixed in this release reached a review rather than a test.
- Two standards are now checked rather than remembered. `internal/hygiene`
  fails the build on a shell script entering the tracked tree, by extension or
  by shebang, and on an em dash in prose. The shebang is read the way `env`
  reads it, quoting included, and anything that cannot be shown to be shell-free
  is treated as shell. En dashes are flagged only when spaced,
  since a numeric range is correct typography.
- The board says which project each agent is in. A machine usually has more
  than one repository open, and agents in three of them all reported branch
  `main`, so the rows were indistinguishable. The project is resolved from the
  agent's working directory once, at registration, and recorded, so it names the
  project even when the agent is several directories inside it.

### Changed

- **Dibs is now Apache 2.0**, relicensed from MIT. Both are permissive; Apache
  grants patent rights from contributors explicitly, terminates the licence of
  anyone who sues over the covered code, and states that no trademark rights come
  with it. `NOTICE` retains the MIT notice for the contributions made under it.
- `dibs doctor` exits nonzero when it finds a problem, so a script can act on it
  rather than parsing the output. Thanks to @floze-the-genius (#6).
- The Homebrew tap moved from `agenxy/homebrew-agents` to `agenxy/homebrew-tap`,
  so the install line is `brew install agenxy/tap/lanes` rather than repeating
  the project name. GitHub keeps a redirect, so the old form still works and
  nobody who already tapped needs to act.
- The README leads with a worked example of a collision being caught, and the
  documentation says `dibs stop` wherever it used to say `pkill dibd`.
- The service identifier is `org.agenxy.dibs`.
- An unstamped build reports `devel+<revision>` rather than a version number
  that names a release it is not.
- Prose throughout uses ordinary punctuation.
### Fixed

- A refused port said only `bind: address already in use`. It now names the
  dibd already holding the address, or says the holder is something else and
  how to find it. It does not fall back to another port: clients resolve the
  daemon from a fixed address, so one that quietly moved would be a daemon
  nobody could find.
- `com.agents.dibd` was missing from the labels the service installer checks
  before writing a new unit, so upgrading from that vintage could leave two jobs
  contending for one data directory.
- Repository identity survives three Git configurations that previously made two
  clones of one project look like strangers: a shallow clone (which does not have
  its root commit, so it now records none rather than a boundary), `git replace
  --graft` (identity is read with replacement objects disabled), and
  `url.*.insteadOf` (the effective remote is resolved instead of the configured
  string).
- Repository identity is re-read when a directory becomes a repository. A path
  observed before `git init` was remembered as not-a-repository for the life of
  the daemon, so an agent there was filed as being nowhere: no project on the
  board, and no identity for anything else to reason with.
- Repository identity is re-read when a checkout path is reused. It was memoised
  by path with no expiry, so after `rm -rf project && git clone something-else
  project` a long-running daemon went on describing the repository that used to
  be there, missing collisions inside the new project and inventing them against
  the old. The Git common directory is now checked on every cache hit.
- A shell reached through a tracked symlink is caught. A link named
  `fixture-python` pointing at `/bin/zsh`, plus a file whose shebang named it,
  ran under zsh while the check passed: both the walker and the reader follow
  symlinks, so nothing ever saw the link itself.
- Repository identity is decided by positive evidence in three forms: the same
  Git common directory, the same canonicalised remote, or equal root-commit sets.
  Root sets known on both sides and unequal mean different projects; everything
  else is unknown, and unknown warns.
  - Remotes are compared case-insensitively, because every forge people use
    serves `Acme/Api` and `acme/api` as one repository.
  - Roots are compared for EQUALITY rather than overlap. `git subtree add`
    imports a dependency's whole history, so two unrelated projects that vendored
    the same library share a root commit, and treating that as proof fired the
    strongest signal Dibs has between strangers.
  - The remote outranks history, because history can legitimately differ inside
    one project: a `--single-branch` clone of an orphan branch, or a history
    rewritten by filter-repo, shares no commit with its sibling.
  - A consequence worth knowing: two forks have equal root sets and are treated
    as one project, which is usually right, since a fork's references normally
    name the upstream tracker.
- A ref such as `issue:42` matched across repositories, so two agents in two
  projects were told they were pursuing the same objective: the strongest signal
  Dibs emits, telling each to stop work nothing else was doing. Refs are
  repository-scoped now, using an identity recorded at registration. The
  same-repository case, including two linked worktrees and two clones of one
  upstream, still reports as before.
- `systemd` expands `$VAR` inside `ExecStart` even though it is not a shell, so
  a daemon path containing a dollar sign started whatever the environment said.
  Literal dollars are escaped.
- A path containing U+FFFE or U+FFFF passed validation and was then silently
  rewritten by the XML encoder, producing a LaunchAgent pointing at a different
  data directory. Characters XML cannot represent are refused.
- The corrective commands printed when an older service unit is found were not
  shell-quoted, so a path containing a space was not runnable as shown.
- `dibs stop --help --force` printed help and exited 0, while `--force --help`
  refused: an unknown argument is now refused whichever side of help it sits.
- The em-dash removal replaced placeholder glyphs with commas in places that are
  not prose: a zero timestamp rendered as `seen , ` in the CLI and on the board,
  table cells meaning "not applicable" became `, `, and several comments and
  error strings were left starting mid-sentence.
- Documentation promised automatic subagent stamping everywhere. The
  `PreToolUse` stamp exists only where a harness offers a hook Dibs can use
  without spawning a subprocess, which today means Claude Code. `dibs doctor`,
  the README, `SKILLS.md` and `SPEC-SUPERVISION.md` now say so.
- An agent whose working directory exceeded 128 bytes could not register at all.
  A cwd was bounded as if it were a name; it is a path. Any checkout a few levels
  inside a home directory hit it, and the refusal was of the whole
  `register`, not of the field.

- `dibs stop --help` stopped the daemon. The dispatch discarded its arguments,
  so asking a destructive command what it does performed it and exited 0.
  `dibs configure --service --help` had the same defect and wrote a LaunchAgent.
- Service units generated by `configure --service` mishandled ordinary paths. A
  directory containing `&` produced a plist launchd will not parse; on Linux,
  spaces split one path into several arguments, `%` expanded as a systemd
  specifier, and a newline could inject a second directive. Paths containing
  control characters are now refused rather than silently altered.
- A relative `DIBS_DIR` was written into the unit verbatim, so the service
  started against whatever it resolved to under the init system.
- Upgrading from a unit written by an earlier version created a second job for
  the same data directory. The directory lock refuses the second, which looks
  like a service that will not start; `configure --service` now stops and says
  how to remove the old one.
- `Daemon.IsStranger` canonicalised only one side of its comparison, so with
  `DIBS_DIR` set, or on a symlinked path, every daemon looked like a stranger
  including itself.

## [0.0.1] - 2026-08-10

### Fixed

- Repairs the release identity. See 0.0.0 below; that version cannot be
  installed with `go install` and is retracted in `go.mod`.

### Added

- `dibs stop`, which stops the daemon for this data directory and leaves other
  daemons on the machine running. The documentation previously said
  `pkill dibd`, which ends every fleet on the host.
- `dibs configure --service` writes a launchd or systemd unit, so the daemon
  survives a closed terminal and a reboot.

## [0.0.0] - 2026-08-09

**Retracted. Do not use.** The tag was moved after the Go module proxy had
cached it, and `sum.golang.org` is append-only, so `go install …@v0.0.0` fails
with a checksum error under `GOPROXY=direct` and serves an older tree through
the default proxy. A moved tag cannot be repaired.

First public release. Everything below is what v0 ships with rather than a list
of changes from something earlier: there is no earlier.

### Coordination

- Dibs, slots and advisory path claims over an append-only, hash-chained JSONL
  ledger. Replay is exact (`state == fold(ledger)`), so the persistence is the
  audit history; `dibs verify` checks the chain.
- Private mailboxes with typed messages: question, request, notify, handoff,
  with delivery receipts, deadlines and idempotent retries.
- Ephemeral and persistent agents; standing roles sleep as `dormant` with durable
  mailboxes and wake by resuming.
- MCP-native: 40 advertised tools over the 2026-07-28 stateless contract, with
  the legacy 2025-11-25 path for current hosts. Five more are callable but not
  listed: the lifecycle hooks a harness invokes on the agent's behalf. A tool an
  agent cannot correctly call is not a capability, it is a trap.
- A coordinator can retire a finished agent with `close_space`. Auto-opened agents
  end themselves when their last member leaves; an agent a human opened outlives
  its members on purpose, and until now nothing could ever end one, so a board
  accumulated finished agents permanently and E_LANE_LIMIT advised a fix that did
  not work for them. Refuses an occupied agent, and one holding an unacknowledged
  announcement.
- The human can act from the board panel, proving they are there with Touch ID
  (the admin password on machines without a sensor). **Binary releases do not
  include the Touch ID helper.** It is a small compiled Swift program that has
  to sit beside the binaries, and the release pipeline builds Go; a Homebrew or
  archive install therefore falls back to the admin password, which is a
  supported path rather than a broken one. `task install`, or a build from
  source on a Mac with the Xcode command line tools, produces it. Dibs reports
  the sensor as `unavailable` and sends you to the password: it never claims a
  human was checked when none was. What they get back is an
  ordinary agent identity, not a privileged one: every action is the same op an
  agent would send, so nothing in the state machine learns that humans exist.
  A cancelled request is reported as `abandoned`, never as a decline: a client
  that disconnects or a daemon shutting down is not a person who was asked and
  said no, and telling somebody they changed their mind about a prompt they never
  saw is the kind of claim this path exists to avoid.
  The check cannot be disabled by configuration: there is a scripted-verdict mock
  for development, but it lives behind a build tag, so the code that reads it is
  not compiled into a release binary at all. An environment variable is inert in
  a shipped build, and two tests, one in the untagged build, one against a real
  release daemon with the variable already set, hold that line.
- An agent whose chosen name is taken is told so, and told by what. Asking for
  `sol` and being handed `sol-4` used to be silent, so an agent could publish an
  address nobody could write to and never learn why the mail stopped. The suffix
  itself stays, a stale agent still owns its mailbox, and giving its name away
  would redirect somebody else's mail, but the note names the holder, says
  whether it is a live conflict or a retired agent holding an id the ledger still
  refers to, and points at reattach if the older agent is in fact you.
- Dibs-issued coordination keys. Opening or joining an agent hands the agent an
  opaque key; declared back in `refs`, later work is matched to that agent exactly
  instead of inferred from wording. Checked rather than trusted, a key the
  declaring agent does not hold is struck out, so copying one buys nothing, and
  inherited down a vouched parent/child lineage, which is what lets one
  coordination decision cover a whole fan-out of subagents.

### Duplicate-work matching (spaces)

- Agents declare work in their own words and are matched against work already in
  flight, using the repository's file layout and git co-change history. No model,
  download or network required for the floor.
- Optional embedding tier behind one endpoint, so MLX, llama.cpp, Ollama or a
  hosted API all satisfy it. An unreachable service degrades to the built-in
  scorer and records `degraded` rather than failing.
- `dibs calibrate` measures thresholds against your own repository's history and
  reports how much genuinely-related work clears the bar, not just a number.
  **Recalibrate if you measured a bar before this release**, because the runtime
  now gates on the quantity calibration actually measures. `dibs calibrate`
  scores one declaration against another; the runtime used to score a declaration
  against an agent's MERGED footprint, so the bar was measured on one thing and
  applied to another, and nothing said so. A candidate is now judged against the
  closest single live declaration: the same comparison, at last. The merged
  footprint was diluted by every other member, so the same pair scores higher
  now: measured on this repository, one scenario moved 0.29 → 0.45.
- Matching stays off until a repository is configured, and auto-join stays off
  until a threshold is measured.
- Repository identity comes from Git, not from the shape of two paths: linked
  worktrees are recognised as one repository, and separate clones as separate.
  Three-valued, so "Git is not installed" and "different repositories" are not
  the same answer. Resolved off the state loop, so no agent waits on `git`.

### Supervision

- Detects whether a spawned subagent is working, thinking, blocked or stuck, from
  process liveness, CPU duty cycle and transcript growth.
- Elapsed time is measured on a monotonic clock, so a sleeping machine does not
  read as a stalled fleet.
- Attribution survives detaching, daemonisation and reparenting: in Claude
  Code, a `PreToolUse` hook stamps a spawned command with its parent's agent.
- Reports and never acts: it hands back the command to resume a stalled child
  rather than running it.

### Surfaces

- Live web board: server-rendered, SSE-streamed, dark/light, no framework and no
  build step. Gated on an admin password the agents do not hold.
- Terminal board that degrades cleanly under pipes, redirection and `NO_COLOR`.
- `dibs doctor`, which names the fix rather than only the fault.
- Board panel as an MCP App, which fills from whichever carrier a host actually
  forwards, tool-result `_meta`, ordinary content, or by fetching the board
  itself, and says so plainly when a host forwards none, rather than showing an
  empty board that looks like a server fault.
- The MCP server DELIVERS its own plugin. `dibs://plugin` carries the actual
  files, manifest, hooks, skill, MCP server definition, so an agent with no
  network and no checkout can install one, and an ordered setup procedure where
  every step says how to check it took effect. On first registration an agent is
  told whether a plugin exists for the harness it just named, and whether its
  lifecycle hooks have ALREADY reached this daemon: installed on disk and
  actually loaded are different claims, and only the daemon can tell them apart.
  Harnesses with hooks but no wake path are told mail is still pull-only there,
  rather than being invited to stop checking.
- `check_in` costs the model half what it did. It is the one tool every agent
  must call every activation, and it returned the whole checkpoint twice: once
  in `content` and again, identically, in `structuredContent`. The duplicate
  existed for hosts that drop `_meta` and forbid an app from calling tools, where
  it is the panel's only carrier; a panel that has successfully called through
  the bridge proves the host is not one of those, so the copy stops. Measured:
  6472 model-facing bytes before, 3238 after, with `content` unchanged.
- `task panel:inspect` renders that panel against the live daemon and prints what
  it DREW as text, with switches to withhold each carrier on purpose. Diagnosing
  a panel from a screenshot is how the carrier bug survived a green suite, and
  `--unlock` drives the human lock so the unlocked panel is observable without a
  person putting a finger on a sensor.
- The panel marks an agent going out of touch, once, as it happens: that agent's
  line to the board retracts and returns amber. It is the board's most
  consequential change and it used to occur in silence, between two frames. A
  agent that changes state also TRAVELS to its new status group rather than
  vanishing from one and reappearing in another, so a change of state reads as
  one event instead of two unrelated edits: both directions, because recovery
  is worth seeing too.
- The board panel is drawn as a spine rather than a stack of cards. Records hang
  off the status rail instead of each sitting in a bordered, filled, bevelled
  box that repeated what the group headers already said; a reading of zero that
  needs no action steps back so the exceptions carry the row; name, current work
  and standing description are ranked instead of typeset alike; and an
  out-of-touch timestamp is marked uncertain with a dotted rule rather than
  struck through, which had said the one number worth reading was void.

### Platform

- Verified on macOS. Builds for Linux and arm64 on every push; behaviour on a GNU
  userland is unverified: see README §Platform.
