// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// All new cache writers share this kernel lock. The older create/unlink lock
// remains inside it for old bridges, but its timed unlocked fallback cannot
// allow two NEW claimers to transfer one legacy credential.
func lockScopedNonceStore(path string) (func(), error) {
	f, err := os.OpenFile(path+".scope.lock", os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- fixed private cache path
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err = paths.LockExclusive(f, false); err == nil {
			return func() { paths.Unlock(f); _ = f.Close() }, nil
		}
		if !paths.LockHeldElsewhere(err) || time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("credential cache is busy or unavailable: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readNonceStore(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed private cache path
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var all map[string]string
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("credential cache is unreadable: %w", err)
	}
	if all == nil {
		all = map[string]string{}
	}
	return all, nil
}

// The new file is the authoritative owner fence. An old bridge can replace
// the whole legacy JSON, but cannot erase session credentials or reclaim a seat
// from this file. The claim linearizes when this file is durably renamed;
// legacy deletion follows it and is retried by the owning session after a crash.
func scopedNoncePlan(project, role, scope, chosen string) ([]string, error) {
	legacyPath := noncePath()
	if project == "" || legacyPath == "" {
		return nil, nil
	}
	guard, err := lockScopedNonceStore(legacyPath)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, nil
		}
		return nil, err
	}
	defer guard()
	return transactScopedNonce(legacyPath, project, role, scope, chosen)
}

func transactScopedNonce(legacyPath, project, role, scope, chosen string) ([]string, error) {
	name := nonceCacheName(role, scope)
	path := nonceStorePath(name)
	all, err := readNonceStore(path)
	if err != nil {
		return nil, err
	}
	owner := scopedNonceOwnerPrefix + canonicalNonceKey(project, role)
	isolatedConversation := strings.HasPrefix(scope, antigravityNoncePrefix)
	var legacy map[string]string
	var changed bool
	if !isolatedConversation {
		var unlock func()
		legacy, unlock, err = prepareLegacyNonceClaim(all, legacyPath, project, role, name, chosen)
		if err != nil {
			return nil, err
		}
		defer unlock()
		changed = claimLegacyNonces(all, legacy, project, role, name, owner, chosen)
	}
	if chosen != "" {
		changed = recordNonceIn(all, project, name, chosen, true) || changed
	}
	candidates := nonceCandidatesIn(all, project, name)
	if !isolatedConversation && chosen == "" && carryProcessNonces(all, project, role, name, scope) {
		// Merged even into a slot that already holds one: a register that ran
		// before this carry-over existed minted a nonce there, and the slot is
		// no longer empty. More than one candidate goes to the daemon as a
		// recovery group, which proves the right one instead of guessing.
		changed = true
		candidates = nonceCandidatesIn(all, project, name)
	}
	if len(candidates) == 0 {
		nonce := mintNonce()
		if nonce == "" {
			return nil, errors.New("could not create a recovery credential")
		}
		changed = recordNonceIn(all, project, name, nonce, false) || changed
		candidates = []string{nonce}
	}
	if changed {
		if err := writeNonces(path, all); err != nil {
			return nil, err
		}
	}
	if !isolatedConversation && all[owner] == name {
		if err := removeClaimedLegacyCopies(all, legacy, project, role, name, legacyPath); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

func prepareLegacyNonceClaim(
	all map[string]string, path, project, role, name, chosen string,
) (map[string]string, func(), error) {
	owner := scopedNonceOwnerPrefix + canonicalNonceKey(project, role)
	needs := all[owner] == name || (all[owner] == "" && (chosen != "" || len(nonceCandidatesIn(all, project, name)) == 0))
	if !needs {
		return nil, func() {}, nil
	}
	unlock := lockNonces(path)
	legacy, err := readNonceStore(path)
	if err != nil && chosen == "" {
		unlock()
		return nil, nil, err
	}
	return legacy, unlock, nil
}

func matchingNonceKeys(all map[string]string, project, name string) []string {
	canonical := paths.Canonical(project)
	var keys []string
	for key := range all {
		if strings.HasPrefix(key, scopedNonceOwnerPrefix) {
			continue
		}
		dir, suffix, found := strings.Cut(key, "\x00")
		base, _, _ := strings.Cut(suffix, "\x00")
		if found && base == name && paths.Canonical(strings.TrimPrefix(dir, "dir:v1:")) == canonical {
			keys = append(keys, key)
		}
	}
	return keys
}

func claimLegacyNonces(all, legacy map[string]string, project, role, name, owner, chosen string) bool {
	if all[owner] != "" {
		return false
	}
	keys := matchingNonceKeys(legacy, project, role)
	if len(keys) == 0 {
		return false
	}
	if chosen != "" {
		matches := false
		for _, key := range keys {
			matches = matches || legacy[key] == chosen
		}
		if !matches {
			return false
		}
	} else if len(nonceCandidatesIn(all, project, name)) != 0 {
		return false
	}
	for _, key := range keys {
		dir, suffix, _ := strings.Cut(key, "\x00")
		all[dir+"\x00"+name+strings.TrimPrefix(suffix, role)] = legacy[key]
	}
	all[owner] = name
	return true
}

func removeClaimedLegacyCopies(all, legacy map[string]string, project, role, name, path string) error {
	owned := map[string]bool{}
	for _, key := range matchingNonceKeys(all, project, name) {
		owned[all[key]] = true
	}
	changed := false
	for _, key := range matchingNonceKeys(legacy, project, role) {
		if owned[legacy[key]] {
			delete(legacy, key)
			changed = true
		}
	}
	if changed {
		return writeNonces(path, legacy)
	}
	return nil
}

const nonceCacheErrorSource = "dibs.bridge.nonce-cache"

func nonceCacheRefusal(line []byte, err error) []byte {
	message := fmt.Sprintf("credential cache migration failed (%v). Registration was not forwarded.", err)
	reply, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": idOf(line), "error": map[string]any{
			"code": -32000, "message": message,
			"data": map[string]any{
				"source": nonceCacheErrorSource, "code": "E_NONCE_CACHE", "cause": err.Error(),
				"hint": "keep this session and its stored credentials; check the private cache's lock, permissions " +
					"and JSON, then retry the same register. Do not retry with a fresh nonce",
			},
		},
	})
	return reply
}

func isNonceCacheRefusal(line []byte) bool {
	var reply struct {
		Error struct {
			Data struct {
				Source string `json:"source"`
			} `json:"data"`
		} `json:"error"`
	}
	return json.Unmarshal(line, &reply) == nil && reply.Error.Data.Source == nonceCacheErrorSource
}

func syncNonceDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path)) // #nosec G304 -- fixed private cache directory
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// carryProcessNonces copies this bridge's own process-scoped credentials into
// its session scope.
//
// A bridge that could not see its harness session used to file its nonce under
// its own process incarnation. Once the same bridge learns the session (a late
// sidecar, or #630's stale CLAUDE_PID fix), its scope changes and the new slot
// is empty: register then minted a fresh nonce, so an active agent got
// E_SESSION_TAKEN and a dormant one could fork a sibling instead of
// reattaching. Measured 2026-11-20 on this board's own architect seat.
//
// Only THIS process's slot is read. Its pid and start time are this bridge,
// so the credential is one this bridge was handed, which is the strongest
// evidence the cache has; any other process's slot stays where it is.
func carryProcessNonces(all map[string]string, project, role, name, scope string) bool {
	started := bridgeStarted()
	if started == "" {
		return false
	}
	own := "bridge-process:" + strconv.Itoa(os.Getpid()) + ":" + started
	if own == scope {
		return false
	}
	changed := false
	for _, nonce := range nonceCandidatesIn(all, project, nonceCacheName(role, own)) {
		changed = recordNonceIn(all, project, name, nonce, false) || changed
	}
	return changed
}
