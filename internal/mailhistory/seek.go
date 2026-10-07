package mailhistory

import "sort"

// Bound ordinary sparse content intervals by native bytes as well as records.
// A single oversized legacy record can still be refused by the content port.
const anchorBytes = 4 << 20

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
	n := sort.Search(len(i.anchors), func(n int) bool { return i.anchors[n].Serial > serial })
	if n == 0 || serial > head.Serial {
		return SeekRange{}, ErrUnavailable
	}
	r := SeekRange{Start: i.anchors[n-1], End: head.End, Hash: head.Hash}
	if n < len(i.anchors) && i.anchors[n].Offset <= head.End {
		r.End, r.Hash = i.anchors[n].Offset, i.anchors[n].Prev
	}
	if r.End <= r.Start.Offset {
		return SeekRange{}, ErrUnavailable
	}
	return r, nil
}
