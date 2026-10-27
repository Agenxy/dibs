// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// These bounds are part of MailTrafficV1's recorded fold. A later change
// needs a new recorded semantic version; never reinterpret old V1 ops.
const (
	TrafficRetentionDays = 35
	TrafficAgentsPerDay  = 512
)

// TrafficCounts holds accepted-send totals by type, with zero types omitted.
type TrafficCounts struct {
	Notify   uint64 `json:"notify,omitempty"`
	Question uint64 `json:"question,omitempty"`
	Request  uint64 `json:"request,omitempty"`
	Handoff  uint64 `json:"handoff,omitempty"`
}

func (c *TrafficCounts) add(kind string) {
	switch kind {
	case MsgNotify:
		c.Notify++
	case MsgQuestion:
		c.Question++
	case MsgRequest:
		c.Request++
	case MsgHandoff:
		c.Handoff++
	}
}

func (c TrafficCounts) empty() bool { return c == (TrafficCounts{}) }

// TrafficAgentCounts is one incarnation's retained daily contribution.
type TrafficAgentCounts struct {
	CreatedSerial uint64        `json:"created_serial"`
	Sent          TrafficCounts `json:"sent"`
	AsksReceived  TrafficCounts `json:"asks_received"`
}

// TrafficBucket is a bounded daily fold, including unattributed overflow.
type TrafficBucket struct {
	Sent                 TrafficCounts                  `json:"sent"`
	AsksReceived         TrafficCounts                  `json:"asks_received"`
	OverflowSent         TrafficCounts                  `json:"overflow_sent"`
	OverflowAsksReceived TrafficCounts                  `json:"overflow_asks_received"`
	Agents               map[string]*TrafficAgentCounts `json:"agents"`
}

// TrafficHistory retains counts, never mail, names, subjects or credentials.
// FirstSerial marks partial first-day coverage; old ops are not backfilled.
type TrafficHistory struct {
	StartedAt   time.Time                 `json:"started_at"`
	FirstSerial uint64                    `json:"first_serial"`
	Through     time.Time                 `json:"through"`
	Days        map[string]*TrafficBucket `json:"days"`
}

func (b *TrafficBucket) agent(id string, created uint64) *TrafficAgentCounts {
	key := id + "\x00" + strconv.FormatUint(created, 10)
	if row := b.Agents[key]; row != nil {
		return row
	}
	if len(b.Agents) >= TrafficAgentsPerDay {
		return nil
	}
	row := &TrafficAgentCounts{CreatedSerial: created}
	b.Agents[key] = row
	return row
}

func (s *State) recordTrafficV1(from, to, kind string, now time.Time) {
	now = now.UTC()
	if s.Traffic == nil {
		s.Traffic = &TrafficHistory{StartedAt: now, FirstSerial: s.Serial, Through: now, Days: map[string]*TrafficBucket{}}
	}
	h := s.Traffic
	if now.After(h.Through) {
		h.Through = now
	}
	cutoff := utcDay(h.Through).AddDate(0, 0, -TrafficRetentionDays+1).Format(time.DateOnly)
	for day := range h.Days {
		if day < cutoff {
			delete(h.Days, day)
		}
	}
	day := now.Format(time.DateOnly)
	if day < cutoff {
		return
	} // a clock rollback cannot grow the retained window
	b := h.Days[day]
	if b == nil {
		b = &TrafficBucket{Agents: map[string]*TrafficAgentCounts{}}
		h.Days[day] = b
	}
	b.Sent.add(kind)
	if row := b.agent(from, s.Agents[from].CreatedSerial); row != nil {
		row.Sent.add(kind)
	} else {
		b.OverflowSent.add(kind)
	}
	if kind == MsgNotify {
		return
	}
	b.AsksReceived.add(kind)
	if row := b.agent(to, s.Agents[to].CreatedSerial); row != nil {
		row.AsksReceived.add(kind)
	} else {
		b.OverflowAsksReceived.add(kind)
	}
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// TrafficAgentRow presents an immutable ID and one incarnation's counters.
type TrafficAgentRow struct {
	Agent string `json:"agent"`
	TrafficAgentCounts
}

// TrafficDay separates exact board totals from optionally authorized rows.
type TrafficDay struct {
	Day                  string            `json:"day"`
	Sent                 TrafficCounts     `json:"sent"`
	AsksReceived         TrafficCounts     `json:"asks_received"`
	OverflowSent         TrafficCounts     `json:"overflow_sent"`
	OverflowAsksReceived TrafficCounts     `json:"overflow_asks_received"`
	RowsComplete         bool              `json:"rows_complete"`
	Agents               []TrafficAgentRow `json:"agents,omitempty"`
}

// TrafficView states retained UTC coverage and its cardinality limits.
type TrafficView struct {
	StartedAt     time.Time    `json:"started_at,omitzero"`
	FirstSerial   uint64       `json:"first_serial,omitempty"`
	Through       time.Time    `json:"through,omitzero"`
	RetentionDays int          `json:"retention_days"`
	AgentsPerDay  int          `json:"agents_per_day"`
	Days          []TrafficDay `json:"days"`
}

// MailTraffic is a pure detached projection. Aggregate callers get no agent
// IDs. The engine owns authorization before requesting per-agent rows.
// Quiet boards expire in the read view without a timer or a state mutation.
func (s *State) MailTraffic(now time.Time, agent string, rows bool) TrafficView {
	return s.mailTraffic(now, agent, nil, rows)
}

// MailTrafficSelf fences the current mailbox incarnation, including a legacy
// zero serial; a reused ID never grants its old holder's metadata to a member.
func (s *State) MailTrafficSelf(now time.Time, agent string, created uint64) TrafficView {
	return s.mailTraffic(now, agent, &created, true)
}

func (s *State) mailTraffic(now time.Time, agent string, created *uint64, rows bool) TrafficView {
	out := TrafficView{RetentionDays: TrafficRetentionDays, AgentsPerDay: TrafficAgentsPerDay, Days: []TrafficDay{}}
	h := s.Traffic
	if h == nil {
		return out
	}
	out.StartedAt, out.FirstSerial, out.Through = h.StartedAt, h.FirstSerial, h.Through
	if now.Before(h.Through) {
		now = h.Through
	}
	cutoff := utcDay(now).AddDate(0, 0, -TrafficRetentionDays+1).Format(time.DateOnly)
	dates := make([]string, 0, len(h.Days))
	for day := range h.Days {
		if day >= cutoff {
			dates = append(dates, day)
		}
	}
	sort.Strings(dates)
	for _, day := range dates {
		b := h.Days[day]
		d := TrafficDay{
			Day: day, Sent: b.Sent, AsksReceived: b.AsksReceived,
			OverflowSent: b.OverflowSent, OverflowAsksReceived: b.OverflowAsksReceived,
			RowsComplete: b.OverflowSent.empty() && b.OverflowAsksReceived.empty(),
		}
		if rows {
			d.Agents = trafficRows(b, agent, created)
		}
		out.Days = append(out.Days, d)
	}
	return out
}

func trafficRows(b *TrafficBucket, only string, created *uint64) []TrafficAgentRow {
	capacity := len(b.Agents)
	if only != "" {
		capacity = 1
	}
	out := make([]TrafficAgentRow, 0, capacity)
	for key, row := range b.Agents {
		id, _, _ := strings.Cut(key, "\x00")
		if only != "" && id != only {
			continue
		}
		if created != nil && row.CreatedSerial != *created {
			continue
		}
		out = append(out, TrafficAgentRow{Agent: id, TrafficAgentCounts: *row})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].CreatedSerial < out[j].CreatedSerial
	})
	return out
}
