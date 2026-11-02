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

// BrokerStatus is a read-only observation of the per-user notifier lock. It
// never starts a broker, requests authorization or posts a notification.
type BrokerStatus struct {
	Held  bool   `json:"held"`
	PIDs  []int  `json:"pids"`
	Error string `json:"error"`
}

// NotificationBrokerStatus observes the holder without starting a notifier.
func NotificationBrokerStatus() (BrokerStatus, error) {
	if goos != "darwin" {
		return BrokerStatus{}, nil
	}
	h := helper()
	if h == "" {
		return BrokerStatus{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- bundled executable and fixed read-only diagnostic argument.
	out, err := procexec.Command(ctx, h, "--broker-status").Output()
	if errors.Is(err, exec.ErrWaitDelay) {
		return BrokerStatus{}, fmt.Errorf("incomplete output from dibs-notify; broker status is unknown: %w", err)
	}
	if err != nil {
		return BrokerStatus{}, err
	}
	var status BrokerStatus
	err = json.Unmarshal(out, &status)
	return status, err
}
