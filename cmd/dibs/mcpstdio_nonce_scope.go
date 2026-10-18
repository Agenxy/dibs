// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const scopedNonceStore = "harness-session-nonces-v1.json"

func nonceStorePath(name string) string {
	legacy := noncePath()
	if legacy != "" && strings.HasPrefix(name, "session:v1:") {
		return filepath.Join(filepath.Dir(legacy), scopedNonceStore)
	}
	return legacy
}

// Scope is evidence supplied by the harness, never the model's register args.
// In particular host-<ppid> may identify a launcher shared by many threads.
// A caller without a harness id can reuse only this bridge's measured process
// incarnation. Unknown process evidence means no credential injection.
func registrationNonceScope(params map[string]any) string {
	if remoteSession {
		return "" // a tunnel cannot borrow a credential from this Mac's cache
	}
	meta, _ := params["_meta"].(map[string]any)
	if thread, _ := meta["threadId"].(string); strings.TrimSpace(thread) != "" {
		return "codex-thread:" + strings.TrimSpace(thread)
	}
	if session, _ := meta["com.dibs/session"].(string); session != "" &&
		!strings.HasPrefix(session, "host-") && !strings.HasPrefix(session, "bridge-") {
		return "harness-session:" + session
	}
	if started := bridgeStarted(); started != "" {
		return "bridge-process:" + strconv.Itoa(os.Getpid()) + ":" + started
	}
	return ""
}

// Keep the native directory/alias handling, but give every harness session a
// separate namespace. The first session claims an unscoped legacy pool once during upgrade.
// Its owner fence lives in a separate file which old writers cannot overwrite.
func nonceCacheName(name, scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return "session:v1:" + hex.EncodeToString(sum[:]) + ":" + name
}
