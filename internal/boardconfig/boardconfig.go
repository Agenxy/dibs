// Package boardconfig is dibs.toml: the one type, and the one loader.
//
// It exists because there were two. The daemon decoded the file into its own
// struct and refused any key it did not recognise; `dibs mcp-config`, which has
// to describe the daemon an agent will connect to, decoded a four-field
// projection and checked the rest against a hand-kept list of key NAMES. That
// list could only ever validate spelling, so `[limits] agent_ttl = 10` passed
// the CLI and produced a configuration while `dibd -check` refused the same
// file: the daemon would not start, and the command telling an operator how to
// connect to it reported success.
//
// A file that two programs must agree about belongs to neither of them.
package boardconfig

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/liveness"
)

// Config is everything dibd can be told. Every field is optional: running
// `dibd` with no arguments and no config file must always do the right thing.
// Written as <dir>/dibs.toml for people who want to get their hands dirty.
type Config struct {
	Addr              string            `toml:"addr"`               // listen address
	TLSCert           string            `toml:"tls_cert"`           // explicit cert (else auto)
	TLSKey            string            `toml:"tls_key"`            //
	InsecurePlaintext bool              `toml:"insecure_plaintext"` // force plaintext off-loopback
	Match             MatchConfig       `toml:"match"`              // work-overlap matching
	Limits            LimitsConfig      `toml:"limits"`             // coordination timings
	Supervise         liveness.Settings `toml:"supervise"`          // stalled-subagent detection

	// set reports whether a key was written in the file, as opposed to holding
	// its zero value because nobody mentioned it. Unexported, so it is not a
	// setting; nil for a zero Config, which reads as "nothing was written".
	set   func(key ...string) bool
	Roles RolesConfig `toml:"roles"` // standing coordinator/admin agents
	Wake  WakeConfig  `toml:"wake"`  // which news may extend an agent's turn
}

// WakeConfig is the [wake] table.
//
// One key, because there is one real decision: does an agent hear about mail
// when it arrives, or when somebody next types at it.
//
// The default is when it arrives. A fleet that waits for a human to kickstart
// its responsiveness is not agentic, and a time-sensitive request sitting
// unseen because nobody was at the keyboard is the failure this product exists
// to prevent. Waking is not driving: the digest says outright that it is
// coordination data the agent may act on or decline. What Dibs must not do is
// instruct.
//
// `urgent` is for an operator who would rather an FYI never cost a turn, and
// `none` for one who wants Dibs strictly pull-shaped. Neither is the default,
// because both trade away awareness for tokens and that is a trade only the
// person paying should make deliberately.
type WakeConfig struct {
	// ExtendTurnFor is "all" (default), "urgent", or "none".
	ExtendTurnFor string `toml:"extend_turn_for"`

	// NoticesWake decides whether situational awareness alone may extend a turn.
	//
	// A notice is something that happened TO an agent and that it could not
	// infer: it was evicted, its request was approved, somebody joined the space
	// it is working in. Useful, and not all of it is worth resuming a session
	// for, which is the cost this setting exists to control.
	//
	// Waking an agent means extending a turn on a thread that may be long and
	// whose prompt cache is cold, and on a fleet of idle sessions that is a real
	// bill to pay for "somebody joined your space".
	//
	// ON by default even so, because "an agent is told what happened to it" is a
	// guarantee this project already makes, in SPEC and in the browser suite,
	// and quietly making a guarantee conditional to save tokens is the wrong way
	// round: an operator who cares about the tokens can say so, and one who
	// never reads this file keeps the behaviour the documentation promises.
	//
	// Set it false to make notices pull-only. Nothing is lost when you do: they
	// queue, they ride along on any wake that happens for another reason, and
	// they arrive in full at the agent's own check_in, which it makes once per
	// activation anyway. What changes is latency, not delivery.
	//
	// Mail is unaffected either way: a peer waiting on an answer still wakes its
	// recipient, because somebody is blocked on that and nobody is blocked on
	// knowing who joined a space.
	NoticesWake *bool `toml:"notices_wake"`

	// Exec is how to REACH an agent that is not running, per harness. See
	// WakeExec. Absent means the board cannot start anything, which is the
	// default and was the only behaviour before this existed.
	Exec map[string]WakeExec `toml:"exec"`
}

// LimitsConfig is the [limits] table: the timings an operator's fleet actually
// changes. Everything else in core.Limits is a safety bound rather than a
// preference, and is deliberately not exposed.
type LimitsConfig struct {
	// AgentTTL is how long an agent may go silent before it is treated as
	// crashed. It matters more than it looks: a stale owner YIELDS its
	// exclusive agents, so an agent that is merely busy: a long build, a slow
	// tool call, a big test run makes no Dibs calls for its duration: can be
	// declared dead and lose an agent it is still working in.
	//
	// The default of 5m suits chatty agents. Fleets that run long silent steps
	// want more; fleets that want fast crash detection want less. There is no
	// value that is right for both, which is why this is a knob and not a
	// better constant.
	AgentTTL string `toml:"agent_ttl"`

	// IdleTTL is agent_ttl's counterpart for agents that gave no PID, where
	// silence is the only evidence available.
	//
	// It needs its own knob because it governs the configuration Dibs itself
	// tells people to use: `dibs mcp-config` prints a plain HTTP client, which
	// registers without a pid, so an operator who sets agent_ttl and points that
	// client at the daemon changes nothing and waits 45 minutes for a lapse
	// they thought they had configured to 5.
	IdleTTL string `toml:"idle_ttl"`

	// MaxPersistentAgents caps standing identities, and needs a knob because a
	// fleet's size is not a constant Dibs can guess.
	//
	// A persistent agent holds its slot while dormant. That is the point of one:
	// its mailbox and memberships survive the harness restarting. The
	// consequence is that the ceiling is reached by ACCUMULATION rather than by
	// concurrency, and on a real board it was: sixteen standing roles against a
	// default of sixteen, so registration was refused while the board held
	// sixteen agents of a possible sixty-four.
	//
	// Exposed as configuration and not raised as a default, because the default
	// being reached is usually a signal worth reading: siblings accumulate when
	// agents cannot prove they are themselves, and the fix for that is to
	// reclaim them rather than to make more room. A fleet that genuinely runs
	// forty standing roles should say so here.
	MaxPersistentAgents int `toml:"max_persistent_agents"`

	// MaxAgents caps live agents of every kind. Its counterpart above is the one
	// usually hit first, since ephemeral agents leave when they finish.
	MaxAgents int `toml:"max_agents"`

	// BlobStoreBytes caps the attachment store. It is a HARD bound: when the
	// store is over it, eviction drops referenced content rather than exceed
	// it, so a recipient can end up holding a message that names a blob which
	// is gone. Fleets that exchange large artifacts want this bigger; a machine
	// with little disk to spare wants it smaller. Accepts a plain byte count.
	BlobStoreBytes int64 `toml:"blob_store_bytes"`
}

// MatchConfig is the [match] table (SPEC-CHANNELS.md §7).
//
// It belongs in a file rather than only in flags because the two numbers that
// matter are MEASURED, not chosen: `dibs calibrate` prints thresholds for a
// specific repository, and retyping them onto a command line every restart is
// how a carefully calibrated bar quietly becomes a guess again.
//
// Every field is optional and a flag always wins, so an operator can override
// one setting for one run without editing anything.
type MatchConfig struct {
	// Repo enables matching. Empty means off: an index built from the wrong
	// repository is worse than none, so this is never inferred.
	Repo string `toml:"repo"`
	// Join is the auto-join threshold. 0 means suggest only. There is no safe
	// default: it is unitless and scorer-relative, so run `dibs calibrate`.
	Join float64 `toml:"join_threshold"`
	// Notify is the mention threshold.
	Notify float64 `toml:"notify_threshold"`
	// History bounds the co-change mining.
	History int `toml:"history"`
	// Deadline bounds the scorer; declaring work never blocks on it.
	Deadline string `toml:"deadline"`
	// EmbedURL points at a tier-2/3 embedding service (contrib/embed-sidecar).
	EmbedURL string `toml:"embed_url"`
	// EmbedModel is the model to request from it.
	//
	// Every other match setting could live here and this one could not, so an
	// operator using an embedding service had to retype `-match-embed-model` on
	// every restart while the URL beside it sat in the file. That is precisely
	// what this table exists to stop.
	//
	// There is deliberately no embed_key: a bearer token belongs in the
	// environment (DIBS_MATCH_EMBED_KEY), not in a file people paste into
	// issues and commit by accident.
	EmbedModel string `toml:"embed_model"`
	// EmbedQueryPrefix / EmbedDocPrefix override the retrieval markers Dibs
	// infers from the model name.
	//
	// Retrieval models are asymmetric and every family marks its two sides
	// differently. Dibs knows the common conventions, but a model it has not
	// heard of gets none, and measured on this repository, a model addressed
	// without its markers separates related from unrelated work about half as
	// well. Set these when you know the convention Dibs does not.
	EmbedQueryPrefix string `toml:"embed_query_prefix"`
	EmbedDocPrefix   string `toml:"embed_doc_prefix"`
	// DirectorRequired gates joins on a coordinator admitting them. Off by
	// default: it serialises the fleet behind one agent.
	DirectorRequired bool `toml:"director_required"`

	// AutoJoin: "declared" (default), "always", or "never".
	//
	// Who decides whether a match becomes a membership. The default lets Dibs
	// join you only on DECLARED overlap: both agents named the same ref, which is
	// a fact rather than a similarity score, and offers everything else as a
	// proposal for the agent to judge. See engine.MatchConfig.AutoJoin for why
	// that turned out to be the right split.
	AutoJoin string `toml:"auto_join"`
}

// WakeExec is how the board reaches an agent that is not running.
//
// Dibs is meant to be a phone, and a phone that only rings while you are
// already holding it is not one. Until this existed, an agent had to be
// executing for mail to arrive: hooks fire on an agent's own turn boundary, and
// an idle session has no boundary coming. That made the delivery guarantee
// conditional on the recipient already being awake, which is the opposite of
// what a message service is for.
//
// ARGV, NEVER A SHELL STRING, and never anything an agent said. The command is
// the operator's, out of their own config file, and the only substitutions are
// whole argv elements. An agent that could contribute any part of this would
// have arbitrary code execution on the machine through the message it sends,
// which is the one failure that would be worse than not delivering at all.
//
//	[wake.exec.codex]
//	argv = ["codex", "queue", "--thread", "{thread}", "--message", "{message}"]
//
// Placeholders, each replaced as a COMPLETE element: {thread}, {agent},
// {from}, {type}, {message}. Anything else is left alone.
type WakeExec struct {
	// Argv is the command and its arguments. Empty means this harness has no
	// wake command, which is the default and is not an error.
	Argv []string `toml:"argv"`
	// Cooldown is the shortest gap between two wakes of the same agent. Zero
	// takes the default; a fleet that wakes on every message is a fork bomb
	// with better manners.
	Cooldown time.Duration `toml:"cooldown"`
}

// RolesConfig is the [roles] table.
type RolesConfig struct {
	// Coordinator gets breadth with ONE narrow reach into private content:
	// broadcast, force-release, merge and evict, plus adopting a peer that is
	// not active, which moves that agent's messages into a live mailbox so they
	// can be read. That last one is the point of adoption and it is ledgered.
	//
	// This said "never another agent's mail", which is the flat claim
	// internal/core/roles.go records as already found wrong once. Repeating it
	// in the newly centralised config type would have reintroduced a false
	// authorisation contract in the file an operator reads to decide who gets
	// this. Admin is still the difference in kind: it reads every mailbox,
	// without adopting anything.
	Coordinator []string `toml:"coordinator"`
	// Admin adds the god view, mail included. Grant it only to an agent trusted
	// as the operator trusts themselves.
	Admin []string `toml:"admin"`
	// Identity binds a declared name to the FINGERPRINT of the agent that may
	// hold it, and it is what makes a standing role safe on a board with no
	// history.
	//
	// A name on its own authenticates nobody. The pin file records whoever
	// registered under a declared name first and holds them to it afterwards,
	// which stops a later impostor and does nothing about the first one: an
	// agent that read dibs.toml, or guessed that `admin = ["fleet-lead"]` is a
	// likely line, could register under that name before the operator's own
	// agent came up and be handed the god view with every mailbox in it.
	//
	// A FINGERPRINT, and never the nonce itself. The first version of this
	// asked for the raw nonce, which was worse than the hole it closed: a nonce
	// is the whole recovery credential, so an agent under the same account that
	// read dibs.toml could reattach AS the admin, rotate its token and take its
	// mailbox at any time, rather than merely winning a two-minute race. Found
	// by the pre-release review round after the one that added it.
	//
	// The daemon prints the fingerprint of any agent that registers under a
	// declared name it cannot yet verify, so the way to fill this in is to
	// start the agent and copy the line out of the log. Nothing secret is
	// typed, stored or transmitted.
	//
	//	[roles]
	//	admin = ["fleet-lead"]
	//	[roles.identity]
	//	fleet-lead = "9f2b...64 hex characters, from the daemon's log"
	Identity map[string]string `toml:"identity"`
}

// Load reads <dir>/dibs.toml if present. A missing file is not an error: zero
// config is the supported default, not a degraded mode.
//
// Both dibd and `dibs mcp-config` call this, which is the point of the package:
// a file the daemon refuses must not be one the command describing that daemon
// accepts.
func Load(dir string) (Config, error) {
	var c Config
	// #nosec G304 -- a path inside the daemon's own data directory, or one the
	// operator pointed the CLI at. Same-user access only; refusing it would mean
	// refusing to run.
	b, err := os.ReadFile(filepath.Join(dir, "dibs.toml"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return c, err
	}
	// A key TOML did not recognise is a key that did nothing.
	//
	// Silently ignoring it is the worst outcome available: `[limit]` for
	// `[limits]`, or `agent_ttl` under `[match]`, parses cleanly, changes
	// nothing, and leaves the operator certain they configured something. They
	// then debug the behaviour they were trying to change. Decode reports what
	// it could not place, so say so and name it.
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, 0, len(un))
		for _, k := range un {
			keys = append(keys, k.String())
		}
		return c, fmt.Errorf(
			"unknown setting(s) in dibs.toml: %s: check the spelling and the table "+
				"they are under ([match], [limits]); nothing here took effect",
			strings.Join(keys, ", "),
		)
	}
	// WHICH KEYS WERE ACTUALLY WRITTEN, carried into validation.
	//
	// An unset duration and an explicit `every = "0s"` are the same zero in the
	// struct, and they mean opposite things: one is "use the default" and the
	// other is a setting the daemon then ignores, because Settings.Apply
	// overlays only values above zero. Refusing every zero would refuse the
	// default; refusing none of them let `min_age = "0s"` read as configured
	// and do nothing. Only the decoder knows the difference. Found by the
	// pre-release review, which noted this list claimed to cover ignored
	// settings and had no zero case at all.
	c.set = md.IsDefined
	return c, c.Validate()
}

// MinAgentTTL is the floor for any lease duration in [limits].
//
// Agents renew at half the TTL, so anything shorter marks healthy agents
// crashed between their own keepalives.
const MinAgentTTL = 5 * time.Second

// AutoJoinPolicies is every value [match] auto_join accepts.
//
// Taken from the engine's own constants by TestConfigVocabulariesMatchTheEngine,
// because inventing one was worse than not checking at all: a validator built
// on "declared", "predicted" and "off" refused the working configurations
// `always` and `never`, and recommended two values that silently behave as
// declared. A vocabulary check has to be against the vocabulary that exists.
var AutoJoinPolicies = map[string]bool{"declared": true, "always": true, "never": true}

// WakePhases is every value [wake] extend_turn_for accepts.
//
// String literals rather than the engine's constants, because this package must
// not import the engine: `dibs` reads this file and has no business linking the
// daemon's internals. TestWakePhasesMatchTheEngine holds the two together.
var WakePhases = []string{"all", "urgent", "none"}

// CheckDuration parses a duration setting and enforces the floor.
func CheckDuration(table, key, raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("[%s] %s = %q is not a duration: write it like \"5m\", "+
			"\"90s\" or \"1h\": %w", table, key, raw, err)
	}
	if d < MinAgentTTL {
		return 0, fmt.Errorf("[%s] %s = %q is below the %s floor: agents renew their "+
			"lease at half the TTL, so anything shorter marks healthy agents crashed "+
			"between their own keepalives", table, key, raw, MinAgentTTL)
	}
	return d, nil
}

// CheckCount refuses a negative ceiling.
func CheckCount(table, key string, raw int) error {
	if raw < 0 {
		return fmt.Errorf("[%s] %s = %d: a ceiling cannot be negative", table, key, raw)
	}
	return nil
}

// Validate refuses what this file can be wrong about on its own.
//
// Decoding and unknown keys are not the whole of what makes a configuration
// unusable: `agent_ttl = "10"` is a string, decodes cleanly, and is not a
// duration, and `extend_turn_for = "everything"` is a perfectly good string that
// names no policy. Both stopped the daemon while `dibs mcp-config` printed a
// complete configuration around them and exited 0. Raised by the pre-release
// review, one round after the shared type was supposed to have ended this.
//
// What stays with the daemon is the part that needs the daemon's own defaults:
// whether a ceiling set here exceeds one that is not, and whether a blob cap
// clears a maximum blob size this package does not know. Those are checked
// where those values live, and this does not claim otherwise.
func (c Config) Validate() error {
	for _, check := range []func() error{
		c.validateAddr, c.validateTLS, c.validateLimits, c.validateMatch,
		c.validateSupervise, c.validateWake, c.validateRoles,
	} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// validateRoles catches the two ways a standing role reads as configured and
// is not.
func (c Config) validateRoles() error {
	declared := map[string]int{}
	for _, n := range c.Roles.Coordinator {
		declared[n] |= 1
	}
	for _, n := range c.Roles.Admin {
		declared[n] |= 2
	}
	// One agent, one role string. Naming the same agent in both lists made the
	// reconciler grant coordinator and then admin on every pass through the
	// startup window: admin, coordinator, admin, fifteen seconds apart, two
	// ledger entries each time and a window in between where admin-only calls
	// fail. Not a race the operator could see, and not something they meant.
	// Found by the pre-release review.
	for name, in := range declared {
		if in == 3 {
			return fmt.Errorf("[roles]: %q is named as both coordinator and admin, "+
				"and an agent holds ONE role: the reconciler would grant one and then "+
				"the other on every pass. Admin already includes what coordinator can "+
				"do, so name it in admin alone", name)
		}
	}
	for name, fp := range c.Roles.Identity {
		if declared[name] == 0 {
			return fmt.Errorf("[roles.identity]: %q is not named in [roles] coordinator "+
				"or admin, so this line grants nothing and reads as though it does", name)
		}
		if !isFingerprint(fp) {
			// The likeliest wrong value is the NONCE itself, which is what the
			// first version of this feature asked for and is a credential: a
			// nonce in a file another same-user agent can read is that agent's
			// route to reattaching AS the role holder.
			return fmt.Errorf("[roles.identity] %s: expected a 64-character hex "+
				"fingerprint, not %d characters. If you put the agent's NONCE here, "+
				"take it out: it is that agent's whole recovery credential, and this "+
				"file is readable by everything running as you. Start the agent and "+
				"copy the fingerprint the daemon logs for it", name, len(fp))
		}
	}
	return nil
}

// isFingerprint reports whether s is the shape RolePinFingerprint produces.
func isFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// validateAddr: the daemon BINDS this one, so it may not carry a scheme.
// net.Listen takes host:port and cannot bind a URL. A client's DIBS_ADDR is a
// different grammar and is checked where it is used.
func (c Config) validateAddr() error {
	if c.Addr == "" {
		return nil
	}
	if _, _, found := strings.Cut(c.Addr, "://"); found {
		return fmt.Errorf("[addr] %q carries a scheme, and the address a daemon "+
			"listens on cannot: write it as host:port. dibd hands this to net.Listen "+
			"verbatim, so it would fail at bind rather than here", c.Addr)
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("[addr] %q is not a host:port address: %w", c.Addr, err)
	}
	return nil
}

// validateTLS: HALF a pair is a mistake, and it looked like an absence. The
// daemon started and served plaintext, or an unrelated self-signed certificate,
// while the operator's explicit setting did nothing.
func (c Config) validateTLS() error {
	if (c.TLSCert == "") == (c.TLSKey == "") {
		return nil
	}
	missing, given := "tls_key", "tls_cert"
	if c.TLSCert == "" {
		missing, given = "tls_cert", "tls_key"
	}
	return fmt.Errorf("%s is set and %s is not: a certificate without its key "+
		"cannot be served, and half a pair is ignored rather than refused, so the "+
		"daemon would start on a transport you did not ask for", given, missing)
}

func (c Config) validateLimits() error {
	for _, d := range []struct{ key, raw string }{
		{"agent_ttl", c.Limits.AgentTTL},
		{"idle_ttl", c.Limits.IdleTTL},
	} {
		if _, err := CheckDuration("limits", d.key, d.raw); err != nil {
			return err
		}
	}
	for _, n := range []struct {
		key string
		raw int
	}{
		{"max_persistent_agents", c.Limits.MaxPersistentAgents},
		{"max_agents", c.Limits.MaxAgents},
	} {
		if err := CheckCount("limits", n.key, n.raw); err != nil {
			return err
		}
	}
	if c.Limits.BlobStoreBytes < 0 {
		return fmt.Errorf("[limits] blob_store_bytes = %d: a store cannot hold a "+
			"negative number of bytes", c.Limits.BlobStoreBytes)
	}
	// AGAINST THE DEFAULT WHEN THE FILE OMITS THE OTHER SIDE.
	//
	// This compared the two only when the file set BOTH, on the reasoning that
	// the daemon's default "is not ours to know". It is: core.DefaultLimits is
	// the same value the daemon folds these over, and refusing to look at it
	// left exactly the hole this loader exists to close. With max_agents
	// omitted, `max_persistent_agents = 65` passed here and was refused by the
	// daemon at startup, so `dibs mcp-config` printed a complete configuration
	// and exited zero for a board that cannot boot. A shared loader that
	// accepts what the daemon rejects is worse than two loaders, because it
	// reads as a guarantee.
	limits := core.DefaultLimits()
	maxAgents := c.Limits.MaxAgents
	if maxAgents == 0 {
		maxAgents = limits.MaxAgents
	}
	if c.Limits.MaxPersistentAgents > maxAgents {
		how := fmt.Sprintf("max_agents = %d", maxAgents)
		if c.Limits.MaxAgents == 0 {
			how = fmt.Sprintf("the default max_agents of %d", maxAgents)
		}
		return fmt.Errorf("[limits] max_persistent_agents = %d exceeds %s, "+
			"so the lower ceiling is the one that binds and this setting would do "+
			"nothing: raise max_agents too, or lower this",
			c.Limits.MaxPersistentAgents, how)
	}
	// A store that cannot hold one maximum-sized blob evicts everything the
	// moment it is used, which looks like attachments not working rather than
	// like a setting. The daemon refuses it; so must anything describing the
	// daemon.
	if min := int64(limits.MaxBlobSize); c.Limits.BlobStoreBytes > 0 && c.Limits.BlobStoreBytes < min {
		return fmt.Errorf("[limits] blob_store_bytes = %d is smaller than one "+
			"maximum-sized blob (%d): the store would evict every attachment as "+
			"soon as it held one, and the daemon refuses to start on it",
			c.Limits.BlobStoreBytes, min)
	}
	return nil
}

// validateMatch covers the settings the daemon silently ignores rather than
// refusing, each of which produced a complete-looking configuration around a
// setting that never took effect.
func (c Config) validateMatch() error {
	if c.Match.History < 0 {
		return fmt.Errorf("[match] history = %d: a window cannot be negative, and a "+
			"negative one is ignored rather than refused", c.Match.History)
	}
	if d := c.Match.Deadline; d != "" {
		if _, err := time.ParseDuration(d); err != nil {
			return fmt.Errorf("[match] deadline = %q is not a duration: write it like "+
				"\"5m\" or \"90s\". An unparseable one is replaced with the default, so "+
				"the setting reads as applied and is not: %w", d, err)
		}
	}
	if j := c.Match.AutoJoin; j != "" && !AutoJoinPolicies[j] {
		return fmt.Errorf("[match] auto_join = %q: use \"declared\" (default: certainty "+
			"joins, guesses are proposed), \"always\" or \"never\". An unknown value "+
			"behaves as \"declared\" rather than being refused", j)
	}
	return nil
}

func (c Config) validateSupervise() error {
	for _, d := range []struct{ key, raw string }{
		{"every", c.Supervise.Every.String()},
		{"quiet", c.Supervise.Quiet.String()},
		{"frozen", c.Supervise.Frozen.String()},
		{"min_age", c.Supervise.MinAge.String()},
	} {
		if strings.HasPrefix(d.raw, "-") {
			return fmt.Errorf("[supervise] %s = %s: a negative interval is ignored "+
				"rather than refused, so it reads as configured and does nothing",
				d.key, d.raw)
		}
	}
	// min_duty is a FRACTION of a process's life, and neither end of it was
	// checked. Settings.Apply takes the value only when it is above zero, so a
	// negative one reads as configured and silently keeps the default: the
	// thing this validation exists to stop. Above 1 is worse than ignored.
	// convictedByDutyCycle ACQUITS a process whose duty exceeds the threshold,
	// so a threshold no duty can reach acquits nobody, and every process past
	// min_age becomes eligible for a stuck verdict. Found by the pre-release
	// review, which also noted the test claiming to cover ignored settings had
	// no case for either end.
	// An explicit zero is a setting that does not take. Settings.Apply overlays
	// only values above zero, so every one of these reads as configured and
	// silently keeps the default.
	if c.set != nil {
		for _, key := range []string{"every", "quiet", "frozen", "min_age", "min_duty"} {
			if !c.set("supervise", key) {
				continue
			}
			zero := c.Supervise.MinDuty == 0
			switch key {
			case "every":
				zero = c.Supervise.Every == 0
			case "quiet":
				zero = c.Supervise.Quiet == 0
			case "frozen":
				zero = c.Supervise.Frozen == 0
			case "min_age":
				zero = c.Supervise.MinAge == 0
			}
			if zero {
				return fmt.Errorf("[supervise] %s is set to zero, which is not "+
					"\"no limit\": the daemon takes this setting only when it is "+
					"above zero, so it keeps the default and nothing says so. "+
					"Remove the line to mean the default", key)
			}
		}
	}
	// NaN passes every comparison below, because no comparison against NaN is
	// true, and is then ignored by the same `> 0` test. TOML has NaN.
	if math.IsNaN(c.Supervise.MinDuty) {
		return errors.New("[supervise] min_duty = nan: no comparison against nan is " +
			"true, so it passes every range check and is then ignored. It is a " +
			"fraction in [0, 1]")
	}
	if d := c.Supervise.MinDuty; d < 0 || d > 1 {
		return fmt.Errorf("[supervise] min_duty = %v: it is the FRACTION of its life "+
			"a process must have spent running, so it lives in [0, 1]. A negative "+
			"value is ignored and the default is kept; above 1 no process can clear "+
			"it, so every process old enough is convicted of being stuck", d)
	}
	return nil
}

func (c Config) validateWake() error {
	// A setting that reads as applied and is not is this file's oldest bug, and
	// [wake.exec] arrived with a fresh one: cooldown accepted any duration, and
	// the waker maps everything <= 0 to the 90s default. So `cooldown = "-1s"`
	// passed `dibd -check`, startup reported the harness configured, and the
	// operator's explicit value did nothing at all. Zero is documented as "use
	// the default" and stays legal; a negative one is a mistake and is refused.
	for harness, x := range c.Wake.Exec {
		if x.Cooldown < 0 {
			return fmt.Errorf("[wake.exec.%s] cooldown = %q: a wake cooldown cannot "+
				"be negative. Omit it, or set 0, to take the default; anything "+
				"below zero was silently becoming that default while reading as "+
				"a setting you had chosen", harness, x.Cooldown)
		}
		if len(x.Argv) > 0 && x.Argv[0] == "" {
			return fmt.Errorf("[wake.exec.%s] argv starts with an empty string, so "+
				"there is no program to run. The first element is the executable, "+
				"and the rest are its arguments: there is no shell in this path "+
				"to work out what was meant", harness)
		}
		// NOTHING AN AGENT SAID MAY CHOOSE THE PROGRAM.
		//
		// Whole-element substitution keeps a message body from being parsed as a
		// command, which is what makes the argv form safe. It does not make the
		// VALUES trustworthy: {agent}, {from} and {type} are derived from agents,
		// and argv[0] is handed straight to exec. A placeholder there would let a
		// peer's chosen name select the executable, which is a different rule
		// from quoting and the one this project actually states: the wake command
		// comes from the operator's file and nothing an agent said reaches it.
		if len(x.Argv) > 0 && strings.HasPrefix(x.Argv[0], "{") {
			return fmt.Errorf("[wake.exec.%s] argv[0] is %q: the program to run must "+
				"be named in this file and cannot be a placeholder. Substituted "+
				"values come from agents, and the one thing an agent must never "+
				"choose is which executable the board starts", harness, x.Argv[0])
		}
	}
	w := c.Wake.ExtendTurnFor
	if w == "" {
		return nil
	}
	for _, p := range WakePhases {
		if w == p {
			return nil
		}
	}
	return fmt.Errorf("[wake] extend_turn_for = %q: use \"all\" (default: anything "+
		"unread wakes the agent once), \"urgent\" (only work somebody is blocked on) "+
		"or \"none\" (never extend a turn)", w)
}
