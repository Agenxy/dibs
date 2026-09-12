package main

import (
	"testing"
	"time"
)

// `[wake] remind_stale_after`: an hour unless set, off when said, and never a
// value short enough to be noise.
func TestRemindStaleAfterIsReadHonestly(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
		err  bool
	}{
		{"", time.Hour, false},
		{"off", 0, false},
		{"0", 0, false},
		{"2h", 2 * time.Hour, false},
		{"5s", 0, true},
		{"soon", 0, true},
	} {
		got, err := staleReminder(WakeConfig{RemindStaleAfter: tc.raw})
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("remind_stale_after = %q: got %v, %v; want %v, err=%v", tc.raw, got, err, tc.want, tc.err)
		}
	}
}
