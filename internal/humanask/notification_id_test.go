package humanask

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_HUMAN_POST_DRIVER") == "1" && filepath.Base(os.Args[0]) == "dibs-notify" {
		if len(os.Args) == 2 && os.Args[1] == "--settings" {
			_, _ = os.Stdout.WriteString("timeSensitive=1\n")
			os.Exit(0)
		}
		if os.Getenv("DIBS_TEST_EXPECT_PRIORITY") == "1" {
			if os.Getenv("DIBS_NOTIFY_TIME_SENSITIVE") != "1" {
				os.Exit(3)
			}
		} else if os.Getenv("DIBS_NOTIFY_ID") != "dibs.msg.board-A.7" {
			os.Exit(3)
		}
		path := os.Getenv("DIBS_NOTIFY_RECEIPT")
		if path == "" || os.WriteFile(path, []byte(`{"state":"posted"}`), 0o600) != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString("Yes\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Enter through humanask.Ask, the actual presenter that the engine and relay
// call, rather than calling its downstream keyed notifier directly.
func TestHumanQuestionPostsItsBoardScopedMessageID(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("installed macOS helper route")
	}
	if os.Getenv("DIBS_TEST_HUMAN_POST_DRIVER") == "1" {
		posted := false
		answer, err := Ask(Message{
			Type: core.MsgQuestion, From: "sender", Body: "question", Choices: []string{"Yes"},
			Node: "board-A", Serial: 7, Receipt: func(state string) { posted = state == "posted" },
		})
		if err != nil || answer.Disposition != "answer" || answer.Body != "Yes" || !posted {
			t.Fatalf("actual human presenter: %+v posted=%v error=%v", answer, posted, err)
		}
		t.Setenv("DIBS_TEST_EXPECT_PRIORITY", "1")
		_, err = Ask(Message{
			Type: core.MsgNotify, Priority: "high", From: "sender", Body: "alert",
			Receipt: func(string) {},
		})
		if err != nil {
			t.Fatalf("high notify was not Time Sensitive: %v", err)
		}
		return
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "driver.test")
	helper := filepath.Join(dir, "Dibs.app", "Contents", "MacOS", "dibs-notify")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{driver, helper} {
		if err := os.WriteFile(path, bytes, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestHumanQuestionPostsItsBoardScopedMessageID$") // #nosec G204 -- private fixture binary
	cmd.Env = append(os.Environ(), "DIBS_TEST_HUMAN_POST_DRIVER=1", "DIBS_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("human notification identity wiring: %v\n%s", err, out)
	}
}
