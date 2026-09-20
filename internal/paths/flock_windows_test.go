//go:build windows

package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// A registration a daemon holds the lock on is still readable by everybody
// else.
//
// LockFileEx is mandatory: a byte range one handle has locked exclusively
// cannot be read through another handle, and the lock was over byte 0 of the
// registration JSON, so `describe` (os.ReadFile through a fresh handle) got
// ERROR_LOCK_VIOLATION for every LIVE daemon and reported each as Unknown.
// stop and upgrade identify a daemon by the directory in that JSON, and an
// Unknown entry is a stranger, so neither could find the daemon that was
// running. The lock now covers a byte far past any body this file will hold.
// Found by the pre-release review, round four; this test runs in the Windows
// CI job, which is the only place it can.
func TestALockedRegistrationIsStillReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "1234.json")
	const body = `{"pid":1234,"dir":"C:\\Users\\me\\.dibs"}`
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := LockExclusive(holder, false); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.WriteAt([]byte(body), 0); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("a held registration cannot be read through another handle: %v; every live "+
			"daemon reads as Unknown", err)
	}
	if string(got) != body {
		t.Errorf("read %q, want the registration body", got)
	}

	// And the lock still does its job: a second holder is refused at once.
	other, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := LockExclusive(other, false); !LockHeldElsewhere(err) {
		t.Errorf("a second exclusive lock was not refused as held elsewhere: %v", err)
	}
}
