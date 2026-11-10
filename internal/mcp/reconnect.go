// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	mu      sync.Mutex
	faultMu sync.Mutex // serialize ReportFault's delivery-time dedup, never held by the writer
	seen    map[string]bool
	entries map[string]*bridgeObservation
	next    uint64
	// probe is a fixture seam for a bounded process scan; nil uses the real probe.
	probe      func(int, string) (harnessenv.AppIncarnation, bool, error)
	batchProbe func(context.Context, []harnessenv.BridgeIncarnation) ([]harnessenv.BridgeObservation, error)
}

type bridgeObservation struct {
	process          harnessenv.BridgeIncarnation
	order            uint64
	inFlight, failed bool
}

// Reserve the caller and all retryable entries as one inventory batch. An
// unrelated concurrent first discovery remains independent; no process scan,
// writer query or disk I/O holds this mutex. All remembered work is bounded.
func (b *observedBridges) begin(pid int, started string) (map[string]harnessenv.BridgeIncarnation, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = map[string]*bridgeObservation{}
	}
	capacityAction := ""
	key := fmt.Sprintf("%d@%s", pid, started)
	if pid > 1 && started != "" && !b.seen[key] && b.entries[key] == nil {
		capacityAction = b.makeRoomLocked()
		if len(b.entries) < maxObservedBridges {
			b.next++
			b.entries[key] = &bridgeObservation{process: harnessenv.BridgeIncarnation{PID: pid, Start: started}, order: b.next}
		}
	}
	batch := map[string]harnessenv.BridgeIncarnation{}
	for k, entry := range b.entries {
		if !entry.inFlight {
			entry.inFlight = true
			batch[k] = entry.process
		}
	}
	return batch, capacityAction
}

// Called under mu. Return the capacity decision for the fault report. A
// reservation doing I/O keeps its place until its observation completes.
func (b *observedBridges) makeRoomLocked() string {
	if len(b.entries) < maxObservedBridges {
		return ""
	}
	var oldest string
	for key, entry := range b.entries {
		if !entry.inFlight && (oldest == "" || entry.order < b.entries[oldest].order) {
			oldest = key
		}
	}
	if oldest == "" {
		return "skipped the arriving observation because all entries were in flight"
	}
	delete(b.entries, oldest)
	return "evicted the oldest idle observation"
}

func (b *observedBridges) finish(key string, observed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[key]
	if !observed {
		entry.inFlight, entry.failed = false, true
		return
	}
	// A completed negative probe also retires the reservation. The bridge can
	// exit between attempts, and that observation requires no fault mail.
	delete(b.entries, key)
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
	s.observeToolCatalog(ctx, params, pid, started)
	batch, capacityAction := s.bridges.begin(pid, started)
	if len(batch) == 0 && capacityAction == "" {
		return
	}
	// Admission used this request's transport authority. Completion belongs to
	// the daemon, so send's 500ms observation wait and HTTP disconnects cannot
	// discard it. No initialize/session state is involved on either era.
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	go func() {
		select {
		case <-s.eng.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	s.observeBridgeBatch(ctx, local, batch)
	s.bridges.mu.Lock()
	pending := 0
	for _, entry := range s.bridges.entries {
		if entry.failed {
			pending++
		}
	}
	s.eng.SetReconnectPending(pending)
	s.bridges.mu.Unlock()
	if capacityAction != "" && ctx.Err() == nil {
		s.bridges.faultMu.Lock()
		s.eng.ReportFault(ctx, engine.Fault{
			Kind: "app-reconnect-given-up", Bug: true, Where: "internal/mcp/reconnect.go",
			What: fmt.Sprintf("Dibs reached its %d-entry app reconnect observation limit and %s.",
				maxObservedBridges, capacityAction),
			Remedy: "Read the outstanding mail on the board. A later local bridge request supplies fresh process evidence; " +
				"run `dibs doctor` to inspect observations still pending.",
		})
		s.bridges.faultMu.Unlock()
	}
}

func (s *Server) observeBridgeBatch(
	ctx context.Context, local string, batch map[string]harnessenv.BridgeIncarnation,
) {
	if len(batch) == 0 {
		return
	}
	keys := make([]string, 0, len(batch))
	for key := range batch {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	processes := make([]harnessenv.BridgeIncarnation, 0, len(keys))
	for _, key := range keys {
		processes = append(processes, batch[key])
	}
	results, err := s.probeBridges(ctx, processes)
	if err != nil && ctx.Err() == nil {
		// One failure-derived retry, inside this same daemon-owned observation.
		// Keep every reservation in flight so a concurrent request cannot take
		// the batch over. A second failure remains visible until another local
		// call; each attempt still uses one bounded shared process inventory.
		s.bridges.mu.Lock()
		for _, key := range keys {
			s.bridges.entries[key].failed = true
		}
		s.bridges.mu.Unlock()
		results, err = s.probeBridges(ctx, processes)
	}
	for i, key := range keys {
		observed := err == nil
		if observed && results[i].App.PID > 1 {
			observed = s.eng.AppReconnected(ctx, local, results[i].App) == nil
		}
		s.bridges.finish(key, observed)
	}
}

func (s *Server) probeBridges(
	ctx context.Context, processes []harnessenv.BridgeIncarnation,
) ([]harnessenv.BridgeObservation, error) {
	if s.bridges.batchProbe != nil {
		return s.bridges.batchProbe(ctx, processes)
	}
	if s.bridges.probe != nil { // single-process seam retained for existing discovery concurrency fixtures
		results := make([]harnessenv.BridgeObservation, len(processes))
		for i, p := range processes {
			app, _, err := s.bridges.probe(p.PID, p.Start)
			if err != nil {
				return nil, err
			}
			results[i] = harnessenv.BridgeObservation{Present: true, App: app}
		}
		return results, nil
	}
	return harnessenv.AppForBridges(ctx, processes)
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
