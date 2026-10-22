// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testport

import (
	"fmt"
	"os"
	"strings"
)

func outageEphemeralRange() (first, last int, supported bool, err error) {
	body, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, true, err
	}
	if _, err := fmt.Fscan(strings.NewReader(string(body)), &first, &last); err != nil {
		return 0, 0, true, err
	}
	return first, last, true, nil
}
