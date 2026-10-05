package notify

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestBackgroundOpenRefusesOldHelperWithoutSendingUnknownMode(t *testing.T) {
	previousHelper, previousOutput, previousOS := backgroundHelper, backgroundOpenFixture, goos
	t.Cleanup(func() { backgroundHelper, backgroundOpenFixture, goos = previousHelper, previousOutput, previousOS })
	goos = "darwin"
	backgroundHelper = func() string { return "fixture-helper" }
	calls := 0
	backgroundOpenFixture = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded helper contact")
		}
		if !reflect.DeepEqual(argv, []string{"--status"}) || !reflect.DeepEqual(env, []string{"DIBS_BACKGROUND_OPEN_V1=1"}) {
			t.Fatalf("unsafe old-helper contact: %q %q", argv, env)
		}
		return []byte("authorized\n"), nil
	}
	if err := OpenChatGPTBackground("codex://threads/compatibility"); err == nil || calls != 1 {
		t.Fatalf("old helper: calls=%d err=%v", calls, err)
	}
}

func TestBackgroundOpenNeverRetriesAmbiguousNativeOutcome(t *testing.T) {
	previousHelper, previousOutput, previousOS := backgroundHelper, backgroundOpenFixture, goos
	t.Cleanup(func() { backgroundHelper, backgroundOpenFixture, goos = previousHelper, previousOutput, previousOS })
	goos = "darwin"
	backgroundHelper = func() string { return "fixture-helper" }
	for _, name := range []string{"bad-receipt", "lost-reply"} {
		t.Run(name, func(t *testing.T) {
			opens := 0
			backgroundOpenFixture = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
				if reflect.DeepEqual(argv, []string{"--status"}) {
					return []byte(`{"background_open":1}`), nil
				}
				opens++
				if name == "lost-reply" {
					return nil, errors.New("reply lost after accepted open")
				}
				return []byte(`{"opened":true}`), nil
			}
			if err := OpenChatGPTBackground("codex://threads/ambiguous"); err == nil || opens != 1 {
				t.Fatalf("ambiguous open retried or confirmed: opens=%d err=%v", opens, err)
			}
		})
	}
}
