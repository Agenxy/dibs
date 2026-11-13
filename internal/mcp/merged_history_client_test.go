// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// This fixture is a history client: a successful bounded page need not be a
// complete read. Keep the real five-second retry bound, not a larger server
// budget. Small pages exercise continuation on every use of the assertion.
func readMergedHistory(f *supportedMergeFixture, required []string) error {
	until := time.Now().Add(5 * time.Second)
	var last map[string]any
	for time.Now().Before(until) {
		var rows []any
		partial := false
		args := map[string]any{"token": f.into["token"], "include_bodies": true, "limit": 1}
		seen := map[string]bool{}
		for {
			if time.Now().After(until) {
				return fmt.Errorf("merged history did not complete within retry bound: %v", last)
			}
			last, _ = historyExchangeBefore(f.t, f.srv, f.version, args, until)
			behind, ok := last["behind_by"].(float64)
			if !ok {
				return fmt.Errorf("merged history lacks a completeness watermark: %v", last)
			}
			partial = partial || behind > 0
			units, ok := last["units"].([]any)
			if !ok {
				return fmt.Errorf("merged history lacks units: %v", last)
			}
			for _, unit := range units {
				row, ok := unit.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid history row: %v", unit)
				}
				if content, ok := row["content"].(map[string]any); ok {
					switch reason, _ := content["unavailable"].(string); reason {
					case "":
					case "native content work bound; metadata retained", "work budget exhausted; retry this conversation from its preceding serial":
						partial = true
					default:
						return fmt.Errorf("unexpected unavailable history content: %v", row)
					}
				}
				rows = append(rows, unit)
			}
			cursor, _ := last["cursor"].(string)
			if cursor == "" {
				break
			}
			if seen[cursor] {
				if behind > 0 {
					break // the documented unchanged cursor while the prefix catches up
				}
				return fmt.Errorf("history cursor did not advance: %v", last)
			}
			seen[cursor] = true
			args = map[string]any{"token": f.into["token"], "cursor": cursor, "include_bodies": true, "limit": 1}
		}
		if !partial {
			full := map[string]any{"units": rows}
			for _, text := range required {
				if !mentions(full, text) {
					return fmt.Errorf("complete history lost inherited mail %q: %v", text, full)
				}
			}
			return nil
		}
		// A new read retries budget-unavailable units as well as catching up
		// the prefix. Following only the old cursor would skip their bodies.
		<-time.NewTimer(10 * time.Millisecond).C
	}
	return fmt.Errorf("merged history stayed partial within retry bound: %v", last)
}

type delayedMergeHistory struct {
	*ledger.Ledger
	once     sync.Once
	mu       sync.Mutex
	first    uint64
	reads    map[uint64]int
	timedOut bool
}

func (l *delayedMergeHistory) ReadHistoryOp(ctx context.Context, serial uint64, seek mailhistory.SeekRange) (*core.Op, error) {
	l.mu.Lock()
	l.reads[serial]++
	l.mu.Unlock()
	delayed := false
	l.once.Do(func() {
		delayed = true
		<-ctx.Done()
		l.mu.Lock()
		l.first, l.timedOut = serial, ctx.Err() == context.DeadlineExceeded
		l.mu.Unlock()
	})
	if delayed {
		return nil, ctx.Err()
	}
	return l.Ledger.ReadHistoryOp(ctx, serial, seek)
}

func TestMergedHistoryClientFollowsPagesAndRetriesBudgetUnavailableBody(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			var delayed *delayedMergeHistory
			f := newSupportedMergeWithLedger(t, version, func(l *ledger.Ledger) engine.Ledger {
				delayed = &delayedMergeHistory{Ledger: l, reads: map[uint64]int{}}
				return delayed
			})
			a := f.args()
			preview := f.call("merge_agents", a)
			a["dry_run"], a["fence"] = false, preview["fence"]
			f.call("merge_agents", a)
			assertMergedHistory(f)
			delayed.mu.Lock()
			defer delayed.mu.Unlock()
			if !delayed.timedOut || delayed.first == 0 || delayed.reads[delayed.first] < 2 {
				t.Fatalf("setup/retry: first content must hit its real deadline and be read again: first=%d timeout=%t reads=%v", delayed.first, delayed.timedOut, delayed.reads)
			}
		})
	}
}

func TestMergedHistoryClientRejectsCompleteMissingMail(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := newSupportedMerge(t, version)
			a := f.args()
			preview := f.call("merge_agents", a)
			a["dry_run"], a["fence"] = false, preview["fence"]
			f.call("merge_agents", a)
			if err := readMergedHistory(f, []string{"private original mail", "source outgoing question"}); err != nil {
				t.Fatal("setup: complete inherited history is unreadable:", err)
			}
			err := readMergedHistory(f, []string{"NEVER-SENT-INHERITANCE-CONTROL"})
			if err == nil || !strings.HasPrefix(err.Error(), "complete history lost inherited mail \"NEVER-SENT-INHERITANCE-CONTROL\"") {
				t.Fatalf("complete missing mail must fail for that reason, got %v", err)
			}
		})
	}
}
