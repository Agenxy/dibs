// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Which app a Codex thread was BORN in, from the thread's own transcript.
//
// The bridge's reading of its process tree is the best evidence of where an
// agent runs, and it arrives on the agent's next call. A dormant agent makes
// no call until something wakes it, and a wake is exactly what needs to know:
// so after an install every app agent that was asleep read as "unknown", and
// its wake queued a message into a thread the app had not loaded, which is the
// failure the in-app wake exists to fix. The answer was on disk all along.
//
// Codex writes a header as the first line of every thread's rollout file, and
// it names the client that created the thread. Measured on 2026-09-30: threads
// started in the ChatGPT app say `"originator":"Codex Desktop"`, threads
// started by `codex exec` say `"codex_exec"`. That is where the agent started,
// which is where the operator said a wake belongs, and nothing an agent says
// can change it.

// AppFor is the app a wake should open the thread in: the surface the bridge
// derived when it stated one, and otherwise, for a Codex thread, the app the
// thread was born in. A bridge that stated a surface outranks the transcript,
// because it says where the agent ran LAST: a thread born in the app and
// since run from a terminal states "codex", and is not pulled back into the
// app. "" when there is no app to open.
func AppFor(surface, harness, thread string) string {
	if surface != "" {
		return surface
	}
	if strings.Contains(strings.ToLower(harness), "codex") && CodexBornInApp(thread) {
		return ChatGPTApp
	}
	return ""
}

// codexAppOriginator is what the ChatGPT app writes as a thread's originator.
const codexAppOriginator = "Codex Desktop"

// CodexBornInApp reports whether the Codex thread was created by the ChatGPT
// app, reading its rollout file under the Codex home. false when the file
// cannot be found or read: an unproven app is never opened.
func CodexBornInApp(thread string) bool {
	if !isThreadID(thread) {
		return false
	}
	return codexOriginator(codexHome(), thread) == codexAppOriginator
}

func codexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// codexOriginator is the originator in the thread's rollout header, or "".
func codexOriginator(home, thread string) string {
	path := rolloutFile(home, thread)
	if path == "" {
		return ""
	}
	f, err := os.Open(path) //nolint:gosec // a path built from the Codex home and a validated thread id
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var head struct {
		Type    string `json:"type"`
		Payload struct {
			ID         string `json:"id"`
			Originator string `json:"originator"`
		} `json:"payload"`
	}
	// The first JSON value is the header. It carries the thread's base
	// instructions, so it is tens of kilobytes; Decode reads that one value.
	if json.NewDecoder(f).Decode(&head) != nil || head.Type != "session_meta" || head.Payload.ID != thread {
		return ""
	}
	return head.Payload.Originator
}

// rolloutFile finds the thread's rollout file. Codex files a thread under the
// local date it was created, and a Codex thread id is a UUIDv7, whose first 48
// bits are that creation time in milliseconds: so the directory is known, and
// the day either side covers a timezone the daemon and Codex disagree on.
func rolloutFile(home, thread string) string {
	if home == "" {
		return ""
	}
	suffix := "-" + thread + ".jsonl"
	var days []time.Time
	if at, ok := uuidv7Time(thread); ok {
		days = append(days, at, at.AddDate(0, 0, -1), at.AddDate(0, 0, 1))
	}
	for _, root := range []string{"sessions", "archived_sessions"} {
		for _, d := range days {
			dir := filepath.Join(home, root, d.Format("2006"), d.Format("01"), d.Format("02"))
			if p := findSuffix(dir, suffix); p != "" {
				return p
			}
		}
		if p := findSuffix(filepath.Join(home, root), suffix); p != "" {
			return p // archived_sessions is flat
		}
	}
	return ""
}

func findSuffix(dir, suffix string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "rollout-") && strings.HasSuffix(e.Name(), suffix) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// uuidv7Time is the creation time a UUIDv7 carries, in local time.
func uuidv7Time(id string) (time.Time, bool) {
	hexed := strings.ReplaceAll(id, "-", "")
	if len(hexed) != 32 || hexed[12] != '7' {
		return time.Time{}, false
	}
	raw, err := hex.DecodeString(hexed[:12])
	if err != nil {
		return time.Time{}, false
	}
	var ms int64
	for _, b := range raw {
		ms = ms<<8 | int64(b)
	}
	return time.UnixMilli(ms), true
}
