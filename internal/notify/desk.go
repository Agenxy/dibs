package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"time"
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
	raw, err := exec.CommandContext(ctx, path, "--desk-state").Output()
	if err != nil {
		return Desk{}, err
	}
	var desk Desk
	if err := json.Unmarshal(raw, &desk); err != nil {
		return Desk{}, err
	}
	return desk, nil
}

// OpenWhenAway rechecks OS presence at the actual open boundary and restores
// the prior frontmost app while away. It never falls back to an activating open.
func OpenWhenAway(url string, minIdle time.Duration) error {
	path := helper()
	if path == "" {
		return errors.New("native desktop helper unavailable; thread remains queued")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	// #nosec G204 -- fixed helper mode; URL is checked again by the native helper.
	err := exec.CommandContext(ctx, path, "--open-away", url,
		strconv.FormatFloat(minIdle.Seconds(), 'f', -1, 64)).Run()
	var exited *exec.ExitError
	if errors.As(err, &exited) && exited.ExitCode() == 3 {
		return ErrNotAway
	}
	return err
}
