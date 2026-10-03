package main

import "fmt"

// reportBridgeOpenPolicy identifies running bridges that have not adopted the policy.
func reportBridgeOpenPolicy(hosts hubHosts, warn fixFn) {
	for _, bridge := range hosts.bridges {
		if bridge.AwayOpen != 1 {
			warn(fmt.Sprintf("Host bridge %s uses legacy idle opening", bridge.Host),
				"restart dibs host-bridge on that host to get away-only open")
		}
	}
}
