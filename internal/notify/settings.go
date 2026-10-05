package notify

import (
	"encoding/json"
	"strings"
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

// Hints diagnoses settings only. Even an alert with Focus off is not proof that
// a particular notification appeared or that its person read it.
func (s *Settings) Hints(action bool) string {
	s = s.Normalized()
	if s == nil {
		return "Notification settings are unknown; the notifier supplied no current settings evidence. Upgrade the Dibs notifier to measure them; posting does not confirm visibility."
	}
	var hints []string
	if s.AuthorizationStatus == "denied" || s.AuthorizationStatus == "not-determined" {
		hints = append(hints, "Allow Notifications for Dibs in System Settings > Notifications > Dibs.")
	}
	if s.AlertStyle == "none" || s.AlertSetting == "disabled" {
		hints = append(hints, "Dibs has no visible alert style. Choose System Settings > Notifications > Dibs > Alerts.")
	} else if action && s.AlertStyle == "banner" {
		hints = append(hints, "Dibs uses banners, which vanish after a few seconds. For requests with Approve buttons, choose System Settings > Notifications > Dibs > Alerts.")
	}
	if s.NotificationCenterSetting == "disabled" {
		hints = append(hints, "Enable Notification Center in System Settings > Notifications > Dibs to retain notifications there.")
	}
	if s.LockScreenSetting == "disabled" {
		hints = append(hints, "Lock-screen notifications are disabled for Dibs; enable them in System Settings > Notifications > Dibs if wanted.")
	}
	switch s.TimeSensitiveSetting {
	case "disabled":
		if action {
			hints = append(hints, "Time Sensitive notifications are disabled. Enable Time Sensitive in System Settings > Notifications > Dibs if requests should be allowed through Focus.")
		}
	case "not-supported":
		hints = append(hints, "time-sensitive: not supported (this build is not provisioned for it). Requests cannot rely on time-sensitive delivery through Focus.")
	case "unknown":
		if action {
			hints = append(hints, "Time Sensitive notification permission is unknown; requesting it does not prove it is available.")
		}
	}
	if !s.Focus.Observable {
		hints = append(hints, "Focus is not observable (authorization "+s.Focus.Authorization+"); no permission was requested, and missing evidence does not mean Focus is off.")
	} else if *s.Focus.IsFocused {
		hints = append(hints, "Focus is on and may hold notifications. Check Dibs in System Settings > Focus; posting does not confirm visibility.")
	}
	return strings.Join(hints, " ")
}

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
	return s == nil || s.AlertStyle != "alert" ||
		s.AlertSetting != "enabled" || s.NotificationCenterSetting == "disabled" ||
		s.LockScreenSetting == "disabled" || s.TimeSensitiveSetting != "enabled" ||
		(s.Focus.Observable && *s.Focus.IsFocused)
}
