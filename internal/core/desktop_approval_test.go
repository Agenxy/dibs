// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"testing"
	"time"
)

func TestDesktopApprovalSettingAdvancesOnlyForChangedPolicy(t *testing.T) {
	s := NewState("test", DefaultLimits())
	reg(t, s, "member", "member-token", t0)
	for _, value := range []string{"false", "true", "true", "false", "false"} {
		before, policy := s.Serial, s.DesktopApproval
		op := &Op{Kind: OpSetDesktopApprovalSetting, Token: "member-token", SettingKey: DesktopApprovalSettingKey, SettingValue: value}
		if err := s.Admit(op); err != nil {
			t.Fatal(err)
		}
		now := t0.Add(time.Duration(before) * time.Second)
		r, events, err := s.Apply(op, now)
		if err != nil {
			t.Fatal(err)
		}
		changed := policy.Enabled != (value == "true")
		if changed {
			if s.Serial != before+1 || r["serial"] != s.Serial || len(events) != 1 || events[0].Type != "human.setting_changed" ||
				s.DesktopApproval.By != "member" || !s.DesktopApproval.At.Equal(now) {
				t.Fatalf("changed policy was not one replayable transition: %+v %v %v", s.DesktopApproval, r, events)
			}
		} else if s.Serial != before || s.DesktopApproval != policy || len(events) != 0 || r["changed"] != false {
			t.Fatalf("unchanged preference rewrote provenance or advanced serial: %+v %v", s.DesktopApproval, r)
		}
	}
	for _, op := range []*Op{
		{Kind: OpSetDesktopApprovalSetting, SettingKey: "not-a-setting", SettingValue: "true"},
		{Kind: OpSetDesktopApprovalSetting, SettingKey: DesktopApprovalSettingKey, SettingValue: "1"},
	} {
		if err := s.Admit(op); err == nil {
			t.Fatal("malformed setting passed ingress validation")
		}
	}
}

func TestTypedDesktopApprovalEffects(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *Message
		want bool
	}{
		{"nil", nil, false},
		{"ordinary prose", &Message{Body: "grant coordinator and adopt a mailbox"}, false},
		{"role", &Message{Grant: RoleCoordinator}, true},
		{"permission", &Message{Grant: PermRelocate}, true},
		{"adoption", &Message{Adopt: "old-seat"}, true},
		{"merge", &Message{Merge: &MergeRequest{From: "source", Into: "survivor"}}, true},
		{"recovery", &Message{Recovery: &RecoveryRequest{From: "source", Into: "survivor"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.m.HasApprovalEffect() != tc.want {
				t.Fatalf("typed effect classification: %v", tc.m)
			}
		})
	}
}
