// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "testing"

// A map-backed mailbox does not promise iteration order. Exercise the swap,
// rather than only passing sorted input through the inbox's ordering rule.
func TestSortMessagesOrdersAnOutOfOrderMailbox(t *testing.T) {
	newest := &Message{Serial: 9}
	oldest := &Message{Serial: 2}
	middle := &Message{Serial: 7}
	mail := []*Message{newest, oldest, middle}
	sortMessages(mail)
	if mail[0] != oldest || mail[1] != middle || mail[2] != newest {
		t.Fatalf("mailbox order is %d, %d, %d; want 2, 7, 9", mail[0].Serial, mail[1].Serial, mail[2].Serial)
	}
}
