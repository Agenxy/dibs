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
	// An unnamed error is evidence of an immediate abort only within two
	// seconds; a longer turn may have done useful work without a tool call.
	// Explicit compact/context errors identify the failure without this cap.
	genericTurnFailureLimit = 2 * time.Second
)

// CodexTurnReceipt refers to the exact turn in the app's acceptance reply.
// OfferedAt excludes turns already in progress when input was submitted.
type CodexTurnReceipt struct {
	ID        string
	OfferedAt time.Time
}

// CodexTurnObservation reports the proved outcome of an exact receipt.
type CodexTurnObservation struct {
	Failure core.ContactReason
	Normal  bool
}

type codexTurnEvidence struct {
	started time.Time
	outcome CodexTurnObservation
}

// ObserveCodexTurns reads a bounded tail of the original app thread and
// returns classifications for up to two receipts. It returns no transcript
// content. A start must match the receipt and occur after the input offer.
func ObserveCodexTurns(thread string, turns [2]CodexTurnReceipt) [2]CodexTurnObservation {
	var result [2]CodexTurnObservation
	if !core.LooksLikeThreadID(thread) || turns[0].ID == "" || turns[0].ID == turns[1].ID {
		return result
	}
	var evidence [2]codexTurnEvidence
	for _, path := range codexTurnRollouts(codexHome(), thread) {
		classifyCodexTurnTail(codexTurnTail(path, thread), turns, &evidence)
		if evidence[0].outcome != (CodexTurnObservation{}) &&
			(turns[1].ID == "" || evidence[1].outcome != (CodexTurnObservation{})) {
			break
		}
	}
	for i, seen := range evidence {
		// Elapsed time alone is not progress: remote compaction can still
		// fail after tens of seconds. An unfinished tool-free turn is unknown.
		result[i] = seen.outcome
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

func classifyCodexTurnTail(tail []byte, turns [2]CodexTurnReceipt, result *[2]codexTurnEvidence) {
	active := -1
	for line := range bytes.SplitSeq(tail, []byte{'\n'}) {
		var row codexTurnRecord
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		if active >= 0 && row.toolActivityFor(turns[active].ID) {
			result[active].outcome.Normal = true
			active = -1
		}
		if row.Type != "event_msg" {
			continue
		}
		switch row.Payload.Type {
		case "task_started":
			clearCodexActiveStart(active, result)
			active = matchCodexTurnStart(row, turns, result)
		case "task_complete":
			if active >= 0 && row.Payload.TurnID == turns[active].ID {
				result[active].outcome = row.completion(result[active].started)
				clearCodexActiveStart(active, result)
				active = -1
			}
		}
	}
}

func clearCodexActiveStart(active int, result *[2]codexTurnEvidence) {
	if active >= 0 {
		result[active].started = time.Time{}
	}
}

func (row codexTurnRecord) toolActivityFor(id string) bool {
	if row.Payload.TurnID != "" && row.Payload.TurnID != id {
		return false
	}
	return row.Type == "event_msg" && row.Payload.Type == "mcp_tool_call_begin" ||
		row.Type == "response_item" && (row.Payload.Type == "function_call" || row.Payload.Type == "custom_tool_call")
}

func matchCodexTurnStart(row codexTurnRecord, turns [2]CodexTurnReceipt, result *[2]codexTurnEvidence) int {
	for i, turn := range turns {
		if turn.ID != "" && row.Payload.TurnID == turn.ID && !turn.OfferedAt.IsZero() &&
			!row.Timestamp.Before(turn.OfferedAt) && result[i].outcome == (CodexTurnObservation{}) {
			result[i].started = row.Timestamp
			return i
		}
	}
	return -1
}

func (row codexTurnRecord) completion(started time.Time) CodexTurnObservation {
	if row.Timestamp.Before(started) {
		return CodexTurnObservation{}
	}
	p := row.Payload
	if p.Error == nil {
		return CodexTurnObservation{Normal: true}
	}
	reason := codexTurnFailureFor(row)
	if reason != core.ContactTurnsEnded || p.DurationMS > 0 && p.DurationMS <= genericTurnFailureLimit.Milliseconds() {
		return CodexTurnObservation{Failure: reason}
	}
	return CodexTurnObservation{Normal: p.DurationMS > genericTurnFailureLimit.Milliseconds()}
}

func codexTurnFailureFor(row codexTurnRecord) core.ContactReason {
	failure := row.Payload.Error
	if string(failure.Code) == `"context_window_exceeded"` {
		return core.ContactContextFull
	}
	if strings.HasPrefix(failure.Message, "Error running remote compact task:") {
		return core.ContactCompactionFailed
	}
	return core.ContactTurnsEnded
}
