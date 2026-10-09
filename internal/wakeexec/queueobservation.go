// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import "time"

// QueueObservation distinguishes command admission from a read-only queue
// observation. Neither is evidence of a started turn or consumed mail.
// IssuedAt dates a timestamped notice or this command's admission attempt.
// ReceiptAt dates only a retained fallback receipt, never the pending notice.
// Legacy pending notices have unknown age. At dates the observation, not now.
type QueueObservation struct {
	Delivery  string // native started/steered/settled/unknown; never queue acceptance
	Admission string
	Pending   string
	At        time.Time
	IssuedAt  time.Time
	ReceiptAt time.Time
	AgeSource string
}

type queueProbe struct {
	pending, known bool
	issuedAt       time.Time
}
