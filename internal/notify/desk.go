// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
)

// ErrNotAway means input resumed between the observation and the actual open.
var ErrNotAway = errors.New("person is no longer away")

// Desk is an observed desktop state, not an elapsed-idle inference.
// Unknown or absent displays are never evidence that the person is away.
type Desk struct {
	SessionKnown   bool `json:"session_known"`
	Locked         bool `json:"locked"`
	DisplayKnown   bool `json:"display_known"`
	DisplaysAsleep bool `json:"displays_asleep"`
	FrontmostPID   int  `json:"frontmost_pid"`
}

// Away requires positive OS evidence of screen lock or sleeping displays.
func (d Desk) Away() bool {
	return (d.SessionKnown && d.Locked) || (d.DisplayKnown && d.DisplaysAsleep)
}

// DesktopState queries the signed native helper without displaying anything.
// A missing or older helper returns unknown; it cannot authorize opening an app.
func DesktopState() (Desk, error) {
	path := helper()
	if path == "" {
		return Desk{}, errors.New("native desktop helper unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// #nosec G204 -- the helper is resolved beside this executable; fixed mode.
	raw, err := procexec.Command(ctx, path, "--desk-state").Output()
	if errors.Is(err, exec.ErrWaitDelay) {
		return Desk{}, fmt.Errorf("incomplete output from dibs-notify; desktop state is unknown: %w", err)
	}
	if err != nil {
		return Desk{}, err
	}
	var desk Desk
	if err := json.Unmarshal(raw, &desk); err != nil {
		return Desk{}, err
	}
	return desk, nil
}

// OpenWhenAway is retained only to refuse old callers. Being away cannot
// authorize a navigating wake, and an old installed helper must not revive it.
func OpenWhenAway(_ string, _ time.Duration) error {
	return errors.New("automatic app navigation is retired; mail remains queued until its thread runs")
}
