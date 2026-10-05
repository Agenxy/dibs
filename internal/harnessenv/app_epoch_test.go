package harnessenv

import (
	"context"
	"errors"
	"testing"
)

func TestChatGPTAppEpochSeparatesUnknownAbsentAndRunning(t *testing.T) {
	old := appProbeOutput
	t.Cleanup(func() { appProbeOutput = old })
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("ps failed") }
	if epoch, known := ChatGPTAppEpoch(); known || epoch != "" {
		t.Fatalf("failed probe looked known: %q %t", epoch, known)
	}
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) { return []byte(""), nil }
	if epoch, known := ChatGPTAppEpoch(); !known || epoch != "absent" {
		t.Fatalf("absent app looked unknown: %q %t", epoch, known)
	}
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("123 Mon Oct 5 13:00:00 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n"), nil
	}
	a, known := ChatGPTAppEpoch()
	if !known || len(a) != 64 {
		t.Fatalf("running epoch = %q %t", a, known)
	}
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("456 Mon Oct 5 13:01:00 2026 /Applications/ChatGPT.app/Contents/MacOS/ChatGPT\n"), nil
	}
	b, known := ChatGPTAppEpoch()
	if !known || a == b {
		t.Fatalf("replacement did not change epoch: %q %q %t", a, b, known)
	}
}
