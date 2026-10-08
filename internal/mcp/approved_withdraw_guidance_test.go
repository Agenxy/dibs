// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"strings"
	"testing"
)

func TestApprovedRequestWithdrawalIsVisibleInBothToolLists(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := newServerWithEngine(t)
			list := result(t, rpc(t, srv, version, "tools/list", map[string]any{}), "tools/list")
			for _, item := range list["tools"].([]any) {
				tool := item.(map[string]any)
				if tool["name"] != "respond" {
					continue
				}
				description := tool["description"].(string)
				for _, rule := range []string{"including after approval", "before done", "clears owed work", "notifies the recipient once"} {
					if !strings.Contains(description, rule) {
						t.Errorf("withdrawal guidance omitted %q: %s", rule, description)
					}
				}
				return
			}
			t.Fatal("setup: respond missing from tools/list")
		})
	}
}
