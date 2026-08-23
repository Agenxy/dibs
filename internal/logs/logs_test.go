package logs

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestRingIsBounded: nothing is immortal. The ring must drop the oldest record
// rather than grow, or a long-running daemon leaks memory.
func TestRingIsBounded(t *testing.T) {
	r := NewRing(4)
	for i := range 20 {
		r.add(Record{Msg: string(rune('a' + i))})
	}
	got := r.Tail(0)
	if len(got) != 4 {
		t.Fatalf("ring held %d records, cap is 4", len(got))
	}
	if got[len(got)-1].Msg != string(rune('a'+19)) {
		t.Fatalf("newest record lost: %q", got[len(got)-1].Msg)
	}
	if got[0].Msg != string(rune('a'+16)) {
		t.Fatalf("expected oldest-first ordering from the tail, got %q", got[0].Msg)
	}
}

// TestSensitiveAttrsAreRedactedAtCapture: the ledger encrypts tokens and message
// bodies. A debug log that printed them would quietly undo that, so redaction
// happens when the record is captured: no copy ever holds the real value.
func TestSensitiveAttrsAreRedactedAtCapture(t *testing.T) {
	r := NewRing(8)
	h := NewHandler(slog.NewTextHandler(discard{}, nil), r)
	log := slog.New(h)

	log.Info("register", "token", "SUPER-SECRET", "agent", "alpha")
	log.Info("send", "body", "the private message", "msg_serial", 7)
	log.With("local_secret", "ALSO-SECRET").Info("wired")

	all := r.Tail(0)
	if len(all) != 3 {
		t.Fatalf("captured %d records, want 3", len(all))
	}
	for _, rec := range all {
		for k, v := range rec.Attrs {
			s, _ := v.(string)
			if strings.Contains(s, "SECRET") || strings.Contains(s, "private message") {
				t.Fatalf("record %q leaked %s=%v", rec.Msg, k, v)
			}
		}
	}
	if all[0].Attrs["token"] != "[redacted]" {
		t.Fatalf("token should be redacted, got %v", all[0].Attrs["token"])
	}
	if all[0].Attrs["agent"] != "alpha" {
		t.Fatal("non-sensitive attrs must survive: a log nobody can read is useless")
	}
	if all[2].Attrs["local_secret"] != "[redacted]" {
		t.Fatal("attrs attached via With() must be redacted too")
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Redaction covers stderr, not only the board's log view.
//
// The handler redacted its own copy for /api/logs and forwarded the ORIGINAL
// record to the base handler, so every value the list calls sensitive was
// hidden from the board and printed in full on stderr: where a service manager
// collects it, where a crash reporter picks it up, and where anything reading
// the daemon's output can see it. The list read as a guarantee about logging
// and was a guarantee about one of its two destinations.
func TestSensitiveValuesAreRedactedOnStderrToo(t *testing.T) {
	var base bytes.Buffer
	ring := NewRing(8)
	log := slog.New(NewHandler(slog.NewTextHandler(&base, nil), ring))

	const leak = "sk-live-do-not-print-this"
	log.Info("an agent registered", "agent", "reviewer", "token", leak,
		"output", "decrypted mail from the resumed turn")

	if strings.Contains(base.String(), leak) {
		t.Errorf("the token reached stderr in full:\n  %s\n"+
			"That is where a service manager collects the daemon's output, so "+
			"redacting only the in-memory ring protects the one destination an "+
			"operator is least likely to be reading", strings.TrimSpace(base.String()))
	}
	if strings.Contains(base.String(), "decrypted mail") {
		t.Errorf("a wake command's output reached stderr: %s", strings.TrimSpace(base.String()))
	}
	// The record still says what happened, or redaction has become deletion.
	for _, want := range []string{"an agent registered", "reviewer", "[redacted]"} {
		if !strings.Contains(base.String(), want) {
			t.Errorf("stderr lost %q, so the log no longer says what happened: %s",
				want, strings.TrimSpace(base.String()))
		}
	}
}
