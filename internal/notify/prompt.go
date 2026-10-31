// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// PromptReply separates a deliberate verdict from participant-written text.
// Only the helper's private pipe can return it. No text is an approval token.
type PromptReply struct{ Action, Choice, Text string }

// ErrReplyUnavailable is a pre-post capability miss, not a failed or ambiguous
// post. Callers may use legacy actions without risking a duplicate prompt.
var ErrReplyUnavailable = errors.New("the native helper has no typed-reply capability")

// LegacyReplyHint is receipt/doctor advice, never a grant or visibility claim.
const LegacyReplyHint = "typed reply unavailable: restart the Dibs service to refresh its bundled notifier, " +
	"then run dibs doctor"

// NativePromptAvailable reports the installed native route. A legacy helper
// must also advertise reply capability on its anonymous pipe before posting.
func NativePromptAvailable() bool { return goos == "darwin" && helper() != "" && !silenced() }

// ReplyMessage posts a full-text native ask and returns only an explicit human
// reply, preserving posting receipts separately from its verdict and body.
func ReplyMessage(
	node string, serial uint64, title string, detail AskDetail, choices []string, receipt DeliveryReceipt,
) (PromptReply, error) {
	if silenced() {
		return PromptReply{}, notificationSilencedError()
	}
	if !NativePromptAvailable() {
		return PromptReply{}, fmt.Errorf("%w: install the Dibs native app and run dibs doctor; "+
			"this build cannot collect a desktop reply", ErrCannotNotify)
	}
	if detail.Kind != core.MsgQuestion && detail.Kind != core.MsgRequest {
		return PromptReply{}, errors.New("native reply needs a question or request")
	}
	if len(choices) > core.MaxChoices || (detail.Kind == core.MsgRequest && len(choices) != 0) {
		return PromptReply{}, errors.New("invalid native reply choices")
	}
	id := ""
	if node != "" {
		var err error
		id, err = MessageID(node, serial)
		if err != nil {
			return PromptReply{}, err
		}
	}
	excerpt := utf8Prefix(strings.Join(strings.Fields(detail.Body), " "), 180)
	post := makeBrokerPost(id, title, "", excerpt, &detail, []string{"Later", "Reply…"}, true)
	if detail.Kind == core.MsgRequest {
		post.Buttons = []string{"Deny", "Later", "Approve"}
	}
	post.ReplyKind, post.Choices = detail.Kind, choices
	post.Profile = detail.Profile
	// Full text is data on a private pipe, never argv, and is not passed
	// through the legacy 8 KiB excerpt encoder. Refuse rather than truncate.
	var err error
	post.Detail, err = json.Marshal(struct {
		Sender string `json:"sender"`
		Body   string `json:"body"`
		Kind   string `json:"kind"`
	}{detail.Sender, detail.Body, detail.Kind})
	if err != nil {
		return PromptReply{}, err
	}
	ctx, cancel := askContext(&detail)
	defer cancel()
	untrack := trackNativeAsk(id, cancel)
	defer untrack()
	wrapped := receipt
	if receipt != nil {
		wrapped = func(r ReceiptData) { available := true; r.ReplyField = &available; receipt(r) }
	}
	event, err := brokerExchange(ctx, helper(), post, wrapped)
	if err != nil {
		return PromptReply{}, err
	}
	return promptReplyOf(event), nil
}

func promptReplyOf(event brokerEvent) PromptReply {
	// Explicit native banner buttons retain their existing semantics.
	if event.Event == "action" {
		switch event.Action {
		case "Approve":
			return PromptReply{Action: "approve"}
		case "Deny":
			return PromptReply{Action: "deny"}
		}
		return PromptReply{}
	}
	return PromptReply{Action: event.Action, Choice: event.Choice, Text: event.Text}
}

func validatePromptReply(post brokerPost, event brokerEvent) error {
	bytes := len(event.Text) + len(event.Choice)
	if event.Text != "" && event.Choice != "" {
		bytes += 2
	}
	valid := bytes <= core.DefaultLimits().MaxBodyBytes
	if post.ReplyKind == core.MsgQuestion {
		valid = valid && event.Action == "answer" && (strings.TrimSpace(event.Text) != "" || event.Choice != "") &&
			(event.Choice == "" || slices.Contains(post.Choices, event.Choice))
	} else {
		valid = valid && event.Choice == "" && (event.Action == "approve" || event.Action == "deny" ||
			(event.Action == "comment" && strings.TrimSpace(event.Text) != ""))
	}
	if !valid {
		return errors.New("invalid native reply; no verdict was recorded; read the Dibs board")
	}
	return nil
}

// ValidateNativeReply applies the same typed boundary to a reconnected native
// peer as to a reply on the daemon-owned anonymous broker pipe.
func ValidateNativeReply(kind string, choices []string, reply PromptReply) error {
	if kind != core.MsgQuestion && kind != core.MsgRequest {
		return errors.New("message does not ask for a native reply")
	}
	return validatePromptReply(brokerPost{ReplyKind: kind, Choices: choices},
		brokerEvent{Action: reply.Action, Choice: reply.Choice, Text: reply.Text})
}
