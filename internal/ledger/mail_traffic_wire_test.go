// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"reflect"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestMailTrafficJSONFieldNamesAreFrozen(t *testing.T) {
	if !containsTrafficTag(declaredTagsOf(reflect.TypeOf(core.State{})), "traffic") {
		t.Fatal("traffic JSON field renamed")
	}
	for _, tc := range []struct {
		name string
		typ  reflect.Type
		want map[string]bool
		hash string
	}{
		{"history", reflect.TypeOf(core.TrafficHistory{}), map[string]bool{"started_at": true, "first_serial": true, "through": true, "days": true}, "sha256:8c7c13b6bc331f9f"},
		{"bucket", reflect.TypeOf(core.TrafficBucket{}), map[string]bool{"sent": true, "asks_received": true, "overflow_sent": true, "overflow_asks_received": true, "agents": true}, "sha256:c82de2f5baf26d05"},
		{"agent", reflect.TypeOf(core.TrafficAgentCounts{}), map[string]bool{"sent": true, "asks_received": true, "created_serial": true}, "sha256:47dca6fa94653122"},
		{"counts", reflect.TypeOf(core.TrafficCounts{}), map[string]bool{"notify": true, "question": true, "request": true, "handoff": true}, "sha256:b7cf9e769ceb434c"},
	} {
		got := declaredTagsOf(tc.typ)
		if len(got) != len(tc.want) {
			t.Fatalf("%s changed JSON shape: %v", tc.name, got)
		}
		for _, tag := range got {
			if !tc.want[tag] {
				t.Errorf("%s JSON tag renamed: %s", tc.name, tag)
			}
		}
		if fingerprint(tc.want) != tc.hash {
			t.Fatalf("%s frozen JSON field list changed", tc.name)
		}
	}
}

func containsTrafficTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
