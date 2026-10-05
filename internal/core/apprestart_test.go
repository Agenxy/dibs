package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRestartSettingsAreCoordinatorScopedAndReplayable(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "member", "tm", t0)
	reg(t, s, "lead", "tl", t0)
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "lead", Mode: RoleCoordinator}, t0)
	base := s.Serial
	if _, _, err := s.Apply(&Op{Kind: OpSetRestartSetting, Token: "tm", SettingKey: "wake.resume_after_app_restart", SettingValue: "1h"}, t0); !errors.Is(err, ErrNotCoordinator) {
		t.Fatalf("member changed restart setting: %v", err)
	}
	if s.Serial != base {
		t.Fatal("refused setting changed serial")
	}
	op := &Op{Kind: OpSetRestartSetting, Token: "tl", SettingKey: "wake.resume_after_app_restart", SettingValue: "1h"}
	if err := s.Admit(op); err != nil {
		t.Fatal(err)
	}
	mustApply(t, s, op, t0)
	if got := s.RestartSettings[op.SettingKey]; got.Value != "1h" || got.By != "lead" || !got.At.Equal(t0) {
		t.Fatalf("setting provenance = %+v", got)
	}
	replay := NewState("n1", DefaultLimits())
	reg(t, replay, "member", "tm", t0)
	reg(t, replay, "lead", "tl", t0)
	mustApply(t, replay, &Op{Kind: OpGrantRole, To: "lead", Mode: RoleCoordinator}, t0)
	mustApply(t, replay, &Op{Kind: OpSetRestartSetting, AgentID: "lead", SettingKey: op.SettingKey, SettingValue: op.SettingValue}, t0)
	if replay.RestartSettings[op.SettingKey] != s.RestartSettings[op.SettingKey] {
		t.Fatal("replay differs from live setting")
	}
	if err := Admit(&Op{Kind: OpSetRestartSetting, SettingKey: op.SettingKey, SettingValue: "-1h"}, DefaultLimits()); err == nil {
		t.Fatal("negative duration admitted")
	}
}

func TestRestartSnapshotIsReadOnceAndFencedToAgentIncarnation(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "worker", "tw", t0)
	l := s.Agents["worker"]
	l.Slots = map[string]Slot{
		"s1": {ID: "s1", Text: "build the feature", UpdatedSerial: 41},
	}
	first := &Op{Kind: OpAppRestartObserved, RestartEpoch: "app:1", RestartBaseline: true, RestartObservedAt: t0}
	_, initialEvents, err := s.Apply(first, t0)
	if err != nil || len(initialEvents) != 1 || initialEvents[0].Type != "app.epoch_observed" {
		t.Fatalf("baseline is not a restart: events=%v err=%v", initialEvents, err)
	}
	base := s.Serial
	if _, evs, err := s.Apply(&Op{Kind: OpAppRestartObserved, RestartEpoch: "app:1"}, t0); err != nil || evs != nil || s.Serial != base {
		t.Fatalf("duplicate epoch mutated: events=%v err=%v serial=%d", evs, err, s.Serial)
	}
	second := &Op{Kind: OpAppRestartObserved, RestartEpoch: "app:2", RestartObservedAt: t0.Add(time.Minute), RestartNotices: []RestartNotice{{AgentID: l.ID, CreatedSerial: l.CreatedSerial, Slots: []RestartSlotRef{{ID: "s1", UpdatedSerial: 41}}}}}
	encoded, err := json.Marshal(second)
	if err != nil || strings.Contains(string(encoded), "build the feature") {
		t.Fatalf("restart op copied declaration text: %v %s", err, encoded)
	}
	mustApply(t, s, second, t0.Add(time.Minute))
	read := &Op{Kind: OpReadAppRestart, Token: "tw"}
	res, _, err := s.Apply(read, t0.Add(2*time.Minute))
	if err != nil || !strings.Contains(res["notice"].(string), "build the feature") {
		t.Fatalf("first read = %v, %v", res, err)
	}
	res, evs, err := s.Apply(&Op{Kind: OpReadAppRestart, Token: "tw"}, t0.Add(3*time.Minute))
	if err != nil || evs != nil || res["notice"] != nil {
		t.Fatalf("second read = %v, %v, %v", res, evs, err)
	}
	third := &Op{Kind: OpAppRestartObserved, RestartEpoch: "app:3", RestartObservedAt: t0.Add(4 * time.Minute), RestartNotices: []RestartNotice{{AgentID: l.ID, CreatedSerial: l.CreatedSerial, Slots: []RestartSlotRef{{ID: "s1", UpdatedSerial: 41}}}}}
	mustApply(t, s, third, t0.Add(4*time.Minute))
	l.CreatedSerial++ // a re-created row must not inherit its predecessor's notice
	res, _, err = s.Apply(&Op{Kind: OpReadAppRestart, Token: "tw"}, t0.Add(5*time.Minute))
	if err != nil || res["notice"] != nil {
		t.Fatalf("stale incarnation read = %v, %v", res, err)
	}
}
