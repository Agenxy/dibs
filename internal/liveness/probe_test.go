package liveness

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The two record shapes, copied from real transcripts on disk rather than
// written from the documentation: the documentation for these does not exist.
//
// A wrong reading here is worse than none: the classifier falls back to file
// growth when Tokens returns 0, but a number that moves for the wrong reason
// makes a stalled agent look busy, which is the failure this whole package is
// meant to prevent.
func TestTokensReadsWhatTheHarnessesActuallyWrite(t *testing.T) {
	dir := t.TempDir()

	codex := filepath.Join(dir, "rollout.jsonl")
	if err := os.WriteFile(codex, []byte(
		`{"timestamp":"2026-07-29T19:06:06.606Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1850276,"cached_input_tokens":1682944,"output_tokens":7837,"total_tokens":1858113}}}}`+"\n"+
			`{"type":"response_item","payload":{"type":"message"}}`+"\n"+
			`{"timestamp":"2026-07-29T19:07:30.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":2684439}}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The LAST total, not the first and not the sum: codex reports a running
	// cumulative figure, so adding them would triple-count and climb even while
	// the agent produced nothing new.
	if got := Tokens(codex); got != 2684439 {
		t.Errorf("codex transcript: got %d, want 2684439 (the latest cumulative total)", got)
	}

	claude := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(claude, []byte(
		`{"type":"assistant","message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":1090,"cache_read_input_tokens":380032,"output_tokens":683}}}`+"\n"+
			`{"type":"user"}`+"\n"+
			`{"type":"assistant","message":{"usage":{"input_tokens":3,"cache_read_input_tokens":100,"output_tokens":50}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Claude Code reports PER-MESSAGE usage, so these accumulate. 381807 + 153.
	if got := Tokens(claude); got != 381960 {
		t.Errorf("claude transcript: got %d, want 381960 (the sum of per-message usage)", got)
	}

	pi := filepath.Join(dir, "pi-session.jsonl")
	if err := os.WriteFile(pi, []byte(
		`{"type":"session","version":3,"id":"s1"}`+"\n"+
			`{"type":"message","message":{"role":"assistant","usage":{"input":3874,"output":75,"totalTokens":3949}}}`+"\n"+
			`{"type":"message","message":{"role":"assistant","usage":{"input":100,"output":20,"totalTokens":120}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// pi reports a per-message total under its own name, so these accumulate.
	// Its field names do not overlap with Claude Code's, which is why one
	// struct reads both without either harness contaminating the other's count.
	if got := Tokens(pi); got != 4069 {
		t.Errorf("pi transcript: got %d, want 4069 (3949 + 120)", got)
	}

	// An unrecognised format must return 0 ("no signal") rather than a number.
	other := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(other, []byte(`{"tokens":"a lot"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Tokens(other); got != 0 {
		t.Errorf("unknown format: got %d, want 0 so the classifier falls back to bytes", got)
	}
	if got := Tokens(filepath.Join(dir, "absent.jsonl")); got != 0 {
		t.Errorf("missing file: got %d, want 0", got)
	}
}

// ps prints cumulative time in a format that varies by how long the process has
// run, and a silent zero here would make every agent look frozen.
func TestPSTimeParsesEveryFormItPrints(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"0:08.50", 8*time.Second + 500*time.Millisecond}, // the common case
		{"12:57.10", 12*time.Minute + 57*time.Second + 100*time.Millisecond},
		{"109:13.35", 109*time.Minute + 13*time.Second + 350*time.Millisecond},
		{"3-08:14:08", 3*24*time.Hour + 8*time.Hour + 14*time.Minute + 8*time.Second}, // days
		{"", 0},
		{"garbage", 0},
	} {
		if got := parsePSTime(c.in); got != c.want {
			t.Errorf("parsePSTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Transcript discovery must follow the PROCESS, not the filesystem.
//
// The first version picked the most recently modified transcript on disk. Run
// from inside a Claude Code session, that discovered the PARENT'S own
// transcript (appended to constantly) and reported a subagent as "working"
// on the strength of its supervisor's activity. Caught by printing the path it
// had chosen, which is the only reason it was caught at all.
func TestTranscriptDiscoveryFollowsTheProcess(t *testing.T) {
	// This process holds no harness transcript open, so the honest answer is
	// nothing. NOT whichever session file happens to be freshest on this disk.
	if got := FindTranscript(os.Getpid()); got != "" {
		t.Errorf("found %q for a process that has no transcript open\n"+
			"  discovery by recency is how a parent ends up watching its own session\n"+
			"  and calling a dead child healthy", got)
	}
	if got := FindTranscript(0); got != "" {
		t.Errorf("found %q for an invalid pid", got)
	}
}

// The predicate that decides what counts, separated from the process lookup so
// the path shapes can be checked without spawning anything.
func TestOnlyHarnessTranscriptsCount(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	yes := []string{
		filepath.Join(home, ".codex/sessions/2026/07/29/rollout-2026-07-29T12-02-30-019faf41.jsonl"),
		filepath.Join(home, ".claude/projects/-home-ada-agents/8064473b.jsonl"),
	}
	no := []string{
		filepath.Join(home, ".codex/sessions/2026/07/29/rollout.txt"), // not jsonl
		filepath.Join(home, "notes.jsonl"),                            // not a session dir
		"/tmp/rollout-anything.jsonl",                                 // not under home
		"",
	}
	for _, p := range yes {
		if !isTranscript(p) {
			t.Errorf("%q should be recognised as a transcript", p)
		}
	}
	for _, p := range no {
		if isTranscript(p) {
			t.Errorf("%q should NOT be recognised as a transcript", p)
		}
	}
}
