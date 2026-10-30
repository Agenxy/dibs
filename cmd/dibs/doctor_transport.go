// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/agenxy/dibs/internal/localtransport"
)

func checkClientTransports(client *http.Client, reached bool, ok reportFn, warn, bad fixFn) {
	if !reached {
		return
	}
	var snapshot struct {
		Clients []localtransport.Client `json:"clients"`
		Scope   string                  `json:"scope"`
	}
	req, err := http.NewRequest(http.MethodGet, origin()+"/api/client-transports", nil)
	if err != nil {
		warn("client transport inventory is unknown", err.Error())
		return
	}
	secret, err := localSecret()
	if err != nil {
		warn("client transport inventory is unknown", err.Error())
		return
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := client.Do(req)
	if err != nil {
		warn("client transport inventory is unknown", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&snapshot) != nil || snapshot.Scope == "" {
		warn("this daemon cannot report connected client transports",
			"upgrade dibd before qualifying the IPC cutover; no dormant or unobserved bridge is qualified")
		return
	}
	for _, c := range snapshot.Clients {
		name := fmt.Sprintf("connected client #%d", c.ID)
		if c.BridgePID > 0 {
			name += fmt.Sprintf(" (reported bridge pid %d, start %s)", c.BridgePID, c.BridgeStarted)
		}
		message := name + " uses " + c.Transport
		switch {
		case c.Transport == "local-ipc" || c.Transport == "https":
			ok(message)
		case c.Transport == "plaintext-tcp" && (c.MCP || strings.HasPrefix(c.UserAgent, "dibs/")):
			bad(message, "refresh this bridge through its harness, then recheck; old buffered calls and "+
				"reconnecting streams can keep the old image alive. For a remote board use verified HTTPS. "+
				"Do not roll out to a shared machine until every expected bridge is observed on IPC or verified HTTPS")
		default:
			warn(message, "browser TCP has no local server-identity guarantee; "+
				"unobserved and dormant bridges are not qualified by this inventory")
		}
	}
	warn("transport inventory covers currently connected clients only",
		"compare it with every expected bridge before shared-host rollout; "+
			"an absent dormant bridge is unknown, not evidence of IPC")
}
