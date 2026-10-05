package main

import "fmt"

// reportBridgeOpenPolicy identifies running bridges that have not adopted the policy.
func reportBridgeOpenPolicy(hosts hubHosts, warn fixFn) {
	for _, bridge := range hosts.bridges {
		if bridge.AwayOpen != 2 {
			warn(fmt.Sprintf("Host bridge %s uses an earlier app-opening policy", bridge.Host),
				"restart dibs host-bridge on that host for prompt bounded ChatGPT opening")
		}
	}
}
