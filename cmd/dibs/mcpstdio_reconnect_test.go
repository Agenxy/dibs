// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/agenxy/dibs/internal/mcp"
)

func TestBridgeProcessMetadataLeavesResponsesByteIdentical(t *testing.T) {
	wasRemote := remoteSession
	remoteSession = false
	bridgeStart.Lock()
	previousStart := bridgeStart.at
	bridgeStart.at = "Sun Oct 4 06:00:00 2026"
	bridgeStart.Unlock()
	t.Cleanup(func() {
		remoteSession = wasRemote
		bridgeStart.Lock()
		bridgeStart.at = previousStart
		bridgeStart.Unlock()
	})
	for _, line := range []string{
		` { "jsonrpc" : "2.0", "id" : "sampling-1", "result" : {"content":{"type":"text","text":"sample"}} } `,
		`{"jsonrpc":"2.0","id":12,"result":{"action":"accept","content":{"name":"example"}}}`,
		`{"jsonrpc":"2.0","id":"roots-3","result":{"roots":[]}}`,
		`{"jsonrpc":"2.0","id":12,"error":{"code":-32603,"message":"cancelled"}}`,
	} {
		if out := enrichBridgeProcess([]byte(line)); !bytes.Equal(out, []byte(line)) {
			t.Errorf("a server-request response was changed: %s -> %s", line, out)
		}
	}
	// Assert setup as well: this must exercise a bridge that actually enriches
	// requests, rather than accidentally pass because process probing failed.
	for _, method := range []string{"server/discover", "notifications/initialized"} {
		out := enrichBridgeProcess([]byte(`{"jsonrpc":"2.0","method":"` + method + `"}`))
		var msg struct {
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.Unmarshal(out, &msg); err != nil || msg.Params.Meta[mcp.BridgeStartMetaKey] != "Sun Oct 4 06:00:00 2026" {
			t.Fatalf("setup: request/notification %s did not carry bridge metadata: %s (%v)", method, out, err)
		}
	}
}
