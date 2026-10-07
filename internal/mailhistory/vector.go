package mailhistory

// Every reference retains uint64 range. Most chunks need only their low words;
// a high-word plane is allocated on the first value above 32 bits in a chunk.
// Fixed chunks charge allocation slack without doubling a growing vector.
const vectorChunk = 4096

type referenceChunk struct {
	low  [vectorChunk]uint32
	high *[vectorChunk]uint32
}

type vector struct {
	chunks []*referenceChunk
	n      int
}

func (v *vector) add(value uint64) {
	if v.n%vectorChunk == 0 {
		v.chunks = append(v.chunks, &referenceChunk{})
	}
	v.set(v.n, value)
	v.n++
}

func (v *vector) get(n int) uint64 {
	c, offset := v.chunks[n/vectorChunk], n%vectorChunk
	value := uint64(c.low[offset])
	if c.high != nil {
		value |= uint64(c.high[offset]) << 32
	}
	return value
}

func (v *vector) set(n int, value uint64) {
	c, offset := v.chunks[n/vectorChunk], n%vectorChunk
	c.low[offset] = uint32(value & 0xffffffff) // #nosec G115 -- explicitly masked low word
	if value>>32 != 0 && c.high == nil {
		c.high = &[vectorChunk]uint32{}
	}
	if c.high != nil {
		c.high[offset] = uint32(value >> 32) // #nosec G115 -- upper 32 bits of a uint64
	}
}

type partyKey struct {
	ID      string
	Created uint64
}

// A prefix points only to a base vector, never another party or prefix. The
// captured length excludes later mail to its previous owner.
type prefix struct {
	vector *vector
	length int
}

type party struct {
	refs        vector
	inherited   []prefix
	unavailable bool
}

const maxSources = 256

func (p *party) inherit(source *party) bool {
	if p.unavailable {
		return true
	}
	if source != nil && source.unavailable {
		p.unavailable, p.inherited = true, nil
		return true
	}
	if source == nil || p == source {
		return true
	}
	if !p.include(prefix{&source.refs, source.refs.n}) {
		return false
	}
	for _, prior := range source.inherited {
		if !p.include(prior) {
			return false
		}
	}
	return true
}

func (p *party) include(ref prefix) bool {
	if ref.length == 0 || ref.vector == &p.refs {
		return true
	}
	for n, old := range p.inherited {
		if old.vector == ref.vector {
			p.inherited[n].length = max(old.length, ref.length)
			return true
		}
	}
	if len(p.inherited) >= maxSources-1 {
		p.unavailable, p.inherited = true, nil
		return false // refuse this party only; the rest of the board stays usable
	}
	p.inherited = append(p.inherited, ref)
	return true
}
