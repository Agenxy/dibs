// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "fmt"

func printDirectHTTPConfig(scheme string, out []byte) {
	if scheme == "https" {
		fmt.Println(string(out))
		return
	}
	fmt.Println("# Direct local HTTP configurations are refused: use the stdio bridge above.")
	fmt.Println("# A process-incapable client requires verified HTTPS, including through SSH.")
}

func directHTTPExample(scheme, url, secret, preview string) (string, string, string) {
	if scheme == "https" {
		return url, secret, preview
	}
	// #nosec G101 -- documentation placeholders, never working credentials.
	return "https://<board-host>:<tls-port>/mcp", "<verified-board-secret>", "<verified-board-secret>"
}

func authenticatedJoiner(scheme, joiner string) string {
	if scheme == "https" {
		return joiner
	}
	return "https://<board-host>:<tls-port>"
}

func printRemoteTLSMigration() {
	fmt.Println(`
# ── Agents on ANOTHER machine ───────────────────────────────────────────────
# Local agents use this profile's private IPC socket. Remote coordination
# requires verified HTTPS. A plaintext TCP listener, including a local SSH
# forward, can be replaced by another OS user while the board is stopped.
#
# Configure HTTPS on the hub (tls_cert/tls_key or a reachable TLS address).
# For a private loopback hub, use an HTTPS SSH forward to that TLS port:
#   ssh -N -L <local-port>:127.0.0.1:<hub-tls-port> <user>@<hub>
# Then on the other machine:
#   dibs mcp-config --board https://127.0.0.1:<local-port>
# The recipe carries DIBS_TRANSPORT=https and a separate private profile, and
# records the hub's trusted CA. Plaintext coordination fallback is refused.
# The separately invited public bearer forward remains available with its
# documented port-squatting exposure; it does not carry local coordination credentials.`)
}
