// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func init() {
	checkMailIndex = func(s *State, cached *mailIndex) {
		if diff := mailIndexDiff(cached, s.buildMailIndex()); diff != "" {
			panic("mail index trusted while stale: " + diff)
		}
	}
}

// sameState is replay equality: the recorded state, without the derived mail
// index, which a read after the last op builds on one side and not the other.
func sameState(a, b *State) bool {
	x, y := *a, *b
	x.mailIdx, y.mailIdx = nil, nil
	return reflect.DeepEqual(&x, &y)
}

func mailIndexDiff(got, want *mailIndex) string {
	for _, side := range []struct {
		name      string
		got, want map[string][]*Message
	}{{"to", got.to, want.to}, {"from", got.from, want.from}} {
		keys := map[string]bool{}
		for k := range side.got {
			keys[k] = true
		}
		for k := range side.want {
			keys[k] = true
		}
		for k := range keys {
			if !slices.Equal(serials(side.got[k]), serials(side.want[k])) {
				return fmt.Sprintf("%s[%q]: cached %v, rebuilt %v", side.name, k, serials(side.got[k]), serials(side.want[k]))
			}
		}
	}
	return ""
}

func serials(ms []*Message) []uint64 {
	out := make([]uint64, len(ms))
	for i, m := range ms {
		out[i] = m.Serial
	}
	return out
}

// The invalidation rule itself, at the one place it can go wrong without the
// serial noticing: a change and a read inside the same op. Mail moved by a
// merge has to leave the source's index and arrive in the survivor's before
// the op that moved it finishes.
func TestMailIndexSeesAReaddressInsideOneOp(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	now := time.Unix(1_700_000_000, 0)
	reg(t, s, "a", "ta", now)
	reg(t, s, "b", "tb", now)
	reg(t, s, "c", "tc", now)
	mustApply(t, s, &Op{Kind: OpSendMessage, Token: "ta", To: "b", MsgType: MsgNotify, Body: "one"}, now)
	if got := len(s.MailTo("b")); got != 1 {
		t.Fatalf("setup: b holds %d messages, want 1", got)
	}
	serial := s.Serial // unchanged across the move: this is the case the serial cannot see
	s.moveMail("b", "c")
	if s.Serial != serial {
		t.Fatalf("setup: moveMail advanced the serial, so this does not test the same-op case")
	}
	if got := len(s.MailTo("b")); got != 0 {
		t.Errorf("b still indexed with %d messages after its mail moved", got)
	}
	if got := len(s.MailTo("c")); got != 1 {
		t.Errorf("c indexed with %d messages after mail moved to it, want 1", got)
	}
}
