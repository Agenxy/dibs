// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestAliasReleaseOwnershipAndMailboxSelectorThroughMCP(t *testing.T) {
	srv, eng, _ := aliasReplayServer(t, t.TempDir())
	id, tok := aliasRegister(t, srv, "worker-id", "ownership-worker")
	peer, peerTok := aliasRegister(t, srv, "peer-id", "ownership-peer")
	aliasRename(t, srv, tok, "former-label")
	aliasRename(t, srv, tok, "current-label")
	n := aliasSend(t, srv, peerTok, "former-label", id, "owned mailbox")
	aliasRead(t, srv, tok, n, id, "owned mailbox")
	r := toolCall(t, srv, "update", map[string]any{"token": peerTok, "release_names": []string{"former-label"}})
	if r["code"] != "E_NOT_PERMITTED" {
		t.Fatalf("another owner could release the name: %v", r)
	}
	for _, ref := range []string{id, "current-label"} {
		r = toolCall(t, srv, "update", map[string]any{"token": tok, "release_names": []string{ref}})
		if r["code"] != "E_BAD_ARG" {
			t.Fatalf("current name/id release succeeded: %v", r)
		}
	}
	// No selector is resolved before authorization, and no role is widened.
	r = toolCall(t, srv, "all_mail", map[string]any{"token": peerTok, "agent": "former-label", "census": true})
	if r["__is_error"] != true {
		t.Fatalf("ordinary participant gained census access: %v", r)
	}
	if _, err := eng.GrantRole(context.Background(), peer, "admin"); err != nil {
		t.Fatal("setup:", err)
	}
	r = aliasCall(t, srv, "all_mail", map[string]any{"token": peerTok, "agent": "former-label"})
	mail, _ := r["messages"].([]any)
	if len(mail) != 1 || mail[0].(map[string]any)["to"] != id {
		t.Fatalf("former-name selector lost its mailbox: %v", r)
	}
	// Rename back to an existing former name: no duplicate is retained, and
	// the name just left becomes the only new alias.
	r = aliasCall(t, srv, "update", map[string]any{"token": tok, "name": "former-label"})
	if strings.Contains(strings.Join(aliasStrings(r["name_aliases"]), ","), "former-label") {
		t.Fatalf("current name also stayed an alias: %v", r)
	}
}

func aliasStrings(value any) []string {
	var out []string
	for _, name := range value.([]any) {
		out = append(out, name.(string))
	}
	return out
}
