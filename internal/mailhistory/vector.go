package mailhistory

// References are uint64 in production: the design probe's uint32 references
// must never wrap on a long-lived ledger. Fixed chunks charge allocation slack
// without doubling a million-entry slice during growth.
const vectorChunk = 4096

type vector struct {
	chunks [][]uint64
	n      int
}

func (v *vector) add(value uint64) {
	if v.n%vectorChunk == 0 {
		v.chunks = append(v.chunks, make([]uint64, vectorChunk))
	}
	v.chunks[v.n/vectorChunk][v.n%vectorChunk] = value
	v.n++
}

func (v *vector) get(n int) uint64        { return v.chunks[n/vectorChunk][n%vectorChunk] }
func (v *vector) set(n int, value uint64) { v.chunks[n/vectorChunk][n%vectorChunk] = value }

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
	refs      vector
	inherited []prefix
}

const maxSources = 256

func (p *party) inherit(source *party) bool {
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
		return false // explicitly disable this derived view, never drop authority
	}
	p.inherited = append(p.inherited, ref)
	return true
}
