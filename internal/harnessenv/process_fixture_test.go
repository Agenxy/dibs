// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"strconv"
	"strings"
)

// Existing synthetic inventories keep their readable timestamps. Conversion
// lives only in fixtures; production native evidence is typed end to end.
func fixtureProcessPass(ctx context.Context, raw string, parents bool) *processPass {
	p := &processPass{ctx: ctx, rows: map[int]processIdentity{}, paths: map[int]processPath{}}
	if parents {
		p.rows = parseProcessInventory(raw)
		return p
	}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		p.rows[pid] = processIdentity{parent: 1, start: strings.Join(f[1:6], " "), path: strings.Join(f[6:], " ")}
	}
	return p
}
