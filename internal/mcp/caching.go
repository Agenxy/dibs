// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"sync"
)

// Cache hints for the 2026-07-28 stateless core.
//
// The spec requires servers to put `ttlMs` and `cacheScope` on every complete
// result from server/discover, tools/list, resources/list and resources/read.
// It matters much more under the stateless core than it looks: with the
// initialize handshake retired there is no session, so a client re-establishes
// nothing and simply issues requests, and Dibs publishes 43 tools whose
// descriptions are deliberately long, because a tool description is the only
// documentation an agent ever reads. Without a freshness hint that payload goes
// back over the wire on every cold path, forever.
//
// The two fields are a hint and a permission, and only one of them is dangerous
// to get wrong:
//
//   - ttlMs is how long a client MAY consider the result fresh. Being wrong
//     costs a stale read or a wasted fetch.
//   - cacheScope is who may KEEP it. "public" tells shared gateways and proxies
//     they may serve this response to a different caller: explicitly including
//     one with a different authorization context. Marking per-agent mail public
//     would therefore be a disclosure bug, not a performance bug.
//
// So the rule here is: public only for bytes that are identical for every
// caller, and private for anything derived from who is asking.
const (
	// ttlStatic is for results fixed for the life of the process: the tool
	// list, the resource descriptors, server/discover, the skills document.
	// These change only when the binary changes, and a client that restarts
	// gets a fresh answer anyway.
	ttlStatic = 3_600_000 // 1 hour

	// ttlLive is for state that genuinely moves. Short rather than zero: a
	// board read is cheap to repeat but pointless to repeat within the same
	// turn, and clients are told (by the spec) not to treat TTL as a polling
	// interval. Subscriptions remain the way to learn about a change promptly,
	// a notification invalidates a cached result immediately.
	ttlLive = 2_000 // 2 seconds

	// scopePublic: identical for every caller, safe for a shared cache.
	scopePublic = "public"
	// scopePrivate: derived from the caller's identity. Never shareable.
	scopePrivate = "private"
)

// cacheable stamps a result with its freshness hint and sharing scope.
//
// Returns the same map so it can wrap a return value directly, which keeps the
// hint adjacent to the thing it describes: a hint added three lines later is a
// hint that gets forgotten when a new branch is added.
func cacheable(result map[string]any, ttlMs int, scope string) map[string]any {
	if result == nil {
		return result
	}
	result["ttlMs"] = ttlMs
	result["cacheScope"] = scope
	return result
}

// ToolsFingerprint identifies the tool list this binary serves, so a stdio
// bridge that has just replaced itself with a newer build can tell its harness
// the list changed (notifications/tools/list_changed).
//
// Why it matters: a long-running session keeps the tool list it started with.
// A Codex worker whose session predated `declare`'s `waiting` argument wrote
// "waiting" into its declaration's text instead, which marks nothing, and was
// continued in a loop for it (2026-10-01). Claude Code refreshes its tools on
// the notification. The installed Codex 0.166 packaged client refreshes only
// with its off-by-default mcp_tool_list_changed feature enabled; see
// docs/TOOL-CATALOG-REFRESH.md for the measurement and its limits.
func ToolsFingerprint() string {
	return localToolsFingerprint()
}

// The local definitions are immutable for the life of an image. A re-exec
// compares and logs this identity; neither needs to marshal the catalog again.
var localToolsFingerprint = sync.OnceValue(func() string { return FingerprintTools(agentTools()) })

// FingerprintTools hashes the complete served catalog, including descriptions
// and schemas. A bridge must hash its daemon's reply, not its own binary.
func FingerprintTools(tools any) string {
	hash := sha256.New()
	// Encoder keeps the module's existing Go API floor. Its only byte change
	// from Marshal is a final newline: withhold that byte, across any writes.
	w := fingerprintJSONWriter{hash: hash}
	if err := json.NewEncoder(&w).Encode(tools); err != nil || !w.pending || w.tail[0] != '\n' {
		hash.Reset() // Marshal returned no bytes on failure; keep that identity.
	}
	return hex.EncodeToString(hash.Sum(nil)[:8])
}

type fingerprintJSONWriter struct {
	hash    hash.Hash
	tail    [1]byte
	pending bool
}

func (w *fingerprintJSONWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// hash.Hash.Write never returns an error. Only the final byte is held;
	// neither this writer nor the bridge retains a serialized catalog buffer.
	if w.pending {
		_, _ = w.hash.Write(w.tail[:])
	}
	_, _ = w.hash.Write(p[:len(p)-1])
	w.tail[0], w.pending = p[len(p)-1], true
	return len(p), nil
}
