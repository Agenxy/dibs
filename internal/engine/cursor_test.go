package engine

import "testing"

// 0 is the only cursor an agent can pick before it has seen the board, so it is
// what every agent reaches for first. Erroring on it makes an agent's opening
// call fail with a message about ring-buffer internals it cannot know about.
func TestCursorZeroMeansFromTheFloor(t *testing.T) {
	for _, tc := range []struct {
		name          string
		serial, floor uint64
		want          uint64
	}{
		{"zero on a trimmed ring starts at the floor", 0, 223, 222},
		{"zero on a fresh ring stays zero", 0, 0, 0},
		{"a real position is left alone", 500, 223, 500},
		// A genuinely lost cursor must still error: there the agent HAD a
		// position and really did miss events, which is worth telling it.
		{"a stale non-zero cursor is not rescued", 5, 223, 5},
	} {
		if got := clampCursor(tc.serial, tc.floor); got != tc.want {
			t.Errorf("%s: clampCursor(%d, %d) = %d, want %d",
				tc.name, tc.serial, tc.floor, got, tc.want)
		}
	}
}
