// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"encoding/json"
	"strings"
	"sync"
)

// Settings is a versioned observation from the notifier on the posting machine.
// It is derived evidence, never a visibility receipt or replayable state. Nil
// means an old, unavailable or malformed helper supplied no settings evidence.
type Settings struct {
	Version                   int         `json:"version"`
	AuthorizationStatus       string      `json:"authorization_status"`
	AlertStyle                string      `json:"alert_style"`
	AlertSetting              string      `json:"alert_setting"`
	NotificationCenterSetting string      `json:"notification_center_setting"`
	LockScreenSetting         string      `json:"lock_screen_setting"`
	TimeSensitiveSetting      string      `json:"time_sensitive_setting"`
	Focus                     FocusStatus `json:"focus"`
}

// FocusStatus reports only an observation the native helper was allowed to read.
type FocusStatus struct {
	Authorization string `json:"authorization"`
	Observable    bool   `json:"observable"`
	IsFocused     *bool  `json:"is_focused,omitempty"`
}

// DecodeSettings accepts this version only. Unknown values remain unknown and
// cannot acquire a meaning by being copied from a relay's arbitrary strings.
func DecodeSettings(raw []byte) *Settings {
	var s Settings
	if json.Unmarshal(raw, &s) != nil || s.Version != 1 {
		return nil
	}
	return s.Normalized()
}

func enum(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}

// Normalized returns an independent bounded copy, including the optional bool.
func (s *Settings) Normalized() *Settings {
	if s == nil || s.Version != 1 {
		return nil
	}
	copy := *s
	copy.AuthorizationStatus = enum(s.AuthorizationStatus, "authorized", "provisional", "denied", "not-determined")
	copy.AlertStyle = enum(s.AlertStyle, "none", "banner", "alert")
	copy.AlertSetting = enum(s.AlertSetting, "enabled", "disabled", "not-supported")
	copy.NotificationCenterSetting = enum(s.NotificationCenterSetting, "enabled", "disabled", "not-supported")
	copy.LockScreenSetting = enum(s.LockScreenSetting, "enabled", "disabled", "not-supported")
	copy.TimeSensitiveSetting = enum(s.TimeSensitiveSetting, "enabled", "disabled", "not-supported")
	copy.Focus.Authorization = enum(s.Focus.Authorization, "authorized", "denied", "restricted", "not-determined")
	copy.Focus.Observable = s.Focus.Observable && copy.Focus.Authorization == "authorized" && s.Focus.IsFocused != nil
	copy.Focus.IsFocused = nil
	if copy.Focus.Observable {
		value := *s.Focus.IsFocused
		copy.Focus.IsFocused = &value
	}
	return &copy
}

// Hints names only settings the person can act on. Standing capability limits
// belong in doctor's Information, rather than being repeated on every receipt.
func (s *Settings) Hints(action bool) string {
	s = s.Normalized()
	if s == nil {
		return ""
	}
	var hints []string
	if s.AuthorizationStatus == "denied" || s.AuthorizationStatus == "not-determined" {
		hints = append(hints, "Allow Notifications for Dibs in System Settings > Notifications > Dibs.")
	}
	if s.AlertStyle == "none" || s.AlertSetting == "disabled" {
		hints = append(hints, "Dibs has no visible alert style. Choose System Settings > Notifications > Dibs > Alerts.")
	} else if action && s.AlertStyle == "banner" {
		advice, _ := s.ApprovalAdvice()
		hints = append(hints, advice)
	}
	if s.NotificationCenterSetting == "disabled" {
		hints = append(hints, "Enable Notification Center in System Settings > Notifications > Dibs "+
			"to retain notifications there.")
	}
	if s.LockScreenSetting == "disabled" {
		hints = append(hints, "Lock-screen notifications are disabled for Dibs; "+
			"enable them in System Settings > Notifications > Dibs if wanted.")
	}
	if action && s.TimeSensitiveSetting == "disabled" {
		hints = append(hints, "Time Sensitive notifications are disabled. "+
			"Enable Time Sensitive in System Settings > Notifications > Dibs "+
			"if requests should be allowed through Focus.")
	}
	if s.Focus.Observable && *s.Focus.IsFocused {
		hints = append(hints, "Focus is on and may hold notifications. "+
			"Check Dibs in System Settings > Focus; posting does not confirm visibility.")
	}
	return strings.Join(hints, " ")
}

// ApprovalAdvice is a setting diagnosis, never a receipt that a person saw an
// approval request. Unknown evidence must not erase a previously shown notice.
func (s *Settings) ApprovalAdvice() (text string, observed bool) {
	s = s.Normalized()
	if s == nil || s.AlertStyle == "unknown" {
		return "", false
	}
	if s.AlertStyle == "banner" {
		return "Dibs approval requests appear as banners and vanish. " +
			"Set System Settings > Notifications > Dibs > Alert style: Alerts.", true
	}
	return "", s.AlertStyle == "alert"
}

// CurrentApprovalAdvice uses the existing permission-free status probe. Call
// outside the engine writer: the native helper may take time to answer.
func CurrentApprovalAdvice() string {
	if goos != "darwin" {
		return ""
	}
	ReachWithSettings()
	return RecordedApprovalAdvice()
}

// RecordedApprovalAdvice reads the persistent diagnosis without a status probe.
// Event-driven board updates use this; existing refresh snapshots re-measure.
func RecordedApprovalAdvice() string {
	if goos != "darwin" {
		return ""
	}
	lastNativeSettings.RLock()
	defer lastNativeSettings.RUnlock()
	return lastNativeSettings.advice
}

var lastNativeSettings struct {
	sync.RWMutex
	value  *Settings
	advice string
}

func rememberNativeSettings(s *Settings) {
	lastNativeSettings.Lock()
	lastNativeSettings.value = s.Normalized()
	if advice, observed := s.ApprovalAdvice(); observed {
		lastNativeSettings.advice = advice
	}
	lastNativeSettings.Unlock()
}

func nativeReceiptSettings(raw []byte) *Settings {
	settings := DecodeSettings(raw)
	if settings != nil {
		rememberNativeSettings(settings)
	}
	return settings
}

// LatestApprovalAdvice is immediate. Native startup/status probes and this
// process's own posting receipts supply the observation; a send never probes
// the notifier or waits for the person. Remote receipts never enter this cache.
func LatestApprovalAdvice() string {
	if goos != "darwin" || silenced() {
		return ""
	}
	lastNativeSettings.RLock()
	settings := lastNativeSettings.value
	lastNativeSettings.RUnlock()
	text, _ := settings.ApprovalAdvice()
	return text
}

// Information explains standing limitations once in doctor, without a warning.
func (s *Settings) Information() string {
	s = s.Normalized()
	if s == nil {
		return "Notification settings are unknown; the notifier supplied no current settings evidence. " +
			"Upgrade the Dibs notifier to measure them; posting does not confirm visibility."
	}
	var info []string
	switch s.TimeSensitiveSetting {
	case "not-supported":
		info = append(info, "time-sensitive: not supported (this build has no Time Sensitive entitlement). "+
			"Requests cannot rely on time-sensitive delivery through Focus.")
	case "unknown":
		info = append(info, "Time Sensitive notification permission is unknown; "+
			"requesting it does not prove it is available.")
	}
	if !s.Focus.Observable {
		info = append(info, "Focus is not observable (authorization "+s.Focus.Authorization+"); "+
			"no permission was requested, and missing evidence does not mean Focus is off.")
	}
	return strings.Join(info, " ")
}

// Summary reports the settings the helper actually observed.
func (s *Settings) Summary() string {
	s = s.Normalized()
	if s == nil {
		return ""
	}
	return "Notification settings: authorization " + s.AuthorizationStatus + ", style " + s.AlertStyle +
		", alerts " + s.AlertSetting + ", Notification Center " + s.NotificationCenterSetting +
		", lock screen " + s.LockScreenSetting + ", time-sensitive " + s.TimeSensitiveSetting + "."
}

// NeedsAttention is about the measured settings, never about actual visibility.
func (s *Settings) NeedsAttention() bool {
	s = s.Normalized()
	return s != nil && (s.AuthorizationStatus == "denied" || s.AuthorizationStatus == "not-determined" ||
		s.AlertStyle == "banner" || s.AlertStyle == "none" ||
		s.AlertSetting == "disabled" || s.NotificationCenterSetting == "disabled" ||
		s.LockScreenSetting == "disabled" || s.TimeSensitiveSetting == "disabled" ||
		(s.Focus.Observable && *s.Focus.IsFocused))
}
