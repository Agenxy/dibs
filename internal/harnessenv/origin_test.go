package harnessenv

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRollout files a thread the way Codex does: under the local date its
// UUIDv7 encodes, with the header as the first line.
func writeRollout(t *testing.T, home, thread, originator string, dayOffset int) {
	t.Helper()
	at, ok := uuidv7Time(thread)
	if !ok {
		t.Fatalf("setup: %s is not a UUIDv7", thread)
	}
	at = at.AddDate(0, 0, dayOffset)
	dir := filepath.Join(home, "sessions", at.Format("2006"), at.Format("01"), at.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	head := `{"timestamp":"x","type":"session_meta","payload":{"id":"` + thread +
		`","originator":"` + originator + `","base_instructions":{"text":"long"}}}` + "\n" +
		`{"type":"event_msg"}` + "\n"
	name := "rollout-" + at.Format("2006-01-02T15-04-05") + "-" + thread + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The thread's own header says where it was born, which is where a wake
// belongs when no bridge has said where the agent runs: a dormant agent makes
// no call until something wakes it, so the bridge's answer arrives too late
// for the one wake that needs it. Measured spellings: the ChatGPT app writes
// "Codex Desktop", `codex exec` writes "codex_exec".
func TestAThreadSaysWhichAppItWasBornIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	const (
		app      = "01a0f45e-cbf8-7ef0-acb8-79c25cb4343d"
		headless = "01a0f0b4-09a6-79d0-9206-c5b807aad7e6"
		dayOff   = "01a0f596-1392-74f0-a0c2-3743b384b58c"
		missing  = "01a0f596-0000-7000-a0c2-3743b384b58c"
	)
	writeRollout(t, home, app, "Codex Desktop", 0)
	writeRollout(t, home, headless, "codex_exec", 0)
	writeRollout(t, home, dayOff, "Codex Desktop", 1) // Codex filed it under the next local day

	if !CodexBornInApp(app) {
		t.Error("a thread the app created read as not born in the app: its wake queues into a thread nobody loads")
	}
	if !CodexBornInApp(dayOff) {
		t.Error("a thread filed under the neighbouring day was not found: a timezone difference hides it")
	}
	if CodexBornInApp(headless) {
		t.Error("a `codex exec` thread read as an app thread: the app would be opened on it")
	}
	// A file filed under one thread whose header names another is not that
	// thread's transcript, whatever its name says.
	writeRollout(t, home, missing, "Codex Desktop", 0)
	renamed := "01a0f596-1111-7000-a0c2-3743b384b58c"
	if err := os.Rename(rolloutFile(home, missing), filepath.Join(filepath.Dir(rolloutFile(home, missing)),
		"rollout-x-"+renamed+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if CodexBornInApp(renamed) {
		t.Error("a transcript whose header names a different thread was believed")
	}
	if CodexBornInApp("01a0f596-2222-7000-a0c2-3743b384b58c") {
		t.Error("a thread with no transcript read as an app thread: an unproven app was opened")
	}

	if got := AppFor("", "Codex", app); got != ChatGPTApp {
		t.Errorf("AppFor with no bridge word = %q, want the app the thread was born in", got)
	}
	if got := AppFor(CodexOutsideApp, "Codex", app); got != CodexOutsideApp {
		t.Errorf("AppFor = %q: a bridge saying the agent now runs in a terminal must outrank where "+
			"the thread was born, or a moved thread is pulled back into the app", got)
	}
	if got := AppFor("", "claude-code", app); got != "" {
		t.Errorf("AppFor for a non-Codex agent = %q", got)
	}
}

func TestAUUIDv7NamesTheDayItWasMade(t *testing.T) {
	at, ok := uuidv7Time("01a0f45e-cbf8-7ef0-acb8-79c25cb4343d")
	if !ok || at.Year() != 2026 || at.Month() != time.September {
		t.Errorf("decoded %v %v", at, ok)
	}
	if _, ok := uuidv7Time("0199a0b1-c2d3-4e5f-8a9b-0c1d2e3f4a5b"); ok {
		t.Error("a UUIDv4 decoded as carrying a time")
	}
}
