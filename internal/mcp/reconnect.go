package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// BridgePIDMetaKey and BridgeStartMetaKey are additive, local process evidence.
// Neither changes an agent's durable identity or authorizes a session binding.
const (
	BridgePIDMetaKey   = "com.dibs/bridge_pid"
	BridgeStartMetaKey = "com.dibs/bridge_started_at"
	maxObservedBridges = 512
)

type observedBridges struct {
	mu   sync.Mutex
	seen map[string]bool
}

// The observation grants no identity, session binding, or access to mail.
// Only a local transport claiming this daemon's own host may name local PIDs.
// A remotely forwarded or invited request cannot probe this process inventory.
func (s *Server) observeBridge(ctx context.Context, params json.RawMessage) {
	local, _ := ctx.Value(hostIDKey{}).(string)
	if local == "" || resolveHostID(ctx, params) != s.eng.HostID() || metaHostless(params) {
		return
	}
	if _, invited := engine.InvitationFrom(ctx); invited {
		return
	}
	pid, started := bridgeProcessMetadata(params)
	if pid <= 1 || started == "" {
		return
	}
	key := fmt.Sprintf("%d@%s", pid, started)
	s.bridges.mu.Lock()
	defer s.bridges.mu.Unlock()
	if s.bridges.seen[key] {
		return
	}
	if len(s.bridges.seen) >= maxObservedBridges {
		// Re-probing is safe: the engine independently coalesces app cohorts.
		s.bridges.seen = nil
	}
	if s.bridges.seen == nil {
		s.bridges.seen = map[string]bool{}
	}
	app, ok, err := harnessenv.AppForBridge(pid, started)
	if err != nil {
		return // unknown is retryable; do not cache a transient failed probe
	}
	if ok {
		if err = s.eng.AppReconnected(ctx, local, app); err != nil {
			return
		}
	}
	s.bridges.seen[key] = true
}

func bridgeProcessMetadata(params json.RawMessage) (int, string) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	var pid int
	var started string
	if json.Unmarshal(params, &p) != nil || json.Unmarshal(p.Meta[BridgePIDMetaKey], &pid) != nil ||
		json.Unmarshal(p.Meta[BridgeStartMetaKey], &started) != nil || len(started) > 64 {
		return 0, ""
	}
	return pid, started
}
