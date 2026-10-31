// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanauth"
)

type desktopPresenceChecker func(context.Context, string) (humanauth.Verdict, error)

type desktopApprovalPlan struct {
	fingerprint [32]byte
	required    bool
	verified    bool
	reason      string
}

// Serialize only authority-bearing fields, including the board and both
// participants. Copying this digest off the writer cannot retain mutable state.
func desktopEffectFingerprint(node string, serial uint64, m *core.Message) ([32]byte, error) {
	raw, err := json.Marshal(struct {
		Node     string
		Serial   uint64
		From, To string
		Type     string
		Grant    string
		Adopt    string
		Merge    *core.MergeRequest
		Recovery *core.RecoveryRequest
	}{node, serial, m.From, m.To, m.Type, m.Grant, m.Adopt, m.Merge, m.Recovery})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func (e *Engine) planDesktopApproval(
	ctx context.Context, node string, serial uint64, disposition, body, commentID string,
) (desktopApprovalPlan, error) {
	var plan desktopApprovalPlan
	_, err := e.query(ctx, func() core.Result {
		view, fault := e.nativeHumanViewLocked(node, serial, time.Now())
		if fault != nil {
			return core.Result{"error": fault}
		}
		if !view.Pending {
			return core.Result{"error": nativeHumanError("E_ALREADY_ANSWERED", "message is "+view.State)}
		}
		if _, fault := nativeHumanResponseOp(view.Notice, "", serial, disposition, body, commentID); fault != nil {
			return core.Result{"error": fault}
		}
		m := e.state.Messages[serial]
		hash, fault := desktopEffectFingerprint(node, serial, m)
		if fault != nil {
			return core.Result{"error": fault}
		}
		plan.fingerprint = hash
		plan.required = e.state.DesktopApproval.Enabled && disposition == "approve" && m.HasApprovalEffect()
		plan.reason = fmt.Sprintf("Dibs: approve request #%d on board %q: %s.", serial, node, desktopEffectLabel(m))
		return nil
	})
	return plan, err
}

func desktopEffectLabel(m *core.Message) string {
	switch {
	case m.Recovery != nil:
		return fmt.Sprintf("recover %q into %q", m.Recovery.From, m.Recovery.Into)
	case m.Merge != nil:
		return fmt.Sprintf("merge %q into %q", m.Merge.From, m.Merge.Into)
	case m.Adopt != "":
		return fmt.Sprintf("give mailbox %q to %q", m.Adopt, m.From)
	default:
		return fmt.Sprintf("grant %q to %q", m.Grant, m.From)
	}
}

func (e *Engine) confirmDesktopApproval(ctx context.Context, plan *desktopApprovalPlan) error {
	if !plan.required {
		return nil
	}
	check := e.desktopApprovalPresence
	if check == nil {
		check = humanauth.Check
	}
	verdict, err := check(ctx, plan.reason)
	if err != nil || verdict != humanauth.Verified {
		slog.Info("privileged desktop approval remains pending after human confirmation", "verdict", verdict, "error", err)
		return &core.Error{Code: "E_HUMAN_PRESENCE", Msg: "human confirmation was canceled or unavailable; the request remains pending",
			Hint: "open dibs web to review and decide the request; only the human can turn off confirmation in Settings"}
	}
	plan.verified = true
	return nil
}

// On the writer, immediately before exec. A setting enabled in the meantime
// cannot be bypassed by a plan made while it was off, and a proof for another
// node, serial, recipient or typed effect is never transferable.
func (e *Engine) recheckDesktopApprovalLocked(
	plan desktopApprovalPlan, node string, serial uint64, disposition string,
) error {
	m := e.state.Messages[serial]
	if m == nil {
		return nativeHumanError("E_ALREADY_ANSWERED", "message is gone")
	}
	hash, err := desktopEffectFingerprint(node, serial, m)
	if err != nil {
		return err
	}
	if hash != plan.fingerprint {
		return nativeHumanError("E_APPROVAL_CHANGED", "the approval effect changed during confirmation")
	}
	if e.state.DesktopApproval.Enabled && disposition == "approve" && m.HasApprovalEffect() && !plan.verified {
		return nativeHumanError("E_HUMAN_PRESENCE",
			"confirmation was enabled before this approval committed; request remains pending")
	}
	return nil
}
