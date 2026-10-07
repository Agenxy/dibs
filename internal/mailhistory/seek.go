// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mailhistory

import "sort"

// Bound ordinary sparse content intervals by native bytes as well as records.
// A single oversized legacy record can still be refused by the content port.
const anchorBytes = 512 << 10

// SeekRange binds the entire sparse interval to the validated next anchor or
// drained head. Checking only links before the target would not authenticate
// the target's own bytes against committed evidence.
type SeekRange struct {
	Start Anchor
	End   int64
	Hash  [32]byte
}

// ContentRange copies a trusted sparse interval without decoding metadata.
func (i *Index) ContentRange(serial uint64, head Record) (SeekRange, error) {
	i.viewMu.RLock()
	defer i.viewMu.RUnlock()
	n := sort.Search(i.anchors.n, func(n int) bool { return i.anchors.get(n).Serial > serial })
	if n == 0 || serial > head.Serial {
		return SeekRange{}, ErrUnavailable
	}
	r := SeekRange{Start: i.anchors.get(n - 1), End: head.End, Hash: head.Hash}
	if n < i.anchors.n && i.anchors.get(n).Offset <= head.End {
		r.End, r.Hash = i.anchors.get(n).Offset, i.anchors.get(n).Prev
	}
	if r.End <= r.Start.Offset {
		return SeekRange{}, ErrUnavailable
	}
	return r, nil
}
