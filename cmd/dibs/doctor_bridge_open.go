// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "fmt"

// reportBridgeOpenPolicy identifies running bridges that have not adopted the policy.
func reportBridgeOpenPolicy(hosts hubHosts, warn fixFn) {
	for _, bridge := range hosts.bridges {
		if !bridge.BackgroundOnly {
			warn(fmt.Sprintf("Host bridge %s uses an earlier app-opening policy", bridge.Host),
				"restart dibs host-bridge on that host for background-only ChatGPT delivery; "+
					"ended Claude sessions retain mail until their next ordinary run")
		}
	}
}
