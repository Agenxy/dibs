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
	out, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf(`# Joining the board at %s from this machine.
#
# 1. Make the data directory and copy that board's secret into it. The secret
#    is per-board and read from the data directory, so a machine that also runs
#    its own board needs this second directory; there is no way to hold two
#    secrets in one.
#
#      mkdir -p %s && chmod 700 %s
#      scp <hub>:<hub-data-dir>/local.secret %s/local.secret
#
#    <hub-data-dir> is ~/.dibs unless that machine sets DIBS_DIR. Only the hub
#    knows: `+"`dibs doctor`"+` prints it on the first line there. A hub running more
#    than one board has more than one, and copying the wrong one gives a
#    credential that authenticates against a board nobody meant.
#
`, remote, dir, dir, dir)

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
`, port(remote)+":"+remote)
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
`, dir, remote)
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
// The PORT is always part of it. It was kept only for loopback, on the
// reasoning that a forwarded port is the only thing distinguishing one tunnel
// from another, which is true and not the whole set: two daemons on one host
// are a configuration Dibs supports deliberately, and both resolved to the same
// directory. Copying the second board's secret would then overwrite the first
// board's, while each generated config claimed the shared directory was its
// own. A credential store keyed by less than the address it serves is a
// collision waiting for the operator who has two boards.
func boardSlug(addr string) string {
	h, port, err := net.SplitHostPort(addr)
	if err != nil {
		h, port = addr, ""
	}
	if h == "" || h == "127.0.0.1" || h == "localhost" || h == "::1" {
		h = "board"
	}
	// Dots are KEPT. Rewriting them to hyphens was cosmetic and it collided:
	// hub.example and hub-example are different hosts and both became
	// "hub-example", so two boards shared a credential directory again, one
	// round after the port did. A directory name may contain dots; only the
	// separators that would change the path have to go.
	// The BRACKETS are kept too, for the same reason the dots were: they are
	// what distinguishes the literal [2001:db8::1] from the syntactically valid
	// hostname 2001-db8--1, which the colon rewrite otherwise maps onto it. A
	// contrived pair, and the point is that the comment above claims every
	// address gets its own directory, so it has to be true rather than true of
	// the examples somebody thought of.
	slug := strings.NewReplacer(":", "-", "/", "-", string(filepath.Separator), "-").Replace(h)
	if strings.Contains(h, ":") {
		slug = "[" + slug + "]"
	}
	if port != "" {
		slug += "-" + port
	}
	return slug
}
