package wakeexec

import "testing"

func TestAppRestartRequiresExactNativeQueueTemplate(t *testing.T) {
	good := []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
		"queue", "--thread", "{thread}", "--message", "{message}",
	}
	if !NativeQueueTemplate(good) {
		t.Fatal("canonical queue template refused")
	}
	for _, bad := range [][]string{
		{"codex", "exec", "resume", "{thread}"},
		{"codex", "queue", "--thread", "fixed-thread", "--message", "{message}"},
		{"codex", "queue", "--thread", "{thread}", "--message", "fixed-text"},
		{"codex", "queue", "--thread", "{thread}", "--message", "{message}", "--extra"},
	} {
		if NativeQueueTemplate(bad) {
			t.Errorf("wrong queue target/message admitted: %v", bad)
		}
	}
}
