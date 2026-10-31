// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/humanauth"
)

func approvalEngine(t *testing.T, checker desktopPresenceChecker, notifier HumanNotifier) (*Engine, context.Context, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	e := New(core.NewState("approval-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	e.desktopApprovalPresence = checker // replace only the OS sensor, before Run
	if notifier == nil {
		notifier = unavailablePriorityNotifier{}
	}
	e.SetHumanNotifier(notifier)
	go e.Run(ctx)
	if _, _, err := e.HumanAgent(ctx); err != nil {
		t.Fatal("human setup:", err)
	}
	return e, ctx, registerAgent(t, e, "approval-sender")
}

func approvalSend(t *testing.T, e *Engine, ctx context.Context, token, kind, grant string) uint64 {
	t.Helper()
	r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: "human", MsgType: kind,
		Grant: grant, Body: "UNTRUSTED request text must not become the sensor reason"})
	if err != nil || r["msg_serial"] == nil {
		t.Fatalf("request setup: %v %v", r, err)
	}
	return r["msg_serial"].(uint64)
}

func enableApproval(t *testing.T, e *Engine, ctx context.Context, token string) {
	t.Helper()
	if _, err := e.Configure(ctx, token, core.DesktopApprovalSettingKey, "true"); err != nil {
		t.Fatal("enable setup:", err)
	}
}

func TestDesktopApprovalPolicyThroughNativeAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, kind, grant, disposition string
		enabled                        bool
		verdict                        humanauth.Verdict
		sensorError                    error
		wantChecks                     int32
		wantError, pending             bool
	}{
		{"default-off", core.MsgRequest, core.RoleCoordinator, "approve", false, humanauth.Declined, nil, 0, false, false},
		{"verified-grant", core.MsgRequest, core.RoleCoordinator, "approve", true, humanauth.Verified, nil, 1, false, false},
		{"canceled", core.MsgRequest, core.RoleCoordinator, "approve", true, humanauth.Declined, nil, 1, true, true},
		{"unavailable", core.MsgRequest, core.RoleCoordinator, "approve", true, humanauth.Unavailable, nil, 1, true, true},
		{"busy", core.MsgRequest, core.RoleCoordinator, "approve", true, humanauth.Declined, humanauth.ErrPromptBusy, 1, true, true},
		{"question", core.MsgQuestion, "", "answer", true, humanauth.Declined, nil, 0, false, false},
		{"ordinary-request", core.MsgRequest, "", "approve", true, humanauth.Declined, nil, 0, false, false},
		{"deny-grant", core.MsgRequest, core.RoleCoordinator, "deny", true, humanauth.Declined, nil, 0, false, false},
		{"comment-grant", core.MsgRequest, core.RoleCoordinator, "comment", true, humanauth.Declined, nil, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			reasons := make(chan string, 1)
			e, ctx, token := approvalEngine(t, func(_ context.Context, reason string) (humanauth.Verdict, error) {
				calls.Add(1)
				reasons <- reason
				return tc.verdict, tc.sensorError
			}, nil)
			if tc.enabled {
				enableApproval(t, e, ctx, token)
			}
			serial := approvalSend(t, e, ctx, token, tc.kind, tc.grant)
			err := e.AnswerNativeHuman(ctx, "approval-node", serial, tc.disposition, "human reply", "submission")
			if (err != nil) != tc.wantError || calls.Load() != tc.wantChecks {
				t.Fatalf("decision: error=%v checks=%d; want error=%v checks=%d", err, calls.Load(), tc.wantError, tc.wantChecks)
			}
			if tc.wantError {
				var fault *core.Error
				if !errors.As(err, &fault) || !strings.Contains(fault.Msg, "pending") || !strings.Contains(fault.Hint, "dibs web") {
					t.Fatalf("refusal lost pending state or board corrective: %v", err)
				}
			}
			if tc.wantChecks > 0 {
				reason := <-reasons
				if !strings.Contains(reason, fmt.Sprint(serial)) || !strings.Contains(reason, "approval-node") ||
					!strings.Contains(reason, core.RoleCoordinator) || strings.Contains(reason, "UNTRUSTED") {
					t.Fatalf("sensor reason is not daemon-owned exact effect data: %q", reason)
				}
			}
			view, err := e.NativeHumanMessage(ctx, "approval-node", serial)
			if err != nil || view.Pending != tc.pending {
				t.Fatalf("final request state: %+v %v", view, err)
			}
			_, err = e.query(ctx, func() core.Result {
				granted := e.state.AgentByToken(token).IsCoordinator()
				wantGrant := tc.grant != "" && tc.disposition == "approve" && !tc.wantError
				if granted != wantGrant {
					t.Errorf("actual grant=%v want=%v", granted, wantGrant)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type approvalDesktop struct{ posted chan humanask.Message }

func (approvalDesktop) Available() bool { return true }
func (n approvalDesktop) Ask(m humanask.Message) (humanask.Answer, error) {
	m.Receipt("posted")
	n.posted <- m
	return humanask.Answer{Disposition: "approve"}, nil
}

// Enter through Send, dispatch and the live desktop callback, rather than
// calling the gate directly. A cold reopen uses AnswerNativeHuman above.
func TestLiveDesktopApprovalReachesPresenceGate(t *testing.T) {
	checked := make(chan string, 1)
	posted := make(chan humanask.Message, 1)
	e, ctx, token := approvalEngine(t, func(_ context.Context, reason string) (humanauth.Verdict, error) {
		checked <- reason
		return humanauth.Declined, nil
	}, approvalDesktop{posted})
	enableApproval(t, e, ctx, token)
	serial := approvalSend(t, e, ctx, token, core.MsgRequest, core.RoleCoordinator)
	select {
	case m := <-posted:
		if m.Serial != serial || m.Node != "approval-node" || m.Grant != core.RoleCoordinator {
			t.Fatalf("desktop setup lost request identity: %+v", m)
		}
	case <-ctx.Done():
		t.Fatal("live desktop path never entered")
	}
	select {
	case <-checked:
	case <-ctx.Done():
		t.Fatal("live desktop approve bypassed the presence gate")
	}
	view, err := e.NativeHumanMessage(ctx, "approval-node", serial)
	if err != nil || !view.Pending {
		t.Fatalf("canceled live approval decided the request: %+v %v", view, err)
	}
}

func TestNativeApprovalRechecksAfterPresence(t *testing.T) {
	for _, change := range []string{"withdraw", "expire", "effect", "recipient"} {
		t.Run(change, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			e, ctx, token := approvalEngine(t, func(ctx context.Context, _ string) (humanauth.Verdict, error) {
				close(entered)
				select {
				case <-release:
					return humanauth.Verified, nil
				case <-ctx.Done():
					return humanauth.Abandoned, ctx.Err()
				}
			}, nil)
			enableApproval(t, e, ctx, token)
			serial := approvalSend(t, e, ctx, token, core.MsgRequest, core.RoleCoordinator)
			answer := make(chan error, 1)
			go func() { answer <- e.AnswerNativeHuman(ctx, "approval-node", serial, "approve", "", "") }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("setup did not enter the sensor")
			}
			if change == "withdraw" {
				if _, err := e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: token, MsgSerial: serial, Disposition: "withdraw"}); err != nil {
					t.Fatal("withdraw setup:", err)
				}
			} else {
				// Fault injection on the writer models expiry or an altered
				// authority-bearing envelope while the sensor is outstanding.
				_, err := e.query(ctx, func() core.Result {
					m := e.state.Messages[serial]
					switch change {
					case "expire":
						m.Deadline = time.Now().Add(-time.Second)
					case "effect":
						m.Grant = core.RoleMember
					case "recipient":
						m.To = "approval-sender"
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			release <- struct{}{}
			select {
			case err := <-answer:
				if err == nil {
					t.Fatal("a stale presence result committed an approval")
				}
				if change == "effect" && !strings.Contains(err.Error(), "changed") {
					t.Fatalf("wrong effect-binding refusal: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("answer did not finish")
			}
			_, err := e.query(ctx, func() core.Result {
				if e.state.AgentByToken(token).IsCoordinator() || e.state.Messages[serial].State == core.MsgStateApproved {
					t.Error("refused approval still applied its effect")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOnlyHumanMayDisableDesktopApproval(t *testing.T) {
	e, ctx, token := approvalEngine(t, nil, nil)
	enableApproval(t, e, ctx, token)
	if _, err := e.GrantRole(ctx, "approval-sender", core.RoleAdmin); err != nil {
		t.Fatal("admin setup:", err)
	}
	if _, err := e.Configure(ctx, token, core.DesktopApprovalSettingKey, "false"); err == nil || !strings.Contains(err.Error(), "only the human") {
		t.Fatalf("admin agent disabled the human control: %v", err)
	}
	_, humanToken, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Configure(ctx, humanToken, core.DesktopApprovalSettingKey, "false"); err != nil {
		t.Fatal("human disable:", err)
	}
	settings, err := e.DesktopApprovalSettings(ctx)
	if err != nil || settings["value"] != "false" || settings["by"] == "" {
		t.Fatalf("human change was not recorded: %v %v", settings, err)
	}
}

func TestEnablingConfirmationBeforeNativeCommitCannotBeBypassed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	e := New(core.NewState("approval-node", core.DefaultLimits()), &memLedger{}, deadProber{})
	// Buffer only the writer's ingress to stage an exact ordering. All calls
	// still enter their production methods: plan while off, enable, commit.
	e.ops = make(chan request, 2)
	var checks atomic.Int32
	e.desktopApprovalPresence = func(context.Context, string) (humanauth.Verdict, error) {
		checks.Add(1)
		return humanauth.Verified, nil
	}
	e.SetHumanNotifier(unavailablePriorityNotifier{})
	go e.Run(ctx)
	if _, _, err := e.HumanAgent(ctx); err != nil {
		t.Fatal(err)
	}
	token := registerAgent(t, e, "approval-sender")
	serial := approvalSend(t, e, ctx, token, core.MsgRequest, core.RoleCoordinator)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	held := make(chan error, 1)
	go func() {
		_, err := e.query(ctx, func() core.Result { close(blocked); <-release; return nil })
		held <- err
	}()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("writer staging did not start")
	}
	waitQueued := func(want int) {
		t.Helper()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for len(e.ops) < want {
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatalf("only %d of %d writer calls entered", len(e.ops), want)
			}
		}
	}
	answer, enabled := make(chan error, 1), make(chan error, 1)
	go func() { answer <- e.AnswerNativeHuman(ctx, "approval-node", serial, "approve", "", "") }()
	waitQueued(1)
	go func() { _, err := e.Configure(ctx, token, core.DesktopApprovalSettingKey, "true"); enabled <- err }()
	waitQueued(2)
	unblock()
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err := <-enabled; err != nil {
		t.Fatal("enable setup:", err)
	}
	err := <-answer
	if err == nil || !strings.Contains(err.Error(), "enabled before") || checks.Load() != 0 {
		t.Fatalf("unconfirmed off-plan bypassed newly enabled policy: err=%v checks=%d", err, checks.Load())
	}
	view, err := e.NativeHumanMessage(ctx, "approval-node", serial)
	if err != nil || !view.Pending {
		t.Fatalf("racing policy change decided request: %+v %v", view, err)
	}
}
