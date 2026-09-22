package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/supgang"
	xport "github.com/agenxy/dibs/internal/transport"
)

// printJoinConfig is `dibs mcp-config --board <addr>`: the config for joining
// SOMEBODY ELSE'S board from this machine.
//
// The rest of this command answers "how do agents reach the daemon I am running",
// which leaves the fleet case to be assembled from three separate blocks by an
// operator who does not yet know that is what they are doing. This one answers
// the other question directly, and it is the question a second machine has.
//
// It deliberately does not read local.secret: the secret needed here belongs to
// the REMOTE board and this machine may have no daemon of its own at all.
func printJoinConfig(remote string) error { return printJoinConfigFor(remote, nil) }

// joinBoard is `dibs mcp-config --board <peer|address>`. A Supgang peer
// first: the hub named the way the fleet names it, resolved to the address
// it has signed now. Then a raw address, for a board reached some other way
// (an ssh forward, a machine with no Supgang), which stays exactly as it was.
func joinBoard(board string) error {
	remote, peer, err := resolveBoardPeer(board)
	if err == nil {
		return printJoinConfigFor(remote, peer)
	}
	if !errors.Is(err, errNotAPeer) {
		return err
	}
	if err := checkBoardAddr(board); err != nil {
		return err
	}
	return printJoinConfig(board)
}

// errNotAPeer is resolveBoardPeer's answer to a --board that is an address
// (or nothing Supgang knows), so the raw-address path takes over.
var errNotAPeer = errors.New("not a supgang peer")

// resolveBoardPeer reads `--board <peer>[:port]` as a Supgang member and
// returns the https address to join it at now, and the peer. A string that
// parses as an address, or that Supgang does not know, is errNotAPeer; a
// Supgang that is absent or not initialised is the same answer, because the
// raw-address path is then the only one there is.
func resolveBoardPeer(board string) (remote string, peer *supgang.Peer, err error) {
	if strings.Contains(board, "://") || net.ParseIP(strings.Trim(board, "[]")) != nil {
		return "", nil, errNotAPeer
	}
	name, port, explicit := board, defaultPort, false
	if h, p, serr := net.SplitHostPort(board); serr == nil {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 65535 {
			return "", nil, fmt.Errorf("dibs mcp-config --board: %q is not a port", p)
		}
		// The number, not the spelling: `:04777` is port 4777, and the
		// advertisement it is compared against says "4777".
		name, port, explicit = h, strconv.Itoa(n), true
	}
	if net.ParseIP(name) != nil {
		return "", nil, errNotAPeer
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, rerr := supgang.Resolve(ctx, name)
	if rerr != nil {
		return "", nil, peerLookupFailure(board, rerr)
	}
	addr := p.Address()
	if addr == "" {
		return "", nil, fmt.Errorf("supgang knows %s (%s) but has no route-compatible address for it right now",
			p.Name, p.Fingerprint)
	}
	// The port the hub ADVERTISES, when it does: that computer signed which
	// port its Dibs listens on, so the default is only a guess it has
	// superseded. `--board <peer>:<port>` still names another daemon there.
	if svc, ok := p.Service(supgang.ServiceName); ok && !explicit && svc.Valid() == nil {
		port = strconv.Itoa(svc.Port)
	}
	host, _, serr := net.SplitHostPort(addr)
	if serr != nil {
		return "", nil, serr
	}
	remote = "https://" + net.JoinHostPort(host, port)
	if err := checkBoardAddr(remote); err != nil {
		return "", nil, err
	}
	return remote, &p, nil
}

// peerLookupFailure decides what a failed Supgang lookup means for --board.
// Exactly two answers hand the word back to the address path: no Supgang here
// at all, and a Supgang that knows no such peer (a Supgang that is installed
// and not initialised is the first of those, said out loud). Anything else
// (an ambiguous name, a Supgang that could not answer) is reported, because
// turning "ambiguous" into a DNS name would pick a destination the operator
// did not name; Supgang's own rule is that ambiguity fails closed.
func peerLookupFailure(board string, err error) error {
	if errors.Is(err, supgang.ErrNotInstalled) {
		return errNotAPeer
	}
	var se *supgang.Error
	if !errors.As(err, &se) {
		return fmt.Errorf("dibs mcp-config --board %s: %w", board, err)
	}
	switch {
	case strings.Contains(se.Message, "no known peer"):
		return errNotAPeer
	case strings.Contains(se.Message, "state directory"):
		fmt.Fprintf(os.Stderr, "# Supgang is installed here but not initialised (%s), so %q is read as an address.\n",
			se.Message, board)
		return errNotAPeer
	}
	return fmt.Errorf("dibs mcp-config --board %s: %w", board, err)
}

// defaultPort is the port a hub listens on unless its dibs.toml says otherwise;
// a hub that advertises its port through Supgang supersedes it, and
// `--board <peer>:<port>` names another.
const defaultPort = "4777"

// pinOutcome is what the join recipe knows about the hub's certificate after
// asking Supgang: the key its computer signed (pin, empty when it advertised
// none for the port being joined), whether the certificate it serves was
// checked against that key and recorded (recorded), and why not when the pin
// is known and it was not (reason).
type pinOutcome struct {
	pin      string
	recorded bool
	reason   string
}

// pinFromPeer turns a hub's Supgang advertisement into a recorded certificate.
//
// When the peer advertises `dibs` on the port being joined, the hub is
// dialled, the certificate it serves is compared with the signed key, and on
// a match it is recorded in dir, the directory the printed config names, so
// the recipe's trust step is already done. A hub that does not answer is not
// an error: the recipe then carries the pin into the `dibs trust` line. A hub
// that answers with another key IS: the recipe is refused rather than printed
// with a step that would record an impostor.
func pinFromPeer(dir, remote string, peer *supgang.Peer) (pinOutcome, error) {
	svc, ok := peer.Service(supgang.ServiceName)
	if !ok {
		return pinOutcome{}, nil
	}
	// A broken advertisement is a fault on the hub, named: falling back to
	// the unpinned ceremony would turn it into a join that looks ordinary.
	if err := svc.Valid(); err != nil {
		return pinOutcome{}, fmt.Errorf("dibs mcp-config --board %s: %w; run `dibs fingerprint` on that "+
			"machine and advertise the pin it prints", peer.Name, err)
	}
	if strconv.Itoa(svc.Port) != port(remote) {
		return pinOutcome{}, nil
	}
	if _, err := trustPinned(dir, hostPort(remote), svc.KeyPin); err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) || strings.Contains(err.Error(), "could not reach") {
			// The dial error alone: the address is already in the sentence.
			_, reason, _ := strings.Cut(err.Error(), ": ")
			return pinOutcome{pin: svc.KeyPin, reason: reason}, nil
		}
		return pinOutcome{}, fmt.Errorf("dibs mcp-config --board %s: %w", peer.Name, err)
	}
	return pinOutcome{pin: svc.KeyPin, recorded: true}, nil
}

// joinDirFor is the credential directory for a board: named after the address
// for a raw address, and after the peer's fingerprint AND the port for a
// peer, because two daemons on one computer are two boards with two secrets,
// and a directory per peer alone would have the second's secret overwrite
// the first's. Empty when the home directory cannot be resolved.
func joinDirFor(remote string, peer *supgang.Peer) string {
	home, err := homeDir()
	if err != nil {
		return ""
	}
	if peer == nil {
		return filepath.Join(home, ".dibs-"+boardSlug(remote))
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(remote, "https://"))
	return filepath.Join(home, ".dibs-"+boardSlug(peer.Fingerprint+":"+port))
}

// printJoinConfigFor is printJoinConfig, naming the hub as a Supgang peer when
// it was given as one: the bridge then re-resolves the hub's address on every
// start, and the printed recipe says where the address came from.
func printJoinConfigFor(remote string, peer *supgang.Peer) error {
	dir := joinDirFor(remote, peer)
	if dir == "" {
		_, herr := homeDir()
		return herr
	}
	env := map[string]string{
		"DIBS_ADDR": remote,
		"DIBS_DIR":  dir,
	}
	var pinned pinOutcome
	if peer != nil {
		env[boardPeerEnv] = peer.NodeID
		fmt.Printf("# %s is the Supgang member %s (%s): the address below is the one it has\n"+
			"# signed now, and the bridge asks Supgang again each time it starts, so the\n"+
			"# board follows that computer when its address changes.\n#\n",
			peer.Name, peer.Fingerprint, peer.NodeID)
		var err error
		if pinned, err = pinFromPeer(dir, remote, peer); err != nil {
			return err
		}
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"dibs": map[string]any{
				"command": self(),
				"args":    []string{"mcp-stdio"},
				"env":     env,
			},
		},
	}
	// Quoted, because a home directory may contain a space.
	//
	// The JSON and TOML forms carry the path as a value and are fine; these are
	// shell lines an operator pastes, and unquoted they would have mkdir, scp
	// and trust act on a split argument. shellArg already exists for exactly
	// this, on the service unit.
	q := shellArg(dir)
	qsecret := shellArg(filepath.Join(dir, "local.secret"))
	out, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf(`# Joining the board at %s from this machine.
#
# 1. Make the data directory and copy that board's secret into it. The secret
#    is per-board and read from the data directory, so a machine that also runs
#    its own board needs this second directory; there is no way to hold two
#    secrets in one.
#
#      mkdir -p %s && chmod 700 %s
#      scp '<hub>:<hub-data-dir>/local.secret' %s
#
#    <hub-data-dir> is ~/.dibs unless that machine sets DIBS_DIR. Only the hub
#    knows: `+"`dibs doctor`"+` prints it on the first line there. A hub running more
#    than one board has more than one, and copying the wrong one gives a
#    credential that authenticates against a board nobody meant.
#
`, remote, q, q, qsecret)

	// Both, when both apply.
	//
	// This was a switch, which reads as "one of these" and quietly dropped a
	// real combination: `https://127.0.0.1:5777` needs a forward AND a
	// certificate recorded, and the tunnel arm won, so the printed
	// configuration was complete-looking and rejected the board's certificate.
	// boardShape had computed both answers correctly; the branch below threw
	// one away.
	tunnel, trust := boardShape(remote, "")
	if tunnel {
		// The hub's port is NOT this one.
		//
		// The two ends of a forward are independent: the local port is whatever
		// is free here, the far port is whatever the hub listens on. Printing
		// `-L 5777:127.0.0.1:5777` for `--board 127.0.0.1:5777` assumes they
		// match, and when they do not the tunnel comes up and forwards to
		// nothing, which is the worst way for this to fail: ssh reports success
		// and the board is simply unreachable.
		fmt.Printf(`# 2%s. That address is loopback, so it is this machine's end of an ssh
#    forward to the hub. Open it and leave it running:
#
#      ssh -N -L %s:127.0.0.1:<hub-port> <user>@<hub>
#
#    <hub-port> is whatever the HUB's daemon listens on, usually 4777, and is
#    unrelated to %s above: that is this machine's end, and only has to be free
#    here. `+"`dibs doctor`"+` on the hub prints its address.
#
#    The hub's daemon never leaves its own loopback, and ssh has authenticated
#    the machine before Dibs sees a byte.
#
`, tunnelStepSuffix(trust), port(remote), port(remote))
	}
	switch {
	case trust && pinned.recorded:
		// Nothing for a person to compare: the hub's computer signed its
		// Dibs's key through Supgang, and the certificate it serves now
		// carries that key, so it was recorded here on the way.
		fmt.Printf(`# 2%s. The board serves HTTPS with a certificate it generated itself, and
#    %s signed, through Supgang, the key its Dibs serves. The certificate
#    %s presents carries that key (pin %s...), so it is recorded in
#    %s already. Nothing to compare by hand.
#
`, trustStepSuffix(tunnel), peer.Name, hostPort(remote), pinned.pin[:16], q)
	case trust && pinned.pin != "":
		// The key is known and signed; only the hub did not answer just now.
		// The command below makes the same comparison when it does.
		fmt.Printf(`# 2%s. The board serves HTTPS with a certificate it generated itself, and
#    %s signed, through Supgang, the key its Dibs serves. %s did not
#    answer just now (%s), so record the certificate when it does; the
#    check against the signed key is made for you:
#
#      DIBS_DIR=%s dibs trust %s --pin %s
#
#    DIBS_DIR is not optional there: trust records the certificate in the data
#    directory it is given, and the bridge reads it from the one in the config
#    below.
#
`, trustStepSuffix(tunnel), peer.Name, hostPort(remote), pinned.reason, q, shellArg(hostPort(remote)), pinned.pin)
	case trust:
		// The trust step, which the bridge cannot do without.
		//
		// A non-loopback daemon serves HTTPS with a certificate it generated
		// itself, and the bridge trusts only what this machine recorded. Without
		// this the config below is complete-looking and the bridge rejects the
		// board on the first call. The older TLS recipe said so; this command was
		// written without it.
		fmt.Printf(`# 2%s. The board serves HTTPS with a certificate it generated
#    itself. The bridge trusts only what this machine
#    has recorded, so record it once:
#
#      DIBS_DIR=%s dibs trust %s
#
#    DIBS_DIR is not optional there: trust records the certificate in the data
#    directory it is given, the bridge reads it from the one in the config
#    below, and without it trust reports success while writing somewhere the
#    bridge never looks.
#
#    Compare the fingerprint it prints against `+"`dibs fingerprint`"+` run on the hub;
#    they must match. Only the bridge's own trust store changes, so nothing
#    else on this machine has its TLS behaviour altered.
#
`, trustStepSuffix(tunnel), q, shellArg(hostPort(remote)))
		if peer != nil && peer.ServicesKnown {
			fmt.Printf(`#    That comparison is a ceremony Supgang can end: once the hub's operator
#    advertises the board's key there (` + "`dibs fingerprint`" + ` on the hub prints the
#    exact command), this recipe checks the certificate against it for you.
#
`)
		}
	}
	if !tunnel && !trust {
		// Reachable directly and serving plaintext, because the operator said
		// so with an explicit scheme. Nothing to trust and nothing to forward.
		fmt.Printf(`# 2. That address is reachable directly and names plaintext, so there is
#    no certificate to record and no forward to open. Nothing about that
#    daemon is protected by the network it is on: keep it on one you trust.
#
`)
	}

	fmt.Printf(`# 3. Add to .mcp.json (Claude Code and JSON-config hosts):
%s

# Codex / ChatGPT desktop, in ~/.codex/config.toml. BOTH parts: the feature
# alone leaves the connection on 2025-06-18.
[features]
mcp_2026_07_28 = true

[mcp_servers.dibs]
command = %q
args = ["mcp-stdio"]
env = %s

# stdio, not the url form, and from another machine that matters MORE rather
# than less: this session is the long-lived unattended one. register hands
# back a nonce on every transport, and a client that keeps it reattaches as
# the same agent; the bridge keeps it FOR the session, in DIBS_DIR above,
# across restarts and upgrades. A url client that drops its nonce registers
# again as a sibling that cannot read its predecessor's mail.
#
# Check it: dibs doctor, with the same two variables set.
`, string(out), self(), codexEnv(env))
	return nil
}

// codexEnv is the Codex stanza's env table, built from the SAME map the JSON
// block was, plus the protocol pin only Codex needs. The stanza used to spell
// its own two variables, so a hub given as a Supgang peer reached the JSON
// config with DIBS_BOARD_PEER and the Codex config without it: that bridge
// was fixed to the address the recipe printed and could not follow the hub
// when it moved, which the recipe had just promised. Round eighteen of the
// pre-release review.
func codexEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		parts = append(parts, k+" = "+strconv.Quote(env[k]))
	}
	parts = append(parts, `CODEX_MCP_PROTOCOL_VERSION = "2026-07-28"`)
	return "{ " + strings.Join(parts, ", ") + " }"
}

// boardShape decides which second step an address calls for: a forward, or
// recording a certificate.
//
// One place, reading the address AS GIVEN, because there are three signals and
// they were being read in two. A scheme, when present, is the operator saying
// what the daemon serves and settles it outright: `http://` is the documented
// way to reach a deliberately plaintext daemon off loopback, and telling that
// operator to trust a certificate it does not serve sends them to a command
// that cannot succeed. Without a scheme, loopback means a forward and anything
// else means the HTTPS a daemon off loopback serves by default.
// served is the transport the daemon RESOLVED, "http" or "https", or "" when
// the caller does not know. When it is known it decides `trust`, because the
// address can only ever guess: a bare loopback address with tls_cert
// configured serves HTTPS and was described as plaintext, so the recipe
// omitted the trust step and the joining bridge was told to speak the wrong
// protocol. `insecure_plaintext` on a LAN address is the same mistake
// inverted. Found by the pre-release review.
//
// `tunnel` stays address-derived, because reachability is a property of what
// the daemon BINDS and not of what it speaks.
func boardShape(addr, served string) (tunnel, trust bool) {
	scheme, rest, hasScheme := strings.Cut(addr, "://")
	scheme = strings.ToLower(scheme)
	if !hasScheme {
		rest = addr
	}
	// The shared rule, not a copy of it.
	//
	// This used to re-derive loopback here and got the wildcard bind wrong:
	// `:4777` means every interface, which is the one shape that is definitely
	// reachable from another machine, and it was classified as confined to this
	// one, so a board bound wide was handed an ssh-forward recipe instead of the
	// direct one. The comment that recorded the fix said "the shared transport
	// code already reads it correctly, so the two disagreed", and then kept the
	// copy. Calling the shared one is what stops the next disagreement.
	loopback := xport.IsLoopback(rest)
	if served != "" {
		return loopback, served == "https"
	}
	if hasScheme {
		return loopback, scheme == "https"
	}
	return loopback, !loopback
}

// homeDir is the operator's home, for a path they can paste.
//
// It returns an error rather than a placeholder. It used to answer "/home/you"
// when the home directory could not be resolved, which on a headless host
// produced a complete, confident recipe of mkdir, scp, trust and JSON all
// rooted at a literal path that is nobody's home and may well be somebody
// else's directory, with nothing in the output saying it was a stand-in.
// Refusing costs an operator one environment variable; the placeholder cost
// them a setup that silently targets the wrong tree.
func homeDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "", fmt.Errorf("cannot resolve a home directory to put this board's "+
			"credential in (%v). Set HOME, or make the directory yourself and pass it "+
			"as DIBS_DIR to the bridge", err)
	}
	return h, nil
}

// boardSlug names the data directory after the board it holds the secret for,
// so a machine on three boards has three directories it can tell apart.
//
// The whole address, with nothing invented and nothing tidied.
//
// This collided three times, each fix keeping one more character and each
// leaving the claim above still false: the port was dropped for non-loopback,
// so two daemons on one host shared a directory; then dots became hyphens, so
// hub.example and hub-example did; then loopback was renamed "board", so
// 127.0.0.1:4777 collided with the ordinary hostname board:4777.
//
// The pattern was rewriting the address into something that reads nicely.
// Every such rewrite maps two addresses onto one name somewhere, and the next
// character is found by whoever has two boards rather than by me. So: keep the
// address, replace only the separators that would change the path, and accept
// a less pretty directory name. Brackets stay, because they are what tells an
// IPv6 literal from a hostname spelled like one.
func boardSlug(addr string) string {
	// The scheme says HOW to reach a board, not WHICH board it is: http://hub
	// and https://hub are one daemon on one address, and giving them separate
	// credential directories would have an operator copy the same secret twice
	// and wonder which one is live.
	h, port, err := net.SplitHostPort(hostPort(addr))
	if err != nil {
		h, port = addr, ""
	}
	if h == "" {
		h = "localhost"
	}
	slug := strings.NewReplacer(":", "-", "/", "-", string(filepath.Separator), "-").Replace(h)
	if strings.Contains(h, ":") {
		slug = "[" + slug + "]"
	}
	if port != "" {
		slug += "-" + port
	}
	// Belt and braces: this names a directory that then has a secret written
	// into it, and checkBoardAddr is one caller rather than a property of the
	// function. A slug that is a path is a slug that escapes the home directory
	// it is joined to.
	slug = strings.NewReplacer("/", "-", `\`, "-", "..", "-").Replace(slug)
	if slug == "" || slug == "." {
		return "board"
	}
	return slug
}

// hostPort is an address with any scheme removed, for the commands that dial
// rather than configure. `dibs trust` is one: a scheme reaches tls.Dial as part
// of the host and fails with "too many colons in address".
func hostPort(a string) string {
	if _, rest, found := strings.Cut(a, "://"); found {
		return rest
	}
	return a
}

// trustStepSuffix labels the trust step "2b" when a forward is step 2a, so two
// steps that both print are not both called 2.
func trustStepSuffix(afterTunnel bool) string {
	if afterTunnel {
		return "b"
	}
	return ""
}

// tunnelStepSuffix labels the forward "2a" when a trust step follows it.
func tunnelStepSuffix(beforeTrust bool) string {
	if beforeTrust {
		return "a"
	}
	return ""
}
