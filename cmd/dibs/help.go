// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"strings"
)

func helpOnly(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
}

// Commands without a flag parser must answer help before credentials, stdin,
// certificates or a live board are inspected. Flag-bearing commands retain
// their own parsers and detailed defaults, rather than generic replacements.
func staticCommandHelp(verb string) bool {
	switch verb {
	case "messages", "fingerprint", "hook-poll", "hook-session", "hook-spawn", "version":
		fmt.Println("usage: dibs " + verb)
		printUsageEntry(verb)
	case "admin":
		fmt.Println("usage: dibs admin <command>")
		fmt.Println(adminUsage)
	case "help":
		fmt.Println("usage: dibs help")
		fmt.Println(styledUsage())
	default:
		return false
	}
	return true
}

// Reuse the canonical usage entry, including its continuation lines. These
// commands take no flags; keeping a second synopsis would invite drift.
func printUsageEntry(verb string) {
	printing := false
	for _, line := range strings.Split(usage, "\n") {
		if printing && !strings.HasPrefix(line, "     ") {
			break
		}
		if strings.HasPrefix(line, "  dibs "+verb+" ") {
			printing = true
		}
		if printing {
			fmt.Println(line)
		}
	}
}
