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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

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
	Roles             RolesConfig       `toml:"roles"`              // standing coordinator/admin agents
	Wake              WakeConfig        `toml:"wake"`               // which news may extend an agent's turn
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

// RolesConfig is the [roles] table.
type RolesConfig struct {
	// Coordinator gets breadth without intrusion: broadcast, force-release,
	// merge and evict, but never another agent's mail.
	Coordinator []string `toml:"coordinator"`
	// Admin adds the god view, mail included. Grant it only to an agent trusted
	// as the operator trusts themselves.
	Admin []string `toml:"admin"`
	// Identity binds a declared name to the NONCE the operator gave that agent,
	// and it is what makes a standing role safe on a board with no history.
	//
	// A name on its own authenticates nobody. The pin file records whoever
	// registered under a declared name first and holds them to it afterwards,
	// which stops a later impostor and does nothing about the first one: an
	// agent that reads dibs.toml, or guesses that `admin = ["fleet-lead"]` is a
	// likely line, could register under that name before the operator's own
	// agent came up, become the durable pin, and be handed the god view
	// including every agent's mail. The startup window bounds the race; it
	// authenticates nothing. Found by the pre-release review.
	//
	// The nonce is the credential the agent already presents at register, and
	// the operator already chooses it. Writing the same value here is a shared
	// secret between the operator's config and the operator's agent, which an
	// impostor does not have. Only its fingerprint is ever compared.
	//
	//	[roles]
	//	admin = ["fleet-lead"]
	//	[roles.identity]
	//	fleet-lead = "the-nonce-you-give-that-agent"
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
		c.validateSupervise, c.validateWake,
	} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
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
	// Only when the file sets BOTH: with one unset the daemon's default is the
	// other side of the comparison, and that default is not ours to know.
	if c.Limits.MaxAgents > 0 && c.Limits.MaxPersistentAgents > c.Limits.MaxAgents {
		return fmt.Errorf("[limits] max_persistent_agents = %d exceeds max_agents = %d, "+
			"so the lower ceiling is the one that binds and this setting would do "+
			"nothing: raise max_agents too, or lower this",
			c.Limits.MaxPersistentAgents, c.Limits.MaxAgents)
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
	if d := c.Supervise.MinDuty; d < 0 || d > 1 {
		return fmt.Errorf("[supervise] min_duty = %v: it is the FRACTION of its life "+
			"a process must have spent running, so it lives in [0, 1]. A negative "+
			"value is ignored and the default is kept; above 1 no process can clear "+
			"it, so every process old enough is convicted of being stuck", d)
	}
	return nil
}

func (c Config) validateWake() error {
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
