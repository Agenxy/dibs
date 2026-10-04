package harnessenv

import (
	"os"
	"testing"
)

// Enter through the real probe: inherited timezones must not give the bridge
// and daemon different start stamps for the very same live process.
func TestBridgeStartStampIgnoresInheritedTimezone(t *testing.T) {
	t.Setenv("TZ", "UTC")
	utc := ProcessStart(os.Getpid())
	if utc == "" {
		t.Fatal("setup: the actual process start stamp is unavailable")
	}
	t.Setenv("TZ", "America/Los_Angeles")
	local := ProcessStart(os.Getpid())
	if local != utc {
		t.Fatalf("same process has different start stamps: UTC=%q local=%q", utc, local)
	}
}
