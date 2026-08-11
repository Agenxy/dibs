package logs

import (
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

	log.Info("register", "token", "SUPER-SECRET", "lane", "alpha")
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
	if all[0].Attrs["lane"] != "alpha" {
		t.Fatal("non-sensitive attrs must survive: a log nobody can read is useless")
	}
	if all[2].Attrs["local_secret"] != "[redacted]" {
		t.Fatal("attrs attached via With() must be redacted too")
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
