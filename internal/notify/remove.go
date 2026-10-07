// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CleanupBatch caps native helper work on startup and relay reconnect.
const CleanupBatch = 64

// Cleanup is observational. Requested is not proof that a banner disappeared.
type Cleanup struct {
	State      string `json:"state"`
	BestEffort bool   `json:"best_effort"`
	Error      string `json:"error,omitempty"`
}

// MessageID names one notification within its board's namespace.
func MessageID(node string, serial uint64) (string, error) {
	if node == "" || len(node) > 128 || serial == 0 || strings.IndexFunc(node, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-", r)
	}) >= 0 {
		return "", fmt.Errorf("notification identifier needs a board node and nonzero message serial")
	}
	return "dibs.msg." + node + "." + strconv.FormatUint(serial, 10), nil
}

// RemoveMessages asks this Mac to remove exactly these board-scoped messages.
// Its structured reply distinguishes new helpers from old ones that ignore flags.
func RemoveMessages(node string, serials []uint64) Cleanup {
	fail := func(state string, err error) Cleanup {
		return Cleanup{State: state, BestEffort: true, Error: err.Error()}
	}
	if len(serials) == 0 || len(serials) > CleanupBatch {
		return fail("failed", fmt.Errorf("notification cleanup takes 1 to %d messages", CleanupBatch))
	}
	var ids []string
	for _, serial := range serials {
		id, err := MessageID(node, serial)
		if err != nil {
			return fail("failed", err)
		}
		ids = append(ids, id)
	}
	if goos != "darwin" || silenced() {
		return fail("unsupported", ErrUnsupported)
	}
	h := helper()
	if h == "" {
		return fail("unsupported", ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// ONE argument is refused by old helpers before their three-argument
	// posting path, even for a batch. ONE process handles the whole batch.
	arg := "--remove-messages=" + strings.Join(ids, ",")
	out, err := exec.CommandContext(ctx, h, arg).Output() // #nosec G204 -- bundled helper; validated IDs
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return fail("unsupported", fmt.Errorf("notifier does not support message cleanup"))
		}
		return fail("failed", err)
	}
	var reply struct {
		Cleanup string `json:"cleanup"`
	}
	if json.Unmarshal(out, &reply) != nil || reply.Cleanup != "requested" {
		return fail("unsupported", fmt.Errorf("notifier did not confirm the cleanup protocol"))
	}
	return Cleanup{State: "requested", BestEffort: true}
}
