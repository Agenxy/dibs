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
