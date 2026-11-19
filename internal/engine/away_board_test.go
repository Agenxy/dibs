// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The wait names the unavailable background route, never the person's presence.
func TestBoardNamesTheEndedClaudeThreadInsteadOfAnAwayWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newReopenContactFixture(t)
		f.do(t, &core.Op{Kind: core.OpSendMessage, Token: f.sender, To: "lead", MsgType: core.MsgRequest, Body: "queued"})
		synctest.Sleep(time.Second)
		synctest.Wait()
		board, err := f.e.Board(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range board["agents"].([]map[string]any) {
			if row["id"] != "lead" {
				continue
			}
			found = row["app_wait_reason"] == "waiting for its thread to be opened"
			if status, _ := row["wake"].(string); strings.Contains(status, "away") {
				t.Fatalf("board invented an operator-presence wait: %q", status)
			}
		}
		if !found {
			t.Fatal("board omitted the thread wait")
		}
		select {
		case argv := <-f.opened:
			t.Fatalf("ended session received app contact: %q", argv)
		default:
		}
	})
}
