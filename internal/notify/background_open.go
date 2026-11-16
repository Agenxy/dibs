// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/procexec"
)

const helperBudget = 20 * time.Second

// Only the final process boundary is replaced by tests. No helper capability
// or installation failure may select a navigating URL as a fallback.
var backgroundWakeFixture func(context.Context, []string) error

// OpenChatGPTBackground requests queued work through the app's non-navigating
// wake handler. An accepted URL is not a turn or mail-read receipt. Measured
// on ChatGPT 26.1111.21308: -g is required even with the wake-only handler.
func OpenChatGPTBackground(url string) error {
	const prefix = "codex://threads/wake/"
	if !strings.HasPrefix(url, prefix) || !core.LooksLikeThreadID(strings.TrimPrefix(url, prefix)) {
		return errors.New("unsupported automatic app URL; mail waits for its thread to be opened")
	}
	if backgroundWakeFixture == nil && (testing.Testing() || os.Getenv("DIBS_TEST_FORBID_APP_OPEN") == "1") {
		return errors.New("unfaked background wake forbidden in tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), helperBudget)
	defer cancel()
	argv := []string{"/usr/bin/open", "-g", url}
	var err error
	if backgroundWakeFixture != nil {
		err = backgroundWakeFixture(ctx, argv)
	} else {
		// #nosec G204 -- fixed OS executable and validated wake-only URL, no shell.
		err = procexec.Command(ctx, argv[0], argv[1:]...).Run()
	}
	if err != nil {
		return fmt.Errorf("background wake unconfirmed; mail remains queued: %w", err)
	}
	return nil
}
