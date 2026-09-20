package main

import (
	"testing"
	"time"
)

// The bridge ships an index only when the daemon has said it could not read
// the tree: a daemon that read it has a better index than a copy, and one
// that has not looked yet may still read it. Issue #19.
func TestTheBridgeShipsOnlyWhenTheDaemonCouldNotRead(t *testing.T) {
	root := "/Users/me/Desktop/proj"
	for _, tc := range []struct {
		name string
		st   matchStatusJSON
		want bool
	}{
		{"daemon has not looked", matchStatusJSON{Phase: "off"}, false},
		{"daemon is indexing it", matchStatusJSON{Phase: "indexing", Repo: root}, false},
		{"daemon read it", matchStatusJSON{Phase: "ready", Repo: root}, false},
		{"daemon could not read it", matchStatusJSON{Phase: "ready", Unreadable: []string{root}}, true},
		{"daemon could not read a subdirectory of it", matchStatusJSON{Unreadable: []string{root + "/internal/core"}}, true},
		{"daemon could not read some OTHER tree", matchStatusJSON{Unreadable: []string{"/Users/me/Documents/x"}}, false},
	} {
		if got := wantsIndex(tc.st, root); got != tc.want {
			t.Errorf("%s: wantsIndex = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := indexURL("http://127.0.0.1:4777/mcp"); got != "http://127.0.0.1:4777/api/index" {
		t.Errorf("indexURL = %q", got)
	}
}

// The schedule must outlast the daemon's Git deadline, or the fallback it
// exists for never fires for the case that motivated it.
func TestTheShipScheduleOutlastsTheDaemonsGitDeadline(t *testing.T) {
	var total time.Duration
	for _, d := range shipSchedule {
		total += d
	}
	if total <= daemonGitDeadline+30*time.Second {
		t.Fatalf("the ship schedule gives up after %v, before the daemon has finished "+
			"waiting on Git (%v) and said the tree is unreadable", total, daemonGitDeadline)
	}
}
