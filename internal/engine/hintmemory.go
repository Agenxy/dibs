package engine

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

// The reattach pointer's memory survives a restart.
//
// It promises "you will not be asked again today", and the memory behind
// that promise was a map in this process. A daemon restarted four times in
// two days (every install restarts it) told one unregistered session four
// times, by UserPromptSubmit and by Stop, and the agent reported the broken
// promise. Ledgering it would be wrong: it is not coordination state and
// replay has no business with it. A file beside the ledger is a derived,
// losable view (AGENTS.md rule 3): losing it costs one repeated sentence.

// SetHintFile names the file the reattach memory is kept in and loads it.
// Called once at startup, before Run. Empty keeps it in memory only.
func (e *Engine) SetHintFile(path string) {
	e.hintFile = path
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the daemon's own data directory
	if err != nil {
		return
	}
	var saved map[string]time.Time
	if json.Unmarshal(raw, &saved) != nil {
		return
	}
	now := time.Now()
	for id, when := range saved {
		if now.Sub(when) <= hintMemory {
			e.hinted[id] = when
		}
	}
}

// saveHints writes the memory. On the loop, after a pointer is spent, which
// is at most once per session per day.
func (e *Engine) saveHints() {
	if e.hintFile == "" {
		return
	}
	raw, _ := json.Marshal(e.hinted)
	tmp := e.hintFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		err = os.Rename(tmp, e.hintFile)
		if err == nil {
			return
		}
	}
	slog.Debug("could not save the reattach memory; a restart may repeat one pointer", "file", e.hintFile)
}
