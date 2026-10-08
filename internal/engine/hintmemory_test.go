// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// "You will not be asked again today" holds across a restart.
//
// Reported by agenxy-supply: one unregistered Claude Desktop session was told
// four times in two days, once after each daemon restart, because the memory
// behind the promise lived only in the process.
func TestTheReattachPromiseSurvivesARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reattach-hints.json")
	boot := func() *Engine {
		st := core.NewState("test", core.DefaultLimits())
		st.Agents["agenxy-supply"] = &core.Agent{
			ID: "agenxy-supply", Name: "agenxy-supply", Status: core.StatusDormant, Nonce: "x",
			Agent: &core.AgentInfo{CWD: "/work/agenxy"}, Slots: map[string]core.Slot{},
		}
		e := New(st, &memLedger{}, deadProber{})
		e.SetHintFile(file)
		return e
	}
	now := time.Now()
	if boot().reattachHint("s1", "/work/agenxy", "", now) == "" {
		t.Fatal("the first hint was never given, so this proves nothing")
	}
	if got := boot().reattachHint("s1", "/work/agenxy", "", now.Add(time.Hour)); got != "" {
		t.Errorf("a restarted daemon asked the same session again within the day:\n%s", got)
	}
	if boot().reattachHint("s2", "/work/agenxy", "", now.Add(time.Hour)) == "" {
		t.Error("another session was never told: the memory is per session")
	}
}
