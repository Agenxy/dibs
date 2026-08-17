# Changelog

Notable changes to Dibs. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
