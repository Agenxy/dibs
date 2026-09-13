// Package supgang is how Dibs asks Supgang who this computer is and where
// the others are.
//
// Supgang (github.com/Agenxy/supgang) is the Agenxy address plane: one
// self-certifying identity per computer, root-signed membership, and fresh
// device-signed addresses exchanged between members even when the addresses
// change. Dibs does none of that itself. Across machines a Dibs board needs
// exactly four things from it: which computer a caller is (`HostID`), which
// computer a human means by a name, where that computer can be reached right
// now, and which TLS key the Dibs on it serves (a service advertisement, a
// claim that computer signed: Supgang ADR 0002). This package is the whole
// of Dibs's dependency on it: the `supgang` binary, spoken to over argv with
// `--json`, whose envelopes are versioned (`supgang.status/v4`,
// `supgang.peers/v5` and `v6`, `supgang.resolve/v4` and `v5`; the later
// majors add `services` and change nothing else) and checked here, so a
// Supgang that changes its answer is a named error rather than a wrong host
// id.
//
// One machine needs none of this: a board on loopback identifies its callers
// by the loopback itself. Supgang is consulted when a bridge joins a hub on
// another computer, when a hub stamps the machine it is on, and when doctor
// names a host. Absent, the answers are empty and Dibs behaves as it did
// before any of this existed, which docs/NETWORK.md §2 states as the rule for
// an unknown host.
package supgang

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/sibling"
)

// Command is the executable consulted; a test points it at a stand-in.
var Command = "supgang"

// callTimeout bounds one invocation. Every answer here is read from local
// state the running service already holds; a Supgang that takes longer than
// this is not answering.
const callTimeout = 5 * time.Second

// maxOutput bounds what is read back. A peers listing for a large hive is
// tens of kilobytes; a megabyte is a program that is not Supgang.
const maxOutput = 1 << 20

// Identity is this computer, as Supgang knows it.
type Identity struct {
	NodeID      string `json:"node_id"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
	HiveID      string `json:"hive_id"`
	Service     string `json:"service"`
}

// Candidate is one address a computer can currently be reached at, in the
// order Supgang prefers them. Address is host:port of SUPGANG's own listener
// on that computer, which is the host to reach and not the port.
type Candidate struct {
	Scope           string `json:"scope"`
	Kind            string `json:"kind"`
	Transport       string `json:"transport"`
	Address         string `json:"address"`
	Provenance      string `json:"provenance"`
	RouteCompatible bool   `json:"route_compatible"`
	Preferred       bool   `json:"preferred"`
}

// Peer is one computer in the hive, with the addresses Supgang has signed
// for it and the services it says it runs there.
type Peer struct {
	Name        string      `json:"name"`
	Tags        []string    `json:"tags"`
	Fingerprint string      `json:"fingerprint"`
	NodeID      string      `json:"node_id"`
	Connected   bool        `json:"connected"`
	Status      string      `json:"status"`
	ExpiresAt   int64       `json:"expires_at"`
	Candidates  []Candidate `json:"addresses"`
	Services    []Service   `json:"services"`
	// ServicesKnown is whether the Supgang that answered carries service
	// advertisements at all (peers/v6, resolve/v5). An empty Services with
	// it false means an older Supgang, not a computer that advertises
	// nothing, and the two call for different advice.
	ServicesKnown bool `json:"-"`
}

// Service is one thing a computer says it runs, signed by that computer's
// device: its name, the port at the computer's addresses, and the SHA-256
// of the TLS public key it presents, as 64 hex digits. A claim, never an
// observation: Supgang has not dialled it. Dibs advertises itself as `dibs`
// with the pin of its board CA's SubjectPublicKeyInfo.
type Service struct {
	Name   string `json:"name"`
	Port   int    `json:"port"`
	KeyPin string `json:"key_pin"`
}

// ServiceName is what Dibs advertises itself as.
const ServiceName = "dibs"

// Service finds the advertisement with a name, if the computer made one.
func (p Peer) Service(name string) (Service, bool) {
	for _, s := range p.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// Valid says whether the advertisement is the shape one has: a name, a
// port, and a pin that is a key. Rows are kept as Supgang gave them and
// judged where they are used, because a malformed advertisement for the
// service a caller is looking for is a fault to report, not an absence to
// fall back from: dropping it would turn a broken hub into an unpinned join.
func (s Service) Valid() error {
	if s.Name == "" {
		return errors.New("supgang carries a service advertisement with no name")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("supgang advertises %s on port %d, which is not a port", s.Name, s.Port)
	}
	if err := checkKeyPin(s.KeyPin); err != nil {
		return fmt.Errorf("supgang's advertisement of %s carries a %w", s.Name, err)
	}
	return nil
}

// checkKeyPin refuses anything but 64 lowercase hex digits.
func checkKeyPin(pin string) error {
	if len(pin) != 64 {
		return fmt.Errorf("key pin %q is not 64 hex digits", pin)
	}
	for _, c := range pin {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("key pin %q is not lowercase hex", pin)
		}
	}
	return nil
}

// ErrNotInstalled is the answer when no `supgang` is on this machine.
var ErrNotInstalled = errors.New("supgang is not installed on this machine")

// Error is what Supgang itself said went wrong, verbatim: "local control
// state directory failed validation" is its phrase for a computer that has
// not run `supgang init`, and its words are better than a paraphrase.
type Error struct{ Message string }

func (e *Error) Error() string { return "supgang: " + e.Message }

// Available reports whether the binary is on this machine at all. Not
// whether it is initialised: Status answers that, with Supgang's own words.
func Available() bool { return executable() != "" }

// executable is where the binary is: Command when it names a path or is on
// PATH, else where the Agenxy installers put it (internal/sibling), which a
// service's PATH does not list.
func executable() string {
	if strings.ContainsRune(Command, os.PathSeparator) {
		if _, err := os.Stat(Command); err == nil {
			return Command
		}
		return ""
	}
	return sibling.Find(Command)
}

// Status is this computer's identity.
func Status(ctx context.Context) (Identity, error) {
	var out struct {
		envelope
		Identity
	}
	if err := call(ctx, &out, "status"); err != nil {
		return Identity{}, err
	}
	if _, err := out.check("supgang.status/", 4); err != nil {
		return Identity{}, err
	}
	if err := checkNodeID(out.NodeID); err != nil {
		return Identity{}, err
	}
	return out.Identity, nil
}

// checkNodeID refuses anything but the shape a Supgang node id has: 64 hex
// digits. An empty or odd id in a schema-valid answer would otherwise be
// stamped on every agent of a machine, and the daemon once sliced it.
func checkNodeID(id string) error {
	if len(id) != 64 {
		return fmt.Errorf("supgang answered with node id %q, which is not the 64 hex digits a node id has", id)
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("supgang answered with node id %q, which is not lowercase hex", id)
		}
	}
	return nil
}

// Peers is this computer and every other member, with signed addresses.
func Peers(ctx context.Context) (self Peer, peers []Peer, err error) {
	var out struct {
		envelope
		This  Peer   `json:"this_computer"`
		Peers []Peer `json:"peers"`
	}
	if err := call(ctx, &out, "peers"); err != nil {
		return Peer{}, nil, err
	}
	major, err := out.check("supgang.peers/", 5, 6)
	if err != nil {
		return Peer{}, nil, err
	}
	if err := checkNodeID(out.This.NodeID); err != nil {
		return Peer{}, nil, err
	}
	out.This.ServicesKnown = major >= 6
	// A peer with a malformed id is dropped rather than failing the listing:
	// the rest of the hive is still worth knowing about, and doctor names
	// what it can.
	kept := out.Peers[:0]
	for _, p := range out.Peers {
		if checkNodeID(p.NodeID) == nil {
			p.ServicesKnown = major >= 6
			kept = append(kept, p)
		}
	}
	return out.This, kept, nil
}

// Resolve is one computer, by name, local tag, fingerprint or node id, with
// fresh signed addresses.
func Resolve(ctx context.Context, peer string) (Peer, error) {
	peer = strings.TrimSpace(peer)
	if peer == "" || strings.HasPrefix(peer, "-") {
		return Peer{}, fmt.Errorf("supgang: %q is not a peer name, tag, fingerprint or node id", peer)
	}
	// Not an embedded Peer: resolve's envelope and a peer both carry a
	// `status`, and they mean different things.
	var out struct {
		envelope
		Name        string      `json:"name"`
		Tags        []string    `json:"tags"`
		Fingerprint string      `json:"fingerprint"`
		NodeID      string      `json:"node_id"`
		ExpiresAt   int64       `json:"expires_at"`
		Candidates  []Candidate `json:"candidates"`
		Services    []Service   `json:"services"`
	}
	if err := call(ctx, &out, "resolve", peer); err != nil {
		return Peer{}, err
	}
	major, err := out.check("supgang.resolve/", 4, 5)
	if err != nil {
		return Peer{}, err
	}
	if err := checkNodeID(out.NodeID); err != nil {
		return Peer{}, err
	}
	return Peer{
		Name: out.Name, Tags: out.Tags, Fingerprint: out.Fingerprint, NodeID: out.NodeID,
		ExpiresAt: out.ExpiresAt, Candidates: out.Candidates,
		Services: out.Services, ServicesKnown: major >= 5,
	}, nil
}

// Address picks the address to dial for a peer: the candidate Supgang
// prefers, else the first it considers route-compatible. Empty when none.
func (p Peer) Address() string {
	for _, c := range p.Candidates {
		if c.Preferred && c.RouteCompatible {
			return c.Address
		}
	}
	for _, c := range p.Candidates {
		if c.RouteCompatible {
			return c.Address
		}
	}
	return ""
}

// envelope is the part every Supgang JSON answer carries.
type envelope struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
	Err    string `json:"error"`
}

// check refuses an answer whose schema is not one this code reads, so a
// Supgang that renamed a field is a named error here and not a wrong host id
// on a board. The major is what a schema's `/vN` promises; more than one is
// accepted when the later ones only added fields, and the one that answered
// is returned so the caller knows which fields to believe.
func (e envelope) check(prefix string, majors ...int) (int, error) {
	if e.Status == "error" {
		return 0, &Error{Message: e.Err}
	}
	for _, major := range majors {
		if e.Schema == fmt.Sprintf("%sv%d", prefix, major) {
			if e.Status != "ok" {
				return 0, fmt.Errorf("supgang answered with status %q, which is neither ok nor error", e.Status)
			}
			return major, nil
		}
	}
	return 0, fmt.Errorf("supgang answered with schema %q, and this build of dibs reads %sv%d: "+
		"update whichever is older", e.Schema, prefix, majors[len(majors)-1])
}

func call(ctx context.Context, into any, args ...string) error {
	exe := executable()
	if exe == "" {
		return ErrNotInstalled
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	// #nosec G204 -- argv, no shell: a fixed executable and Supgang's own
	// subcommands; the one caller-supplied word (a peer) is refused above
	// when it could read as a flag.
	cmd := exec.CommandContext(ctx, exe, append([]string{"--json"}, args...)...)
	out, errOut := &limitedWriter{left: maxOutput}, &limitedWriter{left: 4096}
	cmd.Stdout, cmd.Stderr = out, errOut
	// WaitDelay bounds the wait for the PIPES, not only the process. A child
	// that started a descendant with the same stdout would otherwise keep
	// Run blocked past the deadline for as long as the descendant lived,
	// which for a boot-time identification is the daemon not starting.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if out.truncated {
		return fmt.Errorf("supgang %s answered with more than %d bytes, which is not an answer", args[0], maxOutput)
	}
	// A process that did not finish did not answer, whatever it printed: the
	// deadline, or the wait for a pipe a descendant held. Only then is a
	// refusal read: Supgang prints a JSON error envelope AND exits 3 for "no
	// known peer", and that envelope's sentence is the answer. A non-zero
	// exit with anything else on stdout (an ok envelope, nothing, garbage)
	// is not an answer either: a missing library, a refused state directory,
	// a process that printed a result and then failed.
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		return fmt.Errorf("supgang %s did not finish within %s: %w", args[0], callTimeout, err)
	}
	if refusal, ok := supgangRefusal(out.Bytes()); ok {
		return refusal
	}
	if err != nil {
		return fmt.Errorf("supgang %s: %w: %s", args[0], err, strings.TrimSpace(errOut.String()))
	}
	if out.Len() == 0 {
		return fmt.Errorf("supgang %s answered nothing", args[0])
	}
	if jerr := json.Unmarshal(out.Bytes(), into); jerr != nil {
		return fmt.Errorf("supgang %s did not answer in JSON: %w", args[0], jerr)
	}
	return nil
}

// supgangRefusal reads an error envelope (`supgang.error/v1`); ok is false
// when the output is not one (not JSON, or an answer), which the caller then
// reports in its own terms.
func supgangRefusal(raw []byte) (*Error, bool) {
	// Validity first, as a fact rather than an error: a truncated document
	// can fill `status` and `schema` before the decoder gives up, and that
	// must not read as a refusal.
	if !json.Valid(raw) {
		return nil, false
	}
	var e envelope
	_ = json.Unmarshal(raw, &e) // valid JSON of another shape leaves the fields empty
	if e.Status != "error" || !strings.HasPrefix(e.Schema, "supgang.error/") {
		return nil, false
	}
	return &Error{Message: e.Err}, true
}

// limitedWriter keeps the first `left` bytes, drops the rest, and remembers
// that it did: a truncated answer is refused rather than parsed as a prefix.
type limitedWriter struct {
	bytes.Buffer
	left      int
	truncated bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n > l.left {
		l.truncated = true
		p = p[:l.left]
	}
	l.left -= len(p)
	l.Buffer.Write(p)
	return n, nil
}
