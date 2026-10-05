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
	reg(t, s, "boss", "tb", t0)
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "lead", Mode: RoleCoordinator}, t0)
	mustApply(t, s, &Op{Kind: OpGrantRole, To: "boss", Mode: RoleAdmin}, t0)
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
	adminOp := &Op{Kind: OpSetRestartSetting, Token: "tb", SettingKey: RestartIntervalSetting, SettingValue: "2s"}
	mustApply(t, s, adminOp, t0)
	if got := s.RestartSettings[adminOp.SettingKey]; got.Value != "2s" || got.By != "boss" {
		t.Fatalf("admin setting provenance = %+v", got)
	}
	replay := NewState("n1", DefaultLimits())
	reg(t, replay, "member", "tm", t0)
	reg(t, replay, "lead", "tl", t0)
	reg(t, replay, "boss", "tb", t0)
	mustApply(t, replay, &Op{Kind: OpGrantRole, To: "lead", Mode: RoleCoordinator}, t0)
	mustApply(t, replay, &Op{Kind: OpGrantRole, To: "boss", Mode: RoleAdmin}, t0)
	mustApply(t, replay, &Op{Kind: OpSetRestartSetting, AgentID: "lead", SettingKey: op.SettingKey, SettingValue: op.SettingValue}, t0)
	mustApply(t, replay, &Op{Kind: OpSetRestartSetting, AgentID: "boss", SettingKey: adminOp.SettingKey, SettingValue: adminOp.SettingValue}, t0)
	if replay.RestartSettings[op.SettingKey] != s.RestartSettings[op.SettingKey] {
		t.Fatal("replay differs from live setting")
	}
	if replay.RestartSettings[adminOp.SettingKey] != s.RestartSettings[adminOp.SettingKey] {
		t.Fatal("replay differs from live admin setting")
	}
	if err := Admit(&Op{Kind: OpSetRestartSetting, SettingKey: op.SettingKey, SettingValue: "-1h"}, DefaultLimits()); err == nil {
		t.Fatal("negative duration admitted")
	}
}

func TestRestartNoticesArePrunedByLedgeredSweep(t *testing.T) {
	for _, tc := range []struct {
		name   string
		retire func(*testing.T, *State)
	}{
		{name: "closed", retire: func(t *testing.T, s *State) {
			mustApply(t, s, &Op{Kind: OpSignOff, Token: "tw"}, t0)
		}},
		{name: "replaced incarnation", retire: func(_ *testing.T, s *State) {
			s.Agents["worker"].CreatedSerial++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("n1", DefaultLimits())
			reg(t, s, "worker", "tw", t0)
			l := s.Agents["worker"]
			mustApply(t, s, &Op{
				Kind: OpAppRestartObserved, RestartEpoch: "app:2", RestartObservedAt: t0,
				RestartNotices: []RestartNotice{{AgentID: l.ID, CreatedSerial: l.CreatedSerial}},
			}, t0)
			if len(s.RestartNotices) != 1 {
				t.Fatalf("setup did not record restart notice: %v", s.RestartNotices)
			}
			tc.retire(t, s)
			base := s.Serial
			res, evs, err := s.Apply(&Op{Kind: OpSweep}, t0.Add(time.Second))
			// Silent GC emits no event; its changed result and serial advance
			// are what cause the engine to append the sweep to the ledger.
			if err != nil || res["changed"] != true || len(evs) != 0 || s.Serial != base+1 {
				t.Fatalf("notice-only sweep not ledgerable: res=%v events=%v serial=%d err=%v", res, evs, s.Serial, err)
			}
			if len(s.RestartNotices) != 0 {
				t.Fatalf("stale restart notice retained: %v", s.RestartNotices)
			}
			base = s.Serial
			res, evs, err = s.Apply(&Op{Kind: OpSweep}, t0.Add(2*time.Second))
			if err != nil || res["changed"] != false || evs != nil || s.Serial != base {
				t.Fatalf("repeat sweep changed state: res=%v events=%v serial=%d err=%v", res, evs, s.Serial, err)
			}
		})
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
