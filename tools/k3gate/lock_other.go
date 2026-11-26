// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !unix

package main

import "errors"

// The remote half runs only on the Linux seat.
func lock(string) (func(), error) {
	return nil, errors.New("k3gate -remote runs only on a Unix seat")
}
