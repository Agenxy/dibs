package mailhistory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// CandidateLimit bounds each page's own-party metadata examination.
const CandidateLimit = 4096

// Query refusal errors distinguish initial warming, ownership settling,
// invalid cursors and unavailable evidence without disclosing another party.
var (
	// ErrWarming means the initial validated boot prefix is incomplete.
	ErrWarming = errors.New("initial history build has not reached S0")
	// ErrSettling refuses authority that could predate an ownership move.
	ErrSettling = errors.New("history authority is settling")
	// ErrCursor refuses a cursor without confirming another party's units.
	ErrCursor = errors.New("invalid history cursor")
	// ErrUnavailable refuses unavailable or invalid derived evidence.
	ErrUnavailable = errors.New("history view is unavailable")
)

// A reference is an accelerator, not authority. Its canonical position is
// verified on every page; generation and version pin the reproducible format.
type cursor struct {
	Version    int      `json:"v"`
	Generation string   `json:"g"`
	Upper      uint64   `json:"u"`
	After      position `json:"p"`
	Reference  uint64   `json:"r"`
}

// PageRequest is an authenticated immutable incarnation and a work budget.
type PageRequest struct {
	Reader                        core.Agent
	Upper, Since, OwnershipChange uint64
	Limit                         int
	Cursor                        string
	Deadline                      time.Time
}

// Unit is one canonical projected audit transition, never authored content.
type Unit = snapshotUnit

// Page contains bounded authorized metadata and stateless continuation points.
type Page struct {
	Units        []Unit
	Cursors      []string // cursor after each returned unit, for encoded-byte bounds
	Next         string
	AsOf, Behind uint64
	Examined     int
}

// ReadPage copies bounded references and decodes outside capture/view locks.
func (i *Index) ReadPage(ctx context.Context, req PageRequest) (Page, error) {
	if req.Limit < 1 || req.Limit > 100 {
		return Page{}, ErrCursor
	}
	status, err := i.awaitPrefix(ctx, req)
	if err != nil {
		return Page{}, err
	}
	reader := unitReader{index: i}
	upper, lower, err := i.pageBounds(&reader, req, status)
	if err != nil {
		return Page{}, err
	}
	return i.walkPage(ctx, req, status, &reader, upper, lower)
}

func (i *Index) pageBounds(reader *unitReader, req PageRequest, status Status) (uint64, uint64, error) {
	upper, lower := req.Upper, uint64(0)
	if req.Cursor != "" {
		c, err := readCursor(req.Cursor)
		if err != nil || c.Generation != status.Generation || c.Upper > req.Upper {
			return 0, 0, ErrCursor
		}
		if !i.partyReference(partyKey{req.Reader.ID, req.Reader.CreatedSerial}, c.Reference) {
			return 0, 0, ErrCursor
		}
		u, err := reader.get(c.Reference)
		if err != nil || u.Position != c.After {
			return 0, 0, ErrCursor
		}
		upper, lower = c.Upper, c.Reference+1
	} else {
		var err error
		lower, err = i.firstAfter(req.Since)
		if err != nil {
			return 0, 0, err
		}
	}
	return upper, lower, nil
}

func (i *Index) walkPage(ctx context.Context, req PageRequest, status Status, reader *unitReader,
	upper, lower uint64,
) (Page, error) {
	page := Page{Units: make([]Unit, 0, req.Limit), AsOf: min(upper, status.Drained.Serial)}
	if req.Upper > status.Drained.Serial {
		page.Behind = req.Upper - status.Drained.Serial
	}
	refs, more := i.candidates(partyKey{req.Reader.ID, req.Reader.CreatedSerial}, lower)
	var last cursor
	for n, ref := range refs {
		if err := ctx.Err(); err != nil {
			return Page{}, err
		}
		if page.Examined > 0 && time.Now().After(req.Deadline) {
			more = true
			break
		}
		u, err := reader.get(ref)
		if err != nil {
			return Page{}, err
		}
		if u.Position.Op > page.AsOf {
			more = status.Drained.Serial < upper
			break
		}
		page.Examined++
		last = cursor{1, status.Generation, upper, u.Position, ref}
		allowed, err := i.authorizedUnit(reader, u, &req.Reader)
		if err != nil {
			return Page{}, err
		}
		if allowed {
			page.Units = append(page.Units, u)
			page.Cursors = append(page.Cursors, last.encode())
		}
		if len(page.Units) == req.Limit {
			more = more || n+1 < len(refs)
			break
		}
	}
	if more && page.Examined > 0 {
		page.Next = last.encode()
	}
	return page, nil
}

func (i *Index) authorizedUnit(reader *unitReader, u snapshotUnit, agent *core.Agent) (bool, error) {
	latest, err := i.latestUnit(reader, u.Position.Msg)
	if err != nil {
		return false, err
	}
	m := core.Message{
		From: latest.Metadata.From, To: latest.Metadata.To,
		AdoptedFrom: latest.Metadata.AdoptedFrom, AdoptedAt: latest.Metadata.AdoptedAt,
	}
	allowed, _ := core.MessageAccess(u.Position.Msg, &m, agent)
	return allowed, nil
}

func (i *Index) partyReference(key partyKey, ref uint64) bool {
	i.viewMu.RLock()
	defer i.viewMu.RUnlock()
	p := i.parties[key]
	if p == nil {
		return false
	}
	sources := append([]prefix{{&p.refs, p.refs.n}}, p.inherited...)
	for _, s := range sources {
		n := sort.Search(s.length, func(n int) bool { return s.vector.get(n) >= ref })
		if n < s.length && s.vector.get(n) == ref {
			return true
		}
	}
	return false
}

func (c cursor) encode() string {
	// This fixed struct contains only primitive strings and unsigned integers;
	// encoding/json cannot fail for any of its values.
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func readCursor(value string) (cursor, error) {
	var c cursor
	if len(value) > 512 {
		return c, ErrCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return c, ErrCursor
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Version != 1 {
		return c, ErrCursor
	}
	return c, nil
}

func (i *Index) awaitPrefix(ctx context.Context, req PageRequest) (Status, error) {
	for {
		s := i.Status()
		if s.Failed {
			return s, ErrUnavailable
		}
		if !s.InitialReady {
			return s, ErrWarming
		}
		if s.OwnershipChange != req.OwnershipChange {
			return s, ErrSettling
		}
		if s.Drained.Serial >= req.Upper || time.Now().After(req.Deadline) {
			if s.Drained.Serial < req.OwnershipChange {
				return s, ErrSettling
			}
			return s, nil
		}
		timer := time.NewTimer(min(time.Millisecond, time.Until(req.Deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return s, ctx.Err()
		case <-timer.C:
		}
	}
}

func (i *Index) candidates(key partyKey, lower uint64) ([]uint64, bool) {
	i.viewMu.RLock()
	defer i.viewMu.RUnlock()
	p := i.parties[key]
	if p == nil {
		return nil, false
	}
	sources := make([]prefix, 1, len(p.inherited)+1)
	sources[0] = prefix{&p.refs, p.refs.n}
	sources = append(sources, p.inherited...)
	positions := make([]int, len(sources))
	for n, s := range sources {
		positions[n] = sort.Search(s.length, func(j int) bool { return s.vector.get(j) >= lower })
	}
	refs := make([]uint64, 0, CandidateLimit)
	for len(refs) < CandidateLimit {
		ref, found := minimumReference(sources, positions)
		if !found {
			return refs, false
		}
		refs = append(refs, ref)
		advanceReferences(sources, positions, ref)
	}
	for n, s := range sources {
		if positions[n] < s.length {
			return refs, true
		}
	}
	return refs, false
}

func minimumReference(sources []prefix, positions []int) (uint64, bool) {
	var ref uint64
	found := false
	for n, s := range sources {
		if positions[n] < s.length {
			r := s.vector.get(positions[n])
			if !found || r < ref {
				ref, found = r, true
			}
		}
	}
	return ref, found
}

func advanceReferences(sources []prefix, positions []int, ref uint64) {
	for n, s := range sources {
		if positions[n] < s.length && s.vector.get(positions[n]) == ref {
			positions[n]++
		}
	}
}

type span struct {
	first, count uint64
	raw          int
	data         []byte
	compressed   bool
}

func (i *Index) span(ref uint64) (span, error) {
	i.viewMu.RLock()
	defer i.viewMu.RUnlock()
	if ref >= i.codec.units {
		return span{}, ErrUnavailable
	}
	n := sort.Search(len(i.codec.blocks), func(n int) bool {
		return i.codec.blocks[n].first+i.codec.blocks[n].count > ref
	})
	if n < len(i.codec.blocks) {
		b := i.codec.blocks[n]
		return span{b.first, b.count, b.raw, b.data, true}, nil
	}
	return span{i.codec.units - i.codec.count, i.codec.count, len(i.codec.tail), bytes.Clone(i.codec.tail), false}, nil
}

type (
	decodedSpan struct {
		first uint64
		units []snapshotUnit
	}
	unitReader struct {
		index *Index
		cache [2]decodedSpan
		next  int
	}
)

func (r *unitReader) get(ref uint64) (snapshotUnit, error) {
	for _, c := range r.cache {
		if ref >= c.first && ref-c.first < uint64(len(c.units)) {
			return c.units[ref-c.first], nil
		}
	}
	s, err := r.index.span(ref)
	if err != nil {
		return snapshotUnit{}, err
	}
	var units []snapshotUnit
	if s.compressed {
		units, err = decode(s.data, s.raw, s.count)
	} else {
		units, err = decodeRaw(s.data, s.count)
	}
	if err != nil || ref < s.first || ref-s.first >= uint64(len(units)) {
		return snapshotUnit{}, ErrUnavailable
	}
	r.cache[r.next] = decodedSpan{s.first, units}
	r.next = (r.next + 1) % len(r.cache)
	return units[ref-s.first], nil
}

func (i *Index) latestUnit(reader *unitReader, serial uint64) (snapshotUnit, error) {
	i.viewMu.RLock()
	n := sort.Search(i.serials.n, func(n int) bool { return i.serials.get(n) >= serial })
	if n == i.serials.n || i.serials.get(n) != serial {
		i.viewMu.RUnlock()
		return snapshotUnit{}, ErrUnavailable
	}
	ref := i.latest.get(n)
	i.viewMu.RUnlock()
	return reader.get(ref)
}

func (i *Index) firstAfter(serial uint64) (uint64, error) {
	i.viewMu.RLock()
	n := sort.Search(len(i.codec.blocks), func(n int) bool { return i.codec.blocks[n].lastOp > serial })
	ref := i.codec.units - i.codec.count
	if n < len(i.codec.blocks) {
		ref = i.codec.blocks[n].first
	}
	units := i.codec.units
	i.viewMu.RUnlock()
	if ref == units {
		return ref, nil
	}
	reader := unitReader{index: i}
	for ref < units {
		u, err := reader.get(ref)
		if err != nil {
			return 0, err
		}
		if u.Position.Op > serial {
			return ref, nil
		}
		ref++
	}
	return ref, nil
}
