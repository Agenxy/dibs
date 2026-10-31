// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"sync"

	"github.com/agenxy/dibs/internal/mcp"
)

// This is evidence of a complete list written to stdout, never of a client
// adopting it. In particular an upgrade notification is not a list response.
var servedCatalog struct {
	sync.Mutex
	hash     string
	revision uint64
}

func catalogServed() (string, uint64) {
	servedCatalog.Lock()
	defer servedCatalog.Unlock()
	return servedCatalog.hash, servedCatalog.revision
}

func restoreCatalog(hash string, revision uint64) {
	servedCatalog.Lock()
	defer servedCatalog.Unlock()
	servedCatalog.hash, servedCatalog.revision = hash, revision
}

func catalogOnReply(changed chan<- struct{}, next func([]byte, []byte)) func([]byte, []byte) {
	return func(request, reply []byte) {
		next(request, reply)
		if methodOf(request) != "tools/list" {
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		var res struct {
			ID     json.RawMessage `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result struct {
				Tools      []any  `json:"tools"`
				NextCursor string `json:"nextCursor"`
			} `json:"result"`
		}
		if json.Unmarshal(request, &req) != nil || json.Unmarshal(reply, &res) != nil ||
			string(req.ID) != string(res.ID) || len(req.ID) == 0 || len(res.Error) > 0 ||
			res.Result.Tools == nil || res.Result.NextCursor != "" {
			return
		}
		hash := mcp.FingerprintTools(res.Result.Tools)
		servedCatalog.Lock()
		if servedCatalog.hash != hash {
			servedCatalog.hash = hash
			servedCatalog.revision++
		}
		servedCatalog.Unlock()
		// The existing event-driven metadata worker reports even if the
		// harness makes no later call. No timer, agent token or model contact.
		select {
		case changed <- struct{}{}:
		default:
		}
	}
}
