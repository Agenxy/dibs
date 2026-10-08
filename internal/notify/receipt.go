// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// Receipt reports OS acceptance (posted) or an explicit dismissal. Posted
// does not establish that a banner was visible or that a person saw it.
type Receipt func(state string)

// DeliveryReceipt carries the helper's actual settings alongside OS acceptance.
// Legacy callbacks still receive states; they never invent settings evidence.
type DeliveryReceipt func(ReceiptData)

// ReceiptData separates OS acceptance, requested level and native settings.
type ReceiptData struct {
	State                      string    `json:"state"`
	Settings                   *Settings `json:"settings"`
	InterruptionLevel          string    `json:"interruption_level,omitempty"` // legacy alias for requested
	RequestedInterruptionLevel string    `json:"requested_interruption_level,omitempty"`
	EffectiveInterruptionLevel string    `json:"effective_interruption_level,omitempty"`
	InterruptionReason         string    `json:"interruption_reason,omitempty"`
}

func stateReceipt(receipt Receipt) DeliveryReceipt {
	if receipt == nil {
		return nil
	}
	return func(data ReceiptData) { receipt(data.State) }
}

// Normalized returns a bounded independent observation from a helper or relay.
func (r ReceiptData) Normalized() ReceiptData {
	r.Settings = r.Settings.Normalized()
	requested := r.RequestedInterruptionLevel
	if requested == "" {
		requested = r.InterruptionLevel
	}
	if requested != "" {
		r.RequestedInterruptionLevel = enum(requested, "active", "timeSensitive")
		r.InterruptionLevel = r.RequestedInterruptionLevel
	}
	switch r.RequestedInterruptionLevel {
	case "timeSensitive":
		switch {
		case r.InterruptionReason == "osascript cannot request Time Sensitive":
			r.EffectiveInterruptionLevel = "active"
		case r.Settings == nil:
			r.EffectiveInterruptionLevel, r.InterruptionReason = "unknown", "notification settings unavailable"
		case r.Settings.TimeSensitiveSetting == "enabled":
			r.EffectiveInterruptionLevel, r.InterruptionReason = "timeSensitive", ""
		case r.Settings.TimeSensitiveSetting == "not-supported":
			r.EffectiveInterruptionLevel = "active"
			r.InterruptionReason = "Time Sensitive requested but not available to this build (no entitlement)"
		case r.Settings.TimeSensitiveSetting == "disabled":
			r.EffectiveInterruptionLevel, r.InterruptionReason = "active", "Time Sensitive notifications are disabled"
		default:
			r.EffectiveInterruptionLevel, r.InterruptionReason = "unknown", "Time Sensitive setting is unknown"
		}
	case "active":
		r.EffectiveInterruptionLevel, r.InterruptionReason = "active", ""
	default:
		r.EffectiveInterruptionLevel, r.InterruptionReason = "", ""
	}
	return r
}

// A private 0700 per-invocation directory below the data directory makes
// the receipt protocol additive: an older helper
// ignores the environment variable and supplies no receipt. It also survives
// launchctl asuser, which does not carry the helper's stdout back to us.
func outputWithReceipt(cmd *exec.Cmd, receipt Receipt) ([]byte, error) {
	return outputWithDeliveryReceipt(cmd, stateReceipt(receipt))
}

func outputWithDeliveryReceipt(cmd *exec.Cmd, receipt DeliveryReceipt) ([]byte, error) {
	if receipt == nil {
		return cmd.Output()
	}
	base := paths.DataDir()
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "notification-receipt-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// OpenRoot confines even symlinks to this private 0700 invocation directory.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile("receipt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(dir, "receipt")
	_ = f.Close()
	defer func() { _ = os.Remove(name) }()
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "DIBS_NOTIFY_RECEIPT="+name)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		last := ""
		read := func() {
			b, readErr := root.ReadFile("receipt") // confined to the private directory
			var r struct {
				State             string          `json:"state"`
				Settings          json.RawMessage `json:"settings"`
				InterruptionLevel json.RawMessage `json:"interruption_level"`
			}
			if readErr == nil && json.Unmarshal(b, &r) == nil &&
				(r.State == "posted" || r.State == "dismissed") && string(b) != last {
				last = string(b)
				var level string
				_ = json.Unmarshal(r.InterruptionLevel, &level)
				receipt(ReceiptData{State: r.State, Settings: DecodeSettings(r.Settings), InterruptionLevel: level}.Normalized())
			}
		}
		for {
			select {
			case <-tick.C:
				read()
			case <-stop:
				read()
				return
			}
		}
	}()
	out, err := cmd.Output()
	close(stop)
	<-done
	return out, err
}
