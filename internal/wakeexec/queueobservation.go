// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import "time"

// QueueThreadExecSource denotes a one-shot Codex CLI run.
const QueueThreadExecSource = "exec"

// QueueUnavailableReason explains the verified observation that ended retries.
func QueueUnavailableReason(source string) string {
	if source == QueueThreadExecSource {
		return "The bound thread came from a one-shot codex exec run."
	}
	return "The app queue reported no rollout found for this binding."
}

// QueueObservation distinguishes command admission from a read-only queue
// observation. Neither is evidence of a started turn or consumed mail.
// IssuedAt dates a timestamped notice or this command's admission attempt.
// ReceiptAt dates only a retained fallback receipt, never the pending notice.
// Legacy pending notices have unknown age. At dates the observation, not now.
type QueueObservation struct {
	Delivery     string // native started/steered/settled/unknown; never queue acceptance
	Admission    string
	Pending      string
	ThreadState  string // gone when the bound thread cannot be queued
	ThreadSource string // metadata from a matching thread/read reply
	At           time.Time
	IssuedAt     time.Time
	ReceiptAt    time.Time
	AgeSource    string
	ProbeBinary  string `json:"-"` // local executable that admitted this item
}

type queueProbe struct {
	pending, known bool
	issuedAt       time.Time
	itemID         string
	receipt        string // exact observation result, never a delivery inference
	threadSource   string
}
