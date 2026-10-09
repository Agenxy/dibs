// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import "fmt"

type contactAlertDiagnostic struct {
	Recipient       string `json:"recipient"`
	DeliveryFailure string `json:"delivery_failure"`
	RetryExhausted  bool   `json:"retry_exhausted"`
}

func checkContactDeliveryFailures(b *boardView, warn fixFn) {
	for _, c := range b.ContactAlerts {
		if c.RetryExhausted {
			warn(fmt.Sprintf("human notification for %s failed twice: %s", c.Recipient, c.DeliveryFailure),
				"read the board's contact alert; enable desktop notifications or attach dibs human-relay. "+
					"Dibs will not retry on a timer again.")
		}
	}
}
