package ui

import (
	"bytes"
	"strings"
	"testing"
)

// Degradation is the property that must never regress.
//
// A coordination tool gets piped into grep, teed into a log, run in CI, and
// redirected into an issue report. Before there was a ui package, `doctor`
// wrote raw ANSI escapes unconditionally — so `lanes doctor > report.txt`
// produced a file of escape sequences, and NO_COLOR did nothing at all.
//
// Rendering to a plain buffer is exactly the not-a-terminal case, so this is
// the check that keeps every styled surface greppable.
func TestStyledOutputIsPlainWhenNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(&bytes.Buffer{}) })

	for _, got := range []string{
		Dim("quiet"), Good("fine"), Attn("look"), Alarm("act"), Accent("id"), Bold("loud"),
		OK("worked"), Bad("broke"), Warn("careful"), Note("context"), Fix("do this"),
		Section("heading"), Field("label", 10, "value"), Pad("x", 4),
		Tally([]Count{{Label: "live", N: 3, Tone: "good"}}),
	} {
		if strings.ContainsRune(got, 0x1b) {
			t.Errorf("escape sequence in output bound for a pipe: %q", got)
		}
	}

	// And the text itself survives, or `lanes board | grep builder` stops
	// working — which is how people actually use this.
	if !strings.Contains(Accent("builder"), "builder") {
		t.Error("styling must never alter the text it wraps")
	}
	if !strings.Contains(OK("all good"), "all good") {
		t.Error("the mark must not replace the message")
	}
}

// Marks carry a symbol as well as a colour. A reader who is colour-blind, on a
// 16-colour terminal where attn and alarm converge, or reading a redirected
// file has nothing else to tell them apart.
func TestMarksAreDistinguishableWithoutColour(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(&bytes.Buffer{}) })

	seen := map[string]string{}
	for name, got := range map[string]string{
		"ok": OK("x"), "bad": Bad("x"), "warn": Warn("x"), "note": Note("x"),
	} {
		sym := strings.TrimSpace(strings.TrimSuffix(got, "x"))
		if sym == "" {
			t.Errorf("%s has no symbol, so it is colour-only", name)
		}
		if prev, dup := seen[sym]; dup {
			t.Errorf("%s and %s share the symbol %q — indistinguishable without colour",
				name, prev, sym)
		}
		seen[sym] = name
	}
}

// Pad and Elide count DISPLAY width, not bytes. Agent names come from whatever
// the operator typed, and %-Ns counts bytes — so one non-ASCII name knocks
// every column out of alignment for the rest of the board.
func TestWidthIsMeasuredInColumnsNotBytes(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(&bytes.Buffer{}) })

	// "日本" is 6 bytes and 4 columns wide.
	if got := Pad("日本", 6); len([]rune(got)) != 4 {
		t.Errorf("want 2 wide runes plus 2 spaces, got %q (%d runes)", got, len([]rune(got)))
	}
	// An é composed of e + combining accent is 2 runes but 1 column.
	if got := Pad("é", 3); strings.Count(got, " ") != 2 {
		t.Errorf("a combining accent must not count as its own column, got %q", got)
	}
}

// Elide keeps BOTH ends: the head says where something lives and the tail says
// what it is. Truncating either alone leaves a line the reader cannot act on —
// a claim path elided to its prefix names a temp directory and nothing else.
func TestElideKeepsBothEndsOfAPath(t *testing.T) {
	long := "/private/tmp/very/long/scratch/dir/with/many/segments/project/internal/auth"
	got := Elide(long, 40)
	if len([]rune(got)) > 40 {
		t.Fatalf("elided to %d runes, want <= 40: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "internal/auth") {
		t.Errorf("the tail says which directory this is; it must survive: %q", got)
	}
	if !strings.HasPrefix(got, "/private") {
		t.Errorf("the head says where it lives; it must survive: %q", got)
	}
	// Short enough already: left exactly alone, never decorated.
	if got := Elide("short", 40); got != "short" {
		t.Errorf("nothing to elide, got %q", got)
	}
}

// A row of zeroes is noise that hides the one number that is not zero — except
// for the figure that is alarming AT zero, which is why Always exists.
func TestTallyDropsZeroesButKeepsTheOneThatMattersAtZero(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(&bytes.Buffer{}) })

	got := Tally([]Count{
		{Label: "of 4 live", N: 0, Tone: "good", Always: true},
		{Label: "declared", N: 0},
		{Label: "out of touch", N: 4, Tone: "attn"},
		{Label: "UNANSWERED", N: 0, Tone: "alarm"},
	})
	if !strings.Contains(got, "0 of 4 live") {
		t.Errorf("nobody being live is the alarming case and must show at zero: %q", got)
	}
	if strings.Contains(got, "declared") || strings.Contains(got, "UNANSWERED") {
		t.Errorf("zero counts are noise and must be dropped: %q", got)
	}
	if !strings.Contains(got, "4 out of touch") {
		t.Errorf("the figure that is not zero must be there: %q", got)
	}
	if Tally(nil) != "" {
		t.Error("nothing to say means an empty line, not an empty separator")
	}
}
