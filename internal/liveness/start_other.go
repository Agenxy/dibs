// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package liveness

// StartMillis is unavailable here. Without a start time a live pid cannot be
// told from a recycled one, so the sweep keeps judging silence by the clock.
func (p *Poller) StartMillis(int) (int64, bool) { return 0, false }
