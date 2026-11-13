// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testexec

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func kernelZombie(pid int) (bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false, err
	}
	_, rest, found := strings.Cut(string(raw), ") ")
	if !found {
		return false, fmt.Errorf("invalid kernel stat fixture")
	}
	return strings.HasPrefix(rest, "Z "), nil
}
