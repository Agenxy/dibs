// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory

// HoldHistoryPublicationForTest holds the consumer's existing publish lock.
// It does not set readiness or authority. External tests still drive Open,
// Replay, first HTTP Accept and real engine operations before querying.
func HoldHistoryPublicationForTest(index *Index) func() {
	index.viewMu.Lock()
	return index.viewMu.Unlock
}
