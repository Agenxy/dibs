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
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Literal kind intentionally compiles on the pre-fence source for the RED
// control, and states the minimum reader in an old reader's refusal message.
const requiredV17Kind = "require_dibs_v0.0.17"

func recoveryFenceBoard(t *testing.T) (*Engine, context.Context, *memLedger, map[string]string) {
	t.Helper()
	led := &memLedger{}
	e, ctx := runContactReplayEngine(t, core.NewState("fence", core.DefaultLimits()), led, nil)
	return e, ctx, led, populateRecoveryFenceBoard(t, e, ctx)
}

func populateRecoveryFenceBoard(t *testing.T, e *Engine, ctx context.Context) map[string]string {
	t.Helper()
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
	return tokens
}

func BenchmarkReaderBackstopAffectedRecords(b *testing.B) {
	for _, count := range []int{16_384, 65_536} {
		for _, fenced := range []bool{false, true} {
			b.Run(strconv.Itoa(count)+"/fenced="+strconv.FormatBool(fenced), func(b *testing.B) {
				st := core.NewState("bounded-census", core.DefaultLimits())
				for i := range 64 {
					st.Agents[strconv.Itoa(i)] = &core.Agent{}
				}
				for i := range count {
					st.Messages[uint64(i+1)] = &core.Message{Serial: uint64(i + 1)}
				}
				st.Serial = uint64(count + 1)
				if fenced {
					st.RequiredFeatures = []string{core.FeatureDesktopApproval, core.FeatureMailboxRecovery}
				}
				e := New(st, &memLedger{}, deadProber{})
				op := &core.Op{Kind: core.OpActivityCheckpoint, AgentID: "1", MsgSerial: 1}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if len(e.missingReaderFeaturesAt(op, nil)) != 0 {
						b.Fatal("setup: unrelated records unexpectedly contain recovery data")
					}
				}
			})
		}
	}
}

func TestRecoveryWriterFencesBeforeFirstFeatureAppend(t *testing.T) {
	e, ctx, led, tokens := recoveryFenceBoard(t)
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
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRespond, Token: tokens["lead"], MsgSerial: req["msg_serial"].(uint64), Disposition: "approve"})
	assertFeatureMarkerCount(t, e, ctx, led, 1)
}

// The strict prefix guard above remains separate. This guard also runs under
// the prediction-removal mutation: a late marker must warn, and no completed
// writer trip may leave feature data without its durable reader requirement.
func TestRecoveryWriterAlwaysFencesAndWarnsOnBackstop(t *testing.T) {
	var captured continuationWakeLog
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&captured, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)
	e, ctx, led, tokens := recoveryFenceBoard(t)
	plan, err := e.RecoverAgent(ctx, tokens["survivor"], "source", MergeOptions{DryRun: true})
	if err != nil {
		t.Fatal("setup preview:", err)
	}
	if _, err := e.RecoverAgent(ctx, tokens["survivor"], "source", MergeOptions{Fence: plan["fence"].(string)}); err != nil {
		t.Fatal("setup recovery:", err)
	}
	assertFeatureMarkerCount(t, e, ctx, led, 1)
	if _, err := e.query(ctx, func() core.Result {
		marker, feature := -1, -1
		for i, op := range led.ops {
			if op.Kind == requiredV17Kind {
				marker = i
			}
			if op.Kind == core.OpSendMessage && op.Recovery != nil {
				feature = i
			}
		}
		if marker < 0 || feature < 0 || !reflect.DeepEqual(e.state.RequiredFeatures, e.state.FeatureData()) {
			t.Error("completed recovery append has unfenced feature data")
		}
		warned := strings.Contains(captured.String(), "level=WARN msg=\"ledger reader requirement backstop\" op=send")
		if warned != (marker > feature) {
			t.Errorf("late marker must warn naming its op: marker=%d feature=%d logs=%s", marker, feature, captured.String())
		}
		t.Logf("marker index=%d feature index=%d backstop Warn=%v logs=%s", marker, feature, warned, captured.String())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRefusedFeatureAppendKeepsOnlyOneMarkerOnRetry(t *testing.T) {
	led := &memLedger{}
	e, ctx := runContactReplayEngine(t, core.NewState("refused-fence", core.DefaultLimits()), led, nil)
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpRegister, Name: "retained-actor"})
	if _, err := e.query(ctx, func() core.Result {
		before, records := e.state.Serial, len(led.ops)
		for attempt := range 2 {
			// Exercise the writer's admitted-but-refused fold directly. Public
			// authentication normally refuses this token before reaching it.
			op := &core.Op{
				Kind: core.OpSetDesktopApprovalSetting, Token: "refused-token",
				SettingKey: core.DesktopApprovalSettingKey, SettingValue: "true",
			}
			if err := e.state.Admit(op); err != nil {
				return core.Result{"error": err}
			}
			if _, err := e.applyAndLedger(op, time.Now()); !errors.Is(err, core.ErrBadToken) {
				t.Errorf("expected fold refusal on attempt %d: %v", attempt, err)
			}
			if e.state.Serial != before+1 || len(led.ops) != records+1 ||
				led.ops[records].Kind != requiredV17Kind || !e.state.DesktopApproval.IsZero() {
				t.Errorf("refused feature or retry appended more than its first marker: attempt=%d serial=%d records=%d",
					attempt, e.state.Serial, len(led.ops))
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
