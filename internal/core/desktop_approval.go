// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "time"

// DesktopApprovalSettingKey is the person's optional local approval check.
const DesktopApprovalSettingKey = "human.confirm_privileged_desktop"

// DesktopApprovalSetting survives replay and old snapshots default to off.
// Its Go field names are persistent snapshot vocabulary.
type DesktopApprovalSetting struct {
	Enabled bool      `json:"enabled"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at,omitzero"`
}

// HasApprovalEffect is the common classification of typed request effects.
// Participant prose never makes an ordinary request privileged.
func (m *Message) HasApprovalEffect() bool {
	return m != nil && (m.Grant != "" || m.Adopt != "" || m.RetiresIdentity())
}

func admitDesktopApprovalSetting(op *Op) error {
	if op.Kind != OpSetDesktopApprovalSetting {
		return nil
	}
	if op.SettingKey != DesktopApprovalSettingKey || (op.SettingValue != "true" && op.SettingValue != "false") {
		return errf("E_BAD_SETTING", "use human.confirm_privileged_desktop with true or false",
			"invalid desktop approval setting")
	}
	return nil
}

func (s *State) applyDesktopApprovalSetting(l *Agent, op *Op, now time.Time) (Result, []Event) {
	enabled := op.SettingValue == "true"
	result := Result{"setting": DesktopApprovalSettingKey, "value": op.SettingValue, "saved": true,
		"by": s.DesktopApproval.By, "at": s.DesktopApproval.At, "serial": s.Serial}
	if enabled == s.DesktopApproval.Enabled {
		result["changed"] = false
		result["by"] = s.DesktopApproval.By
		return result, nil
	}
	s.DesktopApproval = DesktopApprovalSetting{Enabled: enabled, By: l.ID, At: now}
	result["changed"], result["by"] = true, l.ID
	result["at"], result["serial"] = now, s.Serial+1
	return result, []Event{{Type: "human.setting_changed", Agent: l.ID,
		Data: map[string]any{"setting": DesktopApprovalSettingKey, "enabled": enabled}}}
}
