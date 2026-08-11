package main

import (
	"testing"
	"time"
)

// The check that answers "is what is running what I last built".
//
// `dibs` and `dibd` are separate processes and installing a binary does not
// restart the one already serving, so a fix can be built, installed, and absent
// from every answer the board gives with no error anywhere: both sides even
// report the same version string, because in development both read `devel`.
// A daemon predating a panel fix served the old template for hours that way, and
// the only symptom was a screenshot that looked wrong.
func TestStaleDaemonComparesBuildTimeAgainstStartTime(t *testing.T) {
	started := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	stamp := started.Format(time.RFC3339)

	if !staleDaemon(started.Add(time.Minute), stamp) {
		t.Error("a binary built AFTER the process started is not being reported stale. " +
			"this is the whole case the check exists for")
	}
	if staleDaemon(started.Add(-time.Hour), stamp) {
		t.Error("a binary older than the running process was reported stale")
	}
	if staleDaemon(started, stamp) {
		t.Error("identical times reported stale; a daemon started from the binary it " +
			"is running is the normal, healthy state")
	}
}

// An unreadable start time must not raise an alarm.
//
// False alarms are the expensive failure for a line like this: somebody restarts
// a healthy daemon, learns the warning is noise, and ignores it on the one
// occasion it is right. Silence is the safe direction: the daemon that cannot
// report a start time is handled separately, and told to restart for a reason
// that is actually true.
func TestAnUnreadableStartTimeIsNotAnAlarm(t *testing.T) {
	for _, bad := range []string{"", "not a time", "0", "2026-13-45T99:99:99Z"} {
		if staleDaemon(time.Now(), bad) {
			t.Errorf("staleDaemon(now, %q) = true: a parse failure became a warning", bad)
		}
	}
}
