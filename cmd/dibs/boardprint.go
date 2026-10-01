package main

import (
	"fmt"
	"strings"

	"github.com/agenxy/dibs/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// printAgents lists who is on the board and whether they are working.
func printAgents(agents []boardAgent) {
	if len(agents) == 0 {
		return
	}
	fmt.Println(ui.Section("agents"))
	// Columns sized to what is actually there, not to a guess. A fixed width
	// wide enough for "stale (process gone)" leaves a gap the width of that
	// phrase on every healthy row, which is most of them.
	nameW, statusW := 0, 0
	for _, l := range agents {
		if w := lipgloss.Width(agentLabel(l)); w > nameW {
			nameW = w
		}
		if w := lipgloss.Width(agentStatus(l)); w > statusW {
			statusW = w
		}
	}
	for _, l := range agents {
		where := ""
		if l.Host != "" {
			where = " on " + l.Host
		}
		fmt.Printf("  %s  %s  %s\n",
			ui.Accent(ui.Pad(agentLabel(l), nameW)),
			ui.Pad(agentStatus(l), statusW),
			ui.Dim("seen "+ago(l.LastSeen)+where))
		if l.Description != "" {
			fmt.Println("    " + ui.Dim(l.Description))
		}
		for _, sl := range l.Slots {
			fmt.Println(slotLine(sl))
		}
	}
}

// agentLabel is what a human should read to know who this is.
//
// Usually the id. But an id is an ADDRESS and must be ASCII, so an agent named
// in a non-Latin script gets `agent`: and a fleet of them reads `agent`,
// `agent-2`, `agent-3`: correct addresses that identify nobody. Where the name
// could not become the id, show both.
func agentLabel(l boardAgent) string {
	if l.DisplayName == "" {
		return l.ID
	}
	return l.DisplayName + " (" + l.ID + ")"
}

// agentStatus weights liveness the same way the browser board does: working is
// good, a dead process is worth noticing, anything else is context.
func agentStatus(l boardAgent) string {
	status := l.Status + staleNote(l.StaleReason)
	if l.Status == "stale" && l.ProcAlive {
		status += " (hung?)"
	}
	// WHAT IT IS DOING LEADS. Status is about a process, and for an agent
	// whose harness runs no process between calls it says the opposite of
	// the truth; the work state does not. Reported by k7-dev (Dibs #1152).
	switch l.Work {
	case "stalled":
		return ui.Alarm("STALLED") + ui.Dim(" · "+status)
	case "working", "waiting":
		return ui.Good(l.Work) + ui.Dim(" · "+status)
	case "declared":
		return ui.Dim("declared · " + status)
	}
	switch {
	case l.Status == "active":
		return ui.Good(status)
	case l.StaleReason == "process_exited":
		return ui.Attn(status)
	}
	return ui.Dim(status)
}

// slotLine is one declaration as `dibs board` prints it.
func slotLine(sl boardSlot) string {
	line := "    " + sl.Text
	if sl.Waiting != "" {
		line += "  " + ui.Dim("(waiting on "+sl.Waiting+")")
	}
	if len(sl.Dirs) > 0 {
		// ui.Path, because these are the same coordination paths the
		// claims rows carry and those rows already shorten them. Grepping
		// a board for a directory found the claim and silently missed the
		// slot on that same directory, or the reverse: one board, two
		// spellings of one path.
		short := make([]string, 0, len(sl.Dirs))
		for _, d := range sl.Dirs {
			short = append(short, ui.Path(d))
		}
		line += "  " + ui.Dim("["+strings.Join(short, " ")+"]")
	}
	return line
}

func staleNote(reason string) string {
	switch reason {
	case "process_exited":
		return " (process gone)"
	case "lease_lapsed":
		return " (no contact)"
	case "idle_no_activity":
		return " (idle, no pid)"
	}
	return ""
}
