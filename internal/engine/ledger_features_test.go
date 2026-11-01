// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Literal kind intentionally compiles on the pre-fence source for the RED
// control, and states the minimum reader in an old reader's refusal message.
const requiredV17Kind = "require_dibs_v0.0.17"

func TestRecoveryWriterFencesBeforeFirstFeatureAppend(t *testing.T) {
	led := &memLedger{}
	e, ctx := runContactReplayEngine(t, core.NewState("fence", core.DefaultLimits()), led, nil)
	do := func(op *core.Op) core.Result { return contactReplayDo(t, e, ctx, op) }
	tokens := map[string]string{}
	for _, id := range []string{"source", "survivor", "lead"} {
		session := ""
		if id != "source" {
			session = id + "-session"
		}
		tokens[id] = do(&core.Op{
			Kind: core.OpRegister, Name: id, Nonce: "fence-" + id,
			AgentKind: core.KindPersistent, SessionID: session,
			Agent: &core.AgentInfo{HostID: e.HostID(), Harness: "Codex", Surface: "cli"},
		})["token"].(string)
		do(&core.Op{Kind: core.OpAckBoard, Token: tokens[id]})
	}
	do(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"source"}})
	if _, err := e.GrantRole(ctx, "lead", core.RoleCoordinator); err != nil {
		t.Fatal("setup grant:", err)
	}
	plan, err := e.RecoverAgent(ctx, tokens["survivor"], "source", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatal("setup preview:", err)
	}
	assertFeatureMarkerCount(t, e, ctx, led, 0)
	req, err := e.RecoverAgent(ctx, tokens["survivor"], "source", MergeOptions{Fence: plan["fence"].(string)})
	if err != nil {
		t.Fatal("setup recovery:", err)
	}
	assertFeatureMarkerCount(t, e, ctx, led, 1)
	if _, err := e.query(ctx, func() core.Result {
		for i, op := range led.ops {
			if op.Kind == core.OpSendMessage && op.Recovery != nil {
				if i == 0 || led.ops[i-1].Kind != requiredV17Kind {
					t.Error("recovery append was not preceded by its requirement marker")
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	do(&core.Op{Kind: core.OpRespond, Token: tokens["lead"], MsgSerial: req["msg_serial"].(uint64), Disposition: "approve"})
	assertFeatureMarkerCount(t, e, ctx, led, 1)
}

func assertFeatureMarkerCount(t *testing.T, e *Engine, ctx context.Context, led *memLedger, want int) {
	t.Helper()
	if _, err := e.query(ctx, func() core.Result {
		count := 0
		for _, op := range led.ops {
			if op.Kind == requiredV17Kind {
				count++
				raw, err := json.Marshal(op)
				var fields map[string]any
				if err != nil || json.Unmarshal(raw, &fields) != nil || fmt.Sprint(fields["required_features"]) != "[mailbox-recovery/v1]" {
					t.Errorf("marker does not name the required feature: %s %v", raw, err)
				}
			}
		}
		if count != want {
			t.Errorf("requirement marker count = %d, want %d", count, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func historicalRecoveryState(t *testing.T, appendRecord func(uint64, time.Time, *core.Op)) *core.State {
	t.Helper()
	st := core.NewState("contact-restart", core.DefaultLimits())
	now := time.Now()
	apply := func(op *core.Op) {
		t.Helper()
		if _, _, err := st.Apply(op, now); err != nil {
			t.Fatal("historical fold:", err)
		}
		if appendRecord != nil {
			appendRecord(st.Serial, now, op)
		}
	}
	for _, id := range []string{"source", "survivor", "lead"} {
		apply(&core.Op{Kind: core.OpRegister, Name: id, Nonce: id + "-nonce", NewToken: id + "-token", AgentKind: core.KindPersistent})
	}
	for _, id := range []string{"source", "survivor", "lead"} {
		apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
	}
	apply(&core.Op{Kind: core.OpGrantRole, To: "lead", Mode: core.RoleCoordinator})
	apply(&core.Op{Kind: core.OpSetSlot, Token: "source-token", SlotID: "s1", Text: "historical declaration"})
	apply(&core.Op{
		Kind: core.OpSendMessage, Token: "survivor-token", To: "lead", MsgType: core.MsgRequest, Body: "historical consent",
		Recovery: &core.RecoveryRequest{From: "source", Into: "survivor", SourceCreated: 1, SurvivorCreated: 2, CoordinatorCreated: 3, Fence: "historical"},
	})
	return st
}

func TestBootFencesHistoricalRecoveryOnce(t *testing.T) {
	st := historicalRecoveryState(t, nil)
	now := time.Now()
	var encoded bytes.Buffer
	var baseline core.State
	if err := gob.NewEncoder(&encoded).Encode(st); err != nil {
		t.Fatal(err)
	}
	if err := gob.NewDecoder(&encoded).Decode(&baseline); err != nil {
		t.Fatal(err)
	}
	led := &memLedger{}
	e, ctx := runContactReplayEngine(t, st, led, nil)
	assertFeatureMarkerCount(t, e, ctx, led, 1)
	var recorded []*core.Op
	var serial uint64
	if _, err := e.query(ctx, func() core.Result {
		recorded, serial = append(recorded, led.ops...), e.state.Serial
		if !reflect.DeepEqual(e.state.Agents, baseline.Agents) {
			t.Error("marker boot changed an agent's record")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Re-fold only boot's actual appended ops on the pre-upgrade state. No
	// second boot may create another marker or advance the serial.
	for _, op := range recorded {
		if _, _, err := baseline.Apply(op, now); err != nil {
			t.Fatal("replay boot:", err)
		}
	}
	secondLedger := &memLedger{}
	second, secondCtx := runContactReplayEngine(t, &baseline, secondLedger, nil)
	if _, err := second.query(secondCtx, func() core.Result {
		if second.state.Serial != serial || len(secondLedger.ops) != 0 {
			t.Errorf("second boot wrote again: serial %d -> %d, ops %v", serial, second.state.Serial, secondLedger.ops)
		}
		raw, err := json.Marshal(second.state)
		var fields map[string]any
		if err != nil || json.Unmarshal(raw, &fields) != nil || !reflect.DeepEqual(fields["required_features"], []any{"mailbox-recovery/v1"}) {
			t.Errorf("requirement set was not replayed: %v %v", fields["required_features"], err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReaderRequirementIsWriterOwned(t *testing.T) {
	led := &memLedger{}
	e, ctx := runContactReplayEngine(t, core.NewState("writer-owned", core.DefaultLimits()), led, nil)
	token := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: "member"})["token"].(string)
	before := contactLedgerSize(t, e, ctx, led)
	if _, err := e.Do(ctx, &core.Op{
		Kind: requiredV17Kind, Token: token,
		RequiredFeatures: []string{core.FeatureMailboxRecovery},
	}); err == nil {
		t.Fatal("caller submitted the writer's requirement marker")
	}
	contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpHeartbeat, Token: token,
		RequiredFeatures: []string{core.FeatureMailboxRecovery},
	})
	if contactLedgerSize(t, e, ctx, led) != before {
		t.Fatal("forged marker or heartbeat requirement wrote replayable state")
	}
	assertFeatureMarkerCount(t, e, ctx, led, 0)
}

func TestDesktopPolicyWriterFencesOnlyAChangedSetting(t *testing.T) {
	led := &memLedger{}
	e, ctx := runContactReplayEngine(t, core.NewState("policy-fence", core.DefaultLimits()), led, nil)
	token := contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: "member"})["token"].(string)
	contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpSetDesktopApprovalSetting, Token: token,
		SettingKey: core.DesktopApprovalSettingKey, SettingValue: "true",
	})
	if _, err := e.query(ctx, func() core.Result {
		found := false
		for i, op := range led.ops {
			if op.Kind == core.OpSetDesktopApprovalSetting {
				found = true
				if i == 0 || led.ops[i-1].Kind != requiredV17Kind ||
					!reflect.DeepEqual(led.ops[i-1].RequiredFeatures, []string{core.FeatureDesktopApproval}) {
					t.Error("changed desktop policy lacks its preceding feature requirement")
				}
			}
		}
		if !found {
			t.Error("setup: policy change was not appended")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := contactLedgerSize(t, e, ctx, led)
	contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpSetDesktopApprovalSetting, Token: token,
		SettingKey: core.DesktopApprovalSettingKey, SettingValue: "true",
	})
	if contactLedgerSize(t, e, ctx, led) != before {
		t.Fatal("unchanged desktop setting appended another marker or policy")
	}
}

type failedFeatureLedger struct{ attempted []string }

func (l *failedFeatureLedger) Append(_ uint64, _ time.Time, op *core.Op) error {
	l.attempted = append(l.attempted, op.Kind)
	return errors.New("fixture: requirement fsync failed")
}

func TestFeatureAppendCannotPassAFailedFence(t *testing.T) {
	st := core.NewState("failed-fence", core.DefaultLimits())
	now := time.Now()
	if _, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: "member", NewToken: "member-token"}, now); err != nil {
		t.Fatal(err)
	}
	disk := &failedFeatureLedger{}
	e := New(st, disk, deadProber{})
	var fault any
	func() {
		defer func() { fault = recover() }()
		_, _ = e.exec(&core.Op{
			Kind: core.OpSetDesktopApprovalSetting, Token: "member-token",
			SettingKey: core.DesktopApprovalSettingKey, SettingValue: "true",
		}, now)
	}()
	if fault == nil || !strings.Contains(fmt.Sprint(fault), "ledger persistence failure") ||
		!reflect.DeepEqual(disk.attempted, []string{requiredV17Kind}) || !st.DesktopApproval.IsZero() {
		t.Fatalf("feature passed a failed requirement append: fault=%v attempted=%v policy=%+v", fault, disk.attempted, st.DesktopApproval)
	}
}
