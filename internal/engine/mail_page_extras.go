// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Mailbox aliases are bounded together with their auxiliary projections.
// Otherwise a page of eight envelopes could still duplicate hundreds of owed
// requests or entire announcement bodies beside that page.
func (e *Engine) compactMailboxExtras(res core.Result, p mailPage) {
	selected := map[uint64]bool{}
	for _, m := range p.mail {
		selected[m.Serial] = true
	}
	for _, key := range []string{"owed_work", "task_queue"} {
		rows, _ := res[key].([]core.Result)
		shown := mailboxProjection(rows, selected)
		res[key], res["more_"+key] = shown, len(rows)-len(shown)
	}
	if owes, ok := res["owes"].([]uint64); ok {
		shown := []uint64{}
		for _, id := range owes {
			if selected[id] {
				shown = append(shown, id)
			}
		}
		res["owes"] = shown
	}
	compactMailboxAnnouncements(res)
	res["projection_hint"] = "owed_work, owes and task_queue describe this mailbox page; " +
		"follow next_cursor for more; read_mail has full envelopes"
}

func mailboxProjection(rows []core.Result, selected map[uint64]bool) []core.Result {
	shown := []core.Result{}
	for _, row := range rows {
		id, _ := row["msg_serial"].(uint64)
		if selected[id] {
			shown = append(shown, row)
		}
	}
	return shown
}

func compactMailboxAnnouncements(res core.Result) {
	announcements, _ := res["announcements"].([]core.Result)
	shown := []core.Result{}
	used := 0
	for _, row := range announcements {
		body, _ := row["body"].(string)
		summary := core.Result{}
		for k, v := range row {
			summary[k] = v
		}
		summary["body"] = trimRunes(strings.Join(strings.Fields(body), " "), 80)
		summary["read_hint"] = fmt.Sprintf("read_space(space:%q) has the full announcement", row["agent"])
		if summary["body"] != body {
			summary["body_truncated"] = true
		}
		raw, _ := json.Marshal(summary)
		if len(shown) == mailPageLimit || used+len(raw) > 2048 {
			break
		}
		shown = append(shown, summary)
		used += len(raw)
	}
	res["announcements"], res["more_announcements"] = shown, len(announcements)-len(shown)
	if len(shown) < len(announcements) {
		res["announcement_hint"] = "board(detail:true) lists spaces; read_space(space,limit) returns " +
			"their full announcement history; acknowledge only after reading"
	}
}
