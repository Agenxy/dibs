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
	mu       sync.Mutex
	seen     map[string]bool
	inFlight map[string]bool
	// probe is a fixture seam for a bounded process scan; nil uses the real probe.
	probe func(int, string) (harnessenv.AppIncarnation, bool, error)
}

// Reserve only the incarnation key. Whole-system probing and cohort receipt
// I/O must never hold this mutex: other bridges' MCP calls are independent.
func (b *observedBridges) begin(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen[key] || b.inFlight[key] || len(b.inFlight) >= maxObservedBridges {
		return false
	}
	if b.inFlight == nil {
		b.inFlight = map[string]bool{}
	}
	b.inFlight[key] = true
	return true
}

func (b *observedBridges) finish(key string, observed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.inFlight, key)
	if !observed {
		return // transient failures remain retryable on a later request
	}
	if len(b.seen) >= maxObservedBridges {
		// Re-probing is safe: the engine independently coalesces app cohorts.
		b.seen = nil
	}
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	b.seen[key] = true
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
	if !s.bridges.begin(key) {
		return
	}
	observed := false
	defer func() { s.bridges.finish(key, observed) }()
	probe := s.bridges.probe
	if probe == nil {
		probe = harnessenv.AppForBridge
	}
	app, ok, err := probe(pid, started)
	if err != nil {
		return // unknown is retryable; do not cache a transient failed probe
	}
	if ok {
		if err = s.eng.AppReconnected(ctx, local, app); err != nil {
			return
		}
	}
	observed = true
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
