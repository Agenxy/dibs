package notify

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A window that drew nothing must not read as a deferral.
//
// THE BUG THIS CATCHES. askInAWindow creates the answer file itself, then asked
// os.ReadFile for an error to decide whether anything came back. That error can
// never arrive: the file exists because this code made it. So a helper that
// crashed, was never installed, or drew no window at all left the file exactly
// as created and the function returned ("", nil) — no answer and no error,
// which every caller treats as a deliberate "not now".
//
// The failure it was written to report was therefore the single case it could
// not report, and there was no behavioural test of this path to notice. Found
// by a pre-release review.
func TestAWindowThatDrewNothingIsNotAnAnswer(t *testing.T) {
	dir := t.TempDir()

	t.Run("a press comes back", func(t *testing.T) {
		p := filepath.Join(dir, "pressed")
		if err := os.WriteFile(p, []byte("Approve\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := answerFrom(p)
		if err != nil || got != "Approve" {
			t.Errorf("answerFrom = (%q, %v), want (\"Approve\", nil)", got, err)
		}
	})

	t.Run("the helper drew nothing and left the file empty", func(t *testing.T) {
		p := filepath.Join(dir, "empty")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := answerFrom(p)
		if !errors.Is(err, ErrNoAnswer) {
			t.Errorf("answerFrom = (%q, %v), want ErrNoAnswer. Reported as a "+
				"non-answer with no error, the caller cannot tell a window nobody "+
				"saw from one somebody deferred, and the agent waiting on it times "+
				"out against a question that was never asked", got, err)
		}
	})

	t.Run("whitespace only is still nothing", func(t *testing.T) {
		p := filepath.Join(dir, "blank")
		if err := os.WriteFile(p, []byte("  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := answerFrom(p); !errors.Is(err, ErrNoAnswer) {
			t.Errorf("a file of whitespace was read as a press: %v", err)
		}
	})

	t.Run("the file is missing entirely", func(t *testing.T) {
		if _, err := answerFrom(filepath.Join(dir, "nope")); !errors.Is(err, ErrNoAnswer) {
			t.Errorf("a missing answer file was not reported: %v", err)
		}
	})
}
