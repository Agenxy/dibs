// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

const (
	codexTurnTailBytes = 1 << 20
	codexTurnHeaderMax = 128 << 10
	codexTurnFileLimit = 4
	codexTurnDirLimit  = 256
	earlyTurnLimit     = 2 * time.Second
)

// CodexTurnReceipt refers to the exact turn in the app's acceptance reply.
// OfferedAt excludes turns already in progress when input was submitted.
type CodexTurnReceipt struct {
	ID        string
	OfferedAt time.Time
}

// CodexEarlyTurnFailures reads a bounded tail of the original app thread.
// Missing, truncated or unfamiliar evidence leaves delivery eligible. Never
// return transcript content: only classifications from exact start/end pairs.
func CodexEarlyTurnFailures(thread string, turns [2]CodexTurnReceipt) [2]core.ContactReason {
	var result [2]core.ContactReason
	if !core.LooksLikeThreadID(thread) || turns[0].ID == "" || turns[0].ID == turns[1].ID || turns[1].ID == "" {
		return result
	}
	for _, path := range codexTurnRollouts(codexHome(), thread) {
		classifyCodexTurnTail(codexTurnTail(path, thread), turns, &result)
		if result[0] != "" && result[1] != "" {
			break
		}
	}
	return result
}

func codexTurnRollouts(home, thread string) []string {
	if home == "" {
		return nil
	}
	root := filepath.Join(home, "sessions")
	dirs := []string{root}
	if at, ok := uuidv7Time(thread); ok {
		for _, offset := range []int{0, -1, 1} {
			dirs = append(dirs, filepath.Join(root, at.AddDate(0, 0, offset).Format("2006/01/02")))
		}
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, dir := range dirs {
		// #nosec G304 -- fixed Codex home and creation date from a verified UUID.
		f, err := os.Open(dir)
		if err != nil {
			continue
		}
		entries, _ := f.ReadDir(codexTurnDirLimit)
		_ = f.Close()
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !codexTurnRolloutName(entry.Name(), thread) {
				continue
			}
			if info, err := entry.Info(); err == nil {
				candidates = append(candidates, candidate{filepath.Join(dir, entry.Name()), info.ModTime()})
			}
		}
	}
	slices.SortFunc(candidates, func(a, b candidate) int { return b.mod.Compare(a.mod) })
	out := make([]string, 0, min(len(candidates), codexTurnFileLimit))
	for _, c := range candidates[:cap(out)] {
		out = append(out, c.path)
	}
	return out
}

func codexTurnRolloutName(name, thread string) bool {
	base, ok := strings.CutSuffix(name, ".jsonl")
	if !ok || !strings.HasPrefix(base, "rollout-") {
		return false
	}
	if strings.HasSuffix(base, "-"+thread) {
		return true
	}
	_, continuation, ok := strings.Cut(base, "-"+thread+"_")
	return ok && core.LooksLikeThreadID(continuation)
}

func codexTurnTail(path, thread string) []byte {
	// #nosec G304 -- generated Codex profile path with a validated rollout filename.
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil
	}
	var head struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.NewDecoder(io.NewSectionReader(f, 0, codexTurnHeaderMax)).Decode(&head) != nil ||
		head.Type != "session_meta" || head.Payload.ID != thread {
		return nil
	}
	offset := max(0, stat.Size()-codexTurnTailBytes)
	tail, err := io.ReadAll(io.NewSectionReader(f, offset, codexTurnTailBytes))
	if err != nil {
		return nil
	}
	if offset != 0 {
		_, tail, _ = bytes.Cut(tail, []byte{'\n'})
	}
	return tail
}

type codexTurnRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type       string `json:"type"`
		TurnID     string `json:"turn_id"`
		DurationMS int64  `json:"duration_ms"`
		Error      *struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"codex_error_info"`
		} `json:"error"`
	} `json:"payload"`
}

func classifyCodexTurnTail(tail []byte, turns [2]CodexTurnReceipt, result *[2]core.ContactReason) {
	active := -1
	var started time.Time
	for line := range bytes.SplitSeq(tail, []byte{'\n'}) {
		var row codexTurnRecord
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		p := row.Payload
		if row.Type == "response_item" && (p.Type == "function_call" || p.Type == "custom_tool_call") {
			active = -1 // cannot call it a failed pre-tool turn
		}
		if row.Type != "event_msg" {
			continue
		}
		switch p.Type {
		case "task_started":
			active = slices.IndexFunc(turns[:], func(turn CodexTurnReceipt) bool {
				return p.TurnID == turn.ID && !turn.OfferedAt.IsZero() && !row.Timestamp.Before(turn.OfferedAt)
			})
			started = row.Timestamp
		case "mcp_tool_call_begin":
			active = -1
		case "task_complete":
			if active >= 0 && row.failedQuickly(turns[active].ID, started) {
				result[active] = codexTurnFailureFor(row)
			}
			active = -1
		}
	}
}

func (row codexTurnRecord) failedQuickly(id string, started time.Time) bool {
	p := row.Payload
	return p.TurnID == id && !row.Timestamp.Before(started) && p.Error != nil &&
		p.DurationMS > 0 && p.DurationMS <= earlyTurnLimit.Milliseconds()
}

func codexTurnFailureFor(row codexTurnRecord) core.ContactReason {
	failure := row.Payload.Error
	if strings.HasPrefix(failure.Message, "Error running remote compact task:") {
		if string(failure.Code) == `"context_window_exceeded"` {
			return core.ContactContextFull
		}
		return core.ContactCompactionFailed
	}
	return core.ContactTurnsEnded
}
