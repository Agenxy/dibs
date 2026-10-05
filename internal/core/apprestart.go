package core

import (
	"fmt"
	"strings"
	"time"
)

const (
	// RestartResumeSetting selects recent app-thread activity after a restart.
	RestartResumeSetting = "wake.resume_after_app_restart"
	// RestartIntervalSetting paces app-thread opens during one sweep.
	RestartIntervalSetting = "wake.restart_open_interval"
)

// RestartSetting is durable provenance for the two coordinator controls.
type RestartSetting struct {
	Value string
	By    string
	At    time.Time
}

// RestartNotice is a pre-restart slot-version snapshot, fenced to one
// incarnation of its agent. It never copies declaration text into the op.
type RestartNotice struct {
	AgentID       string           `json:"agent_id"`
	CreatedSerial uint64           `json:"created_serial"`
	Slots         []RestartSlotRef `json:"slots,omitempty"`
	Epoch         string           `json:"epoch,omitempty"`
	ObservedAt    time.Time        `json:"observed_at,omitzero"`
}

// RestartSlotRef identifies the version of one slot at the old app epoch.
// The slot's text already lives in replayable State and is never copied into
// the restart op, which keeps a fleet sweep below the ledger record bound.
type RestartSlotRef struct {
	ID            string `json:"id"`
	UpdatedSerial uint64 `json:"updated_serial"`
}

func admitRestart(op *Op, lim Limits) error {
	switch op.Kind {
	case OpSetRestartSetting:
		return admitRestartSetting(op)
	case OpAppRestartObserved:
		return admitRestartObservation(op, lim)
	}
	return nil
}

func admitRestartSetting(op *Op) error {
	if op.SettingKey != RestartResumeSetting && op.SettingKey != RestartIntervalSetting {
		return errf("E_NO_SETTING", "use settings with no key to list the two restart controls",
			"unknown restart setting %q", op.SettingKey)
	}
	d, err := time.ParseDuration(op.SettingValue)
	if err != nil || d < 0 || d > 24*time.Hour || (op.SettingKey == RestartIntervalSetting && d == 0) {
		return errf("E_BAD_SETTING", "give a duration such as 1h or 2s; zero disables resume, not pacing",
			"invalid restart setting %q = %q", op.SettingKey, op.SettingValue)
	}
	return nil
}

func admitRestartObservation(op *Op, lim Limits) error {
	if op.RestartEpoch == "" || len(op.RestartEpoch) > 300 || len(op.RestartNotices) > lim.MaxAgents {
		return errf("E_BAD_RESTART", "record one bounded app epoch and at most one notice per agent",
			"invalid restart observation")
	}
	for _, n := range op.RestartNotices {
		if n.AgentID == "" || len(n.AgentID) > lim.MaxNameBytes || len(n.Slots) > lim.MaxSlotsPerAgent {
			return errf("E_BAD_RESTART", "keep the restart roster bounded", "invalid restart notice")
		}
		for _, slot := range n.Slots {
			if slot.ID == "" || len(slot.ID) > lim.MaxNameBytes {
				return errf("E_BAD_RESTART", "record short slot ids only", "invalid restart slot reference")
			}
		}
	}
	return nil
}

func (s *State) applyRestartSetting(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	if !l.IsCoordinator() {
		return nil, nil, ErrNotCoordinator
	}
	if s.RestartSettings == nil {
		s.RestartSettings = map[string]RestartSetting{}
	}
	s.RestartSettings[op.SettingKey] = RestartSetting{Value: op.SettingValue, By: l.ID, At: now}
	return Result{"setting": op.SettingKey, "value": op.SettingValue, "by": l.ID, "saved": true},
		[]Event{{Type: "setting.changed", Agent: l.ID}}, nil
}

func (s *State) applyAppRestartObserved(op *Op, now time.Time) (Result, []Event, error) {
	if op.RestartEpoch == s.RestartEpoch {
		return Result{"ok": true, "changed": false}, nil, nil
	}
	s.RestartEpoch = op.RestartEpoch
	if s.RestartNotices == nil {
		s.RestartNotices = map[string]RestartNotice{}
	}
	for _, n := range op.RestartNotices {
		l := s.Agents[n.AgentID]
		if l == nil || l.CreatedSerial != n.CreatedSerial {
			continue
		}
		n.Epoch = op.RestartEpoch
		n.ObservedAt = op.RestartObservedAt
		s.RestartNotices[n.AgentID] = n
	}
	kind := "app.restarted"
	if op.RestartBaseline {
		kind = "app.epoch_observed"
	}
	evs := []Event{{Type: kind, Data: map[string]any{"notices": len(op.RestartNotices)}}}
	s.finish(&evs, now)
	return Result{"ok": true, "changed": true}, evs, nil
}

func (s *State) applyReadAppRestart(l *Agent) (Result, []Event) {
	n, ok := s.RestartNotices[l.ID]
	if !ok {
		return Result{"ok": true}, nil
	}
	delete(s.RestartNotices, l.ID)
	if n.CreatedSerial != l.CreatedSerial {
		return Result{"ok": true}, []Event{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Dibs observed the ChatGPT app restart at %s. Your declarations at that time:",
		n.ObservedAt.UTC().Format(time.RFC3339))
	if len(n.Slots) == 0 {
		b.WriteString(" none.")
	} else {
		for _, ref := range n.Slots {
			slot, exists := l.Slots[ref.ID]
			switch {
			case !exists:
				fmt.Fprintf(&b, "\n- A declaration you held at the restart (slot %s) has since been cleared.", ref.ID)
			case slot.UpdatedSerial != ref.UpdatedSerial:
				fmt.Fprintf(&b, "\n- Updated since the restart (slot %s): %s", ref.ID, slot.Text)
			default:
				fmt.Fprintf(&b, "\n- Before the restart you declared (slot %s): %s", ref.ID, slot.Text)
			}
		}
	}
	return Result{"notice": b.String()}, []Event{}
}

// gcRestartNotices discards snapshots that no surviving incarnation can read.
// It runs inside a sweep, so this silent deletion advances the serial and is
// recorded in the ledger just like the other deterministic GC work.
func (s *State) gcRestartNotices() bool {
	pruned := false
	for id, notice := range s.RestartNotices {
		l := s.Agents[id]
		if l == nil || l.Retired() || l.CreatedSerial != notice.CreatedSerial {
			delete(s.RestartNotices, id)
			pruned = true
		}
	}
	return pruned
}
