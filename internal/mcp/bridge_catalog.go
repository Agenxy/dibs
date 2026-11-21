// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/agenxy/dibs/internal/engine"
)

// Bridge metadata keys distinguish the running build from the tool list actually served.
const (
	BridgeBuildMetaKey         = "com.dibs/bridge_build"
	BridgeToolsHashMetaKey     = "com.dibs/served_tools_hash"
	BridgeToolsRevisionMetaKey = "com.dibs/served_tools_revision"
)

// Called only after observeBridge's local-host and invitation gates. Missing
// evidence from an old bridge is unknown, never inferred from its binary.
func (s *Server) observeToolCatalog(ctx context.Context, params json.RawMessage, pid int, started string) {
	if pid <= 1 || started == "" {
		return
	}
	var p struct {
		Token     string `json:"token"`
		Arguments struct {
			Token string `json:"token"`
		} `json:"arguments"`
		Meta struct {
			Build    json.RawMessage `json:"com.dibs/bridge_build"`
			Hash     string          `json:"com.dibs/served_tools_hash"`
			Revision uint64          `json:"com.dibs/served_tools_revision"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	if p.Meta.Hash != "" {
		if raw, err := hex.DecodeString(p.Meta.Hash); err != nil || len(raw) != 8 || p.Meta.Revision == 0 {
			return
		}
	} else if p.Meta.Revision != 0 {
		return
	}
	if p.Token == "" {
		p.Token = p.Arguments.Token
	}
	var build string
	if err := json.Unmarshal(p.Meta.Build, &build); err != nil {
		build = ""
	}
	_ = s.eng.ObserveBridgeToolCatalog(ctx, p.Token, engine.BridgeToolCatalog{
		PID: pid, StartedAt: started, Build: build,
		Hash: p.Meta.Hash, Revision: p.Meta.Revision, CurrentHash: ToolsFingerprint(),
	})
}
