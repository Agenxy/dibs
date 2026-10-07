// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory

// Fixed segments bound allocation slack to one segment instead of keeping
// spare capacity proportional to the ledger's growing anchor count.
const anchorSegment = 128

type anchorVector struct {
	segments []*[anchorSegment]Anchor
	n        int
}

func (v *anchorVector) add(a Anchor) {
	if v.n%anchorSegment == 0 {
		v.segments = append(v.segments, &[anchorSegment]Anchor{})
	}
	v.segments[v.n/anchorSegment][v.n%anchorSegment] = a
	v.n++
}

func (v *anchorVector) get(n int) Anchor {
	return v.segments[n/anchorSegment][n%anchorSegment]
}
