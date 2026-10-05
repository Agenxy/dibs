package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The status request is harmless to an older helper: it ignores the capability
// environment and returns its old notification-status word. Never send an
// unknown option to an old notifier; it might interpret it as a banner title.
const backgroundOpenCapability = "DIBS_BACKGROUND_OPEN_V1"

type backgroundOpenReceipt struct {
	Version          int    `json:"version"`
	Opened           bool   `json:"opened"`
	RestoreAttempted bool   `json:"restore_attempted"`
	RestoreAccepted  bool   `json:"restore_accepted"`
	Reason           string `json:"reason"`
	PreviousPID      int    `json:"previous_pid"`
	FrontmostPID     int    `json:"frontmost_pid"`
}

// backgroundOpenFixture replaces the process contact, not the public wrapper
// or its capability and receipt decisions. It is never set by production.
var backgroundOpenFixture func(context.Context, string, []string, []string) ([]byte, error)
var backgroundHelper = helper

func backgroundOpenOutput(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
	if backgroundOpenFixture != nil {
		return backgroundOpenFixture(ctx, binary, argv, env)
	}
	if len(argv) > 0 && argv[0] == "--open-background" {
		if testing.Testing() {
			panic("unfaked native background opener in a Go test")
		}
		if os.Getenv("DIBS_TEST_FORBID_APP_OPEN") == "1" {
			return nil, errors.New("native background opener forbidden by DIBS_TEST_FORBID_APP_OPEN")
		}
	}
	// #nosec G204 -- signed helper resolved beside this executable; fixed modes.
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.Output()
}

// OpenChatGPTBackground opens an already app-owned thread and attempts one
// bounded restoration of the previous app. An accepted open is not evidence
// that a thread loaded, that mail was read, or that activation was invisible.
// The caller owns host-wide serialization of the open/restore pair.
func OpenChatGPTBackground(url string) error {
	if goos != "darwin" {
		return errors.New("background ChatGPT opening requires the macOS native helper; mail remains queued")
	}
	if !strings.HasPrefix(url, "codex://threads/") || len(url) > 150 || strings.ContainsAny(url, "\r\n\x00") {
		return errors.New("unsupported ChatGPT thread URL; use an existing app-owned thread")
	}
	binary := backgroundHelper()
	if binary == "" {
		return errors.New("native background-open helper unavailable; reinstall the matching Dibs.app, mail remains queued")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := backgroundOpenOutput(ctx, binary, []string{"--status"}, []string{backgroundOpenCapability + "=1"})
	if err != nil {
		return fmt.Errorf("native background-open capability unavailable; reinstall the matching Dibs.app, mail remains queued: %w", err)
	}
	var capability struct {
		Version int `json:"background_open"`
	}
	if json.Unmarshal(raw, &capability) != nil || capability.Version != 1 {
		return errors.New("native helper lacks background-open v1; reinstall the matching Dibs.app, mail remains queued")
	}
	raw, err = backgroundOpenOutput(ctx, binary, []string{"--open-background", url}, nil)
	if err != nil {
		return fmt.Errorf("native background open failed; mail remains queued: %w", err)
	}
	var receipt backgroundOpenReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || !receipt.Opened {
		return errors.New("native background-open receipt unknown; do not retry the open, inspect the helper installation")
	}
	slog.Debug("native background thread open accepted", "restore_attempted", receipt.RestoreAttempted,
		"restore_accepted", receipt.RestoreAccepted, "reason", receipt.Reason,
		"previous_pid", receipt.PreviousPID, "frontmost_pid", receipt.FrontmostPID)
	return nil
}
