// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "fmt"

func checkReconnectPending(b *boardView, warn fixFn) {
	if b.ReconnectPending > 0 {
		warn(fmt.Sprintf("app reconnect recovery has %d pending bridge observations", b.ReconnectPending), b.ReconnectAdvice)
	}
}
