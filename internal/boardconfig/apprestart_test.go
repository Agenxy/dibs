// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"testing"
	"time"
)

func TestAppRestartConfigDefaultsBoundsAndExplicitOff(t *testing.T) {
	window, interval, err := (WakeConfig{}).AppRestart()
	if err != nil || window != 0 || interval != 2*time.Second {
		t.Fatalf("defaults = %v %v %v", window, interval, err)
	}
	window, interval, err = (WakeConfig{ResumeAfterAppRestart: "1h", RestartOpenInterval: "3s"}).AppRestart()
	if err != nil || window != time.Hour || interval != 3*time.Second {
		t.Fatalf("configured = %v %v %v", window, interval, err)
	}
	if _, _, err := (WakeConfig{ResumeAfterAppRestart: "0"}).AppRestart(); err != nil {
		t.Fatalf("explicit off refused: %v", err)
	}
	for _, cfg := range []WakeConfig{
		{ResumeAfterAppRestart: "-1s"},
		{ResumeAfterAppRestart: "25h"},
		{RestartOpenInterval: "0s"},
		{RestartOpenInterval: "unknown"},
	} {
		if _, _, err := cfg.AppRestart(); err == nil {
			t.Errorf("invalid duration accepted: %+v", cfg)
		}
	}
}
