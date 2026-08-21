package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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
func printJoinConfig(remote string) error {
	dir := filepath.Join(homeDir(), ".dibs-"+boardSlug(remote))
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"dibs": map[string]any{
				"command": self(),
				"args":    []string{"mcp-stdio"},
				"env": map[string]string{
					"DIBS_ADDR": remote,
					"DIBS_DIR":  dir,
				},
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

	// Step 2 is a different step for the two shapes a board comes in, and
	// printing both as conditionals leaves the operator deciding which sentence
	// applies to them. The address already says.
	if isLoopbackAddr(remote) {
		fmt.Printf(`# 2. That address is loopback, so it is this machine's end of an ssh
#    forward to the hub. Open it and leave it running:
#
#      ssh -N -L %s <user>@<hub>
#
#    The hub's daemon never leaves its own loopback, and ssh has authenticated
#    the machine before Dibs sees a byte.
#
`, shellArg(port(remote)+":"+remote))
	} else {
		// The trust step, which the bridge cannot do without.
		//
		// A non-loopback daemon serves HTTPS with a certificate it generated
		// itself, and the bridge trusts only what this machine recorded. Without
		// this the config below is complete-looking and the bridge rejects the
		// board on the first call. The older TLS recipe said so; this command was
		// written without it.
		fmt.Printf(`# 2. That address is not loopback, so the board serves HTTPS with a
#    certificate it generated itself. The bridge trusts only what this machine
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
`, q, shellArg(remote))
	}

	fmt.Printf(`# 3. Add to .mcp.json (Claude Code and JSON-config hosts):
%s

# Codex / ChatGPT desktop, in ~/.codex/config.toml:
[mcp_servers.dibs]
command = %q
args = ["mcp-stdio"]
env = { DIBS_ADDR = %q, DIBS_DIR = %q, CODEX_MCP_PROTOCOL_VERSION = "2026-07-28" }

# stdio, not the url form, and from another machine that matters MORE rather
# than less: this session is the long-lived unattended one, and a url client
# holds no nonce, so every reconnect forks an identity that cannot read its
# predecessor's mail. The bridge keeps the nonce in DIBS_DIR above, which is
# what makes a returning session the same agent.
#
# Check it: dibs doctor, with the same two variables set.
`, string(out), self(), remote, dir)
	return nil
}

// isLoopbackAddr reports whether an address names this machine, which is what
// the near end of an ssh forward looks like.
func isLoopbackAddr(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		h = addr
	}
	if h == "" || h == "localhost" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// homeDir is the operator's home, for a path they can paste. An empty string
// would silently produce a config rooted at "/", so say what went wrong.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "/home/you"
	}
	return h
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
	h, port, err := net.SplitHostPort(addr)
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
	return slug
}
