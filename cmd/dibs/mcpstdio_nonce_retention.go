// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/procinfo"
)

const (
	scopedNamePrefix       = "session:v1:"
	scopedNonceOwnerPrefix = "owner:v1:"
	nonceScopePrefix       = "scope:v1:"
	nonceScopeFirstSweep   = "scope:v1:first-sweep"
	nonceScopeLastSweep    = "scope:v1:last-sweep"
	nonceScopeSweepEvery   = 6 * time.Hour
	nonceScopeRetention    = 90 * 24 * time.Hour
)

// Older writers decode a string map and copy unfamiliar keys on each write.
// Keep the metadata in that format, with keys outside the directory namespaces.
type nonceScopeUse struct {
	Kind     string `json:"kind"`
	LastUsed int64  `json:"last_used"`
	PID      int    `json:"pid,omitempty"`
	Start    string `json:"start,omitempty"`
}

func noteNonceScopeUse(all map[string]string, name, scope string, now time.Time) bool {
	key := nonceScopePrefix + name
	var previous nonceScopeUse
	if json.Unmarshal([]byte(all[key]), &previous) == nil {
		age := now.Sub(time.Unix(previous.LastUsed, 0))
		if age >= 0 && age < nonceScopeSweepEvery {
			return false
		}
	}
	kind, binding, _ := strings.Cut(scope, ":")
	use := nonceScopeUse{Kind: kind, LastUsed: now.Unix()}
	if kind == "bridge-process" {
		pid, started, _ := strings.Cut(binding, ":")
		use.PID, _ = strconv.Atoi(pid)
		use.Start = started
	}
	raw, _ := json.Marshal(use)
	all[key] = string(raw)
	return true
}

func maintainNonceScopes(all map[string]string, current string, now time.Time) bool {
	last, _ := strconv.ParseInt(all[nonceScopeLastSweep], 10, 64)
	age := now.Sub(time.Unix(last, 0))
	if last != 0 && age >= 0 && age < nonceScopeSweepEvery {
		return false
	}
	first, _ := strconv.ParseInt(all[nonceScopeFirstSweep], 10, 64)
	if first == 0 || first > now.Unix() {
		first = now.Unix()
		all[nonceScopeFirstSweep] = strconv.FormatInt(first, 10)
	}
	all[nonceScopeLastSweep] = strconv.FormatInt(now.Unix(), 10)
	cutoff := now.Add(-nonceScopeRetention).Unix()
	uses, protected := scopedNonceRetentionInputs(all, current)
	pruneNonceEntries(all, uses, protected, expiredNonceScopes(uses, cutoff), first <= cutoff)
	return true
}

func scopedNonceRetentionInputs(all map[string]string, current string) (map[string]nonceScopeUse, map[string]bool) {
	uses := map[string]nonceScopeUse{}
	protected := map[string]bool{current: true}
	for key, value := range all {
		if strings.HasPrefix(key, scopedNonceOwnerPrefix) {
			protected[value] = true
		}
		if name, ok := strings.CutPrefix(key, nonceScopePrefix); ok && strings.HasPrefix(name, scopedNamePrefix) {
			var use nonceScopeUse
			if json.Unmarshal([]byte(value), &use) == nil && use.LastUsed > 0 {
				uses[name] = use
			}
		}
	}
	return uses, protected
}

func expiredNonceScopes(uses map[string]nonceScopeUse, cutoff int64) map[string]bool {
	expired := map[string]bool{}
	var live map[int]string
	for name, use := range uses {
		if use.Kind != "bridge-process" || use.PID <= 1 || use.Start == "" {
			expired[name] = use.LastUsed <= cutoff
			continue
		}
		if live == nil {
			live = nonceScopeProcesses()
		}
		expired[name] = nonceProcessExpired(live, use, cutoff)
	}
	return expired
}

func nonceProcessExpired(live map[int]string, use nonceScopeUse, cutoff int64) bool {
	if len(live) == 0 {
		return use.LastUsed <= cutoff
	}
	return live[use.PID] != use.Start
}

func nonceScopeProcesses() map[int]string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rows, err := procinfo.Snapshot(ctx)
	live := make(map[int]string, len(rows))
	if err == nil {
		for _, row := range rows {
			if stamp := procinfo.Stamp(row.Start); stamp != "" {
				live[row.PID] = stamp
			}
		}
	}
	return live
}

func pruneNonceEntries(
	all map[string]string, uses map[string]nonceScopeUse, protected, expired map[string]bool, opaqueExpired bool,
) {
	for key, value := range all {
		if name, ok := strings.CutPrefix(key, nonceScopePrefix); ok {
			if expired[name] && !protected[name] {
				delete(all, key)
			}
			continue
		}
		if strings.HasPrefix(key, scopedNonceOwnerPrefix) {
			continue
		}
		dir, suffix, _ := strings.Cut(key, "\x00")
		name, _, extra := strings.Cut(suffix, "\x00")
		if !strings.HasPrefix(name, scopedNamePrefix) {
			continue
		}
		_, tracked := uses[name]
		redundant := !extra && !strings.HasPrefix(dir, "dir:v1:") && all["dir:v1:"+key] == value
		if redundant || !protected[name] && (expired[name] || !tracked && opaqueExpired) {
			delete(all, key)
		}
	}
}
