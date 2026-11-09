// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import "slices"

// mailIndex is Messages grouped by addressee and by sender, in serial order.
//
// A DERIVED VIEW, and the cheapest way to be one. Nearly every question the
// board asks about mail is per agent ("what is in this inbox", "what did this
// agent ask for"), and every one of them used to range over every message on
// the board. The daemon asks them per agent per second (the socket receipt
// prune alone walked every non-retired row), so a board of a hundred rows and
// a thousand retained messages spent its idle time doing a hundred thousand
// comparisons a second to learn that nothing had changed. Measured on a real
// board: that loop was the whole of the daemon's idle CPU.
//
// Unexported, so gob never sees it and the snapshot format does not change.
// Built lazily by the first read after an op and DISCARDED by every op
// (finish drops it), and by the handful of sites in this package that add,
// delete or readdress a message, so a read later in the SAME op sees the
// change. A state therefore never carries an index across an op: a live
// board and its replay compare equal, as the replay tests require, and the
// index lives exactly where it pays, in the idle stretch between ops where
// the daemon reads mail per row per second. The serial and the count are
// checked as well, as a backstop for a mutation site added later and not
// marked: such a site can be stale within its own op, never across two.
// checkMailIndex holds every trusted read to a rebuild in the core tests.
type mailIndex struct {
	serial   uint64
	n        int
	to, from map[string][]*Message
}

// mailChanged marks Messages membership or addressing as changed. Call it
// wherever a message is added or deleted or its To or From is rewritten.
func (s *State) mailChanged() { s.mailIdx = nil }

// checkMailIndex is set by this package's tests only: it is handed every
// cached index the moment it is trusted, and fails the test if a rebuild
// would differ. So every fold in the core suite checks the invalidation rule,
// not one test written for it.
var checkMailIndex func(s *State, cached *mailIndex)

func (s *State) mail() *mailIndex {
	if x := s.mailIdx; x != nil && x.serial == s.Serial && x.n == len(s.Messages) {
		if checkMailIndex != nil {
			checkMailIndex(s, x)
		}
		return x
	}
	x := s.buildMailIndex()
	s.mailIdx = x
	return x
}

func (s *State) buildMailIndex() *mailIndex {
	x := &mailIndex{
		serial: s.Serial, n: len(s.Messages),
		to: map[string][]*Message{}, from: map[string][]*Message{},
	}
	for _, m := range s.Messages {
		x.to[m.To] = append(x.to[m.To], m)
		x.from[m.From] = append(x.from[m.From], m)
	}
	bySerial := func(a, b *Message) int {
		switch {
		case a.Serial < b.Serial:
			return -1
		case a.Serial > b.Serial:
			return 1
		}
		return 0
	}
	for _, ms := range x.to {
		slices.SortFunc(ms, bySerial)
	}
	for _, ms := range x.from {
		slices.SortFunc(ms, bySerial)
	}
	return x
}

// MailTo is every retained message addressed to agent, oldest first, whatever
// its state. The slice is shared: callers must not modify it.
func (s *State) MailTo(agent string) []*Message { return s.mail().to[agent] }

// MailFrom is every retained message agent sent, oldest first. Shared, as
// MailTo.
func (s *State) MailFrom(agent string) []*Message { return s.mail().from[agent] }
