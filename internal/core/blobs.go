package core

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// Attachments & blob registry (SPEC-ATTACHMENTS A3–A6). The registry holds
// metadata only: bytes live in the side blob store (internal/blobstore),
// outside the replay model. Every mutation here is a pure function of ledgered
// ops, so which blobs *should* exist replays exactly; the bytes are reconciled
// against this registry at boot.

// blobIDRe validates a content address before it is ever used to build a
// filesystem path (A3, fixes P2-3 traversal). Lowercase hex, fixed length.
var blobIDRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidBlobID reports whether id is a well-formed sha256 content address.
func ValidBlobID(id string) bool { return blobIDRe.MatchString(id) }

// mimeRe bounds attacker-influenced mime metadata that is echoed back into a
// recipient's context (A9/A10, fixes P2-6). type/subtype of RFC-6838 tokens.
var mimeRe = regexp.MustCompile(`^[a-zA-Z0-9!#$&^_.+-]{1,64}/[a-zA-Z0-9!#$&^_.+-]{1,64}$`)

// ValidMime reports whether m is an acceptable, bounded mime string.
func ValidMime(m string) bool { return len(m) <= 128 && mimeRe.MatchString(m) }

// Blob is a registry entry: metadata only, no bytes (A4).
type Blob struct {
	ID            string          `json:"id"`
	Size          int64           `json:"size"`
	Mime          string          `json:"mime,omitempty"`
	CreatedSerial uint64          `json:"created_serial"`
	CreatedAt     time.Time       `json:"created_at"`
	Owners        map[string]bool `json:"owners"`         // agents that have put this content (A6.1)
	Pins          map[string]bool `json:"pins,omitempty"` // reserved: no pin op in v1
}

// Attachment is one handle carried by a message (A2): a durable blob reference
// (Blob set) or an advisory zero-copy fileref (Path set). Never both.
type Attachment struct {
	Blob string `json:"blob,omitempty"` // "sha256:…" content address
	Path string `json:"path,omitempty"` // fileref: advisory local path (A2.1)
	Size int64  `json:"size,omitempty"` // sender-asserted (advisory for filerefs)
	Hash string `json:"hash,omitempty"` // fileref: sender-asserted, advisory
	Mime string `json:"mime,omitempty"`
}

// IsBlob distinguishes a durable blob handle from an advisory fileref.
func (a Attachment) IsBlob() bool { return a.Blob != "" }

// blobRefs is the pure refcount (A5): live (still-present, non-GC'd) messages
// attaching id, plus explicit pins. A message counts until retention GC deletes
// it from s.Messages, at which point its reference drops automatically.
func (s *State) blobRefs(id string) int {
	n := 0
	for _, m := range s.Messages {
		for _, a := range m.Attachments {
			if a.Blob == id {
				n++
			}
		}
	}
	if b := s.Blobs[id]; b != nil {
		n += len(b.Pins)
	}
	return n
}

// blobAccessible enforces A6: fetchable only by an owner agent or the recipient
// of a live message referencing it. Does not reveal existence to others.
func (s *State) blobAccessible(id, agent string) bool {
	b := s.Blobs[id]
	if b == nil {
		return false
	}
	if b.Owners[agent] {
		return true
	}
	for _, m := range s.Messages {
		if m.To != agent {
			continue
		}
		for _, a := range m.Attachments {
			if a.Blob == id {
				return true
			}
		}
	}
	return false
}

// BlobAccessible reports whether agent may fetch id (A6). Exported for the engine.
func (s *State) BlobAccessible(id, agent string) bool { return s.blobAccessible(id, agent) }

// BlobWasEvicted reports that agent holds a live message naming id, but the blob
// itself is gone: the store cap's last-resort pass drops referenced content
// rather than exceed the bound.
//
// This separates "you may not have it" from "it no longer exists", which are
// the same answer today and are not the same problem. Only ever consulted for a
// caller that already holds the reference, so it is not an existence oracle.
func (s *State) BlobWasEvicted(id, agent string) bool {
	if s.Blobs[id] != nil {
		return false
	}
	for _, m := range s.Messages {
		if m.To != agent && m.From != agent {
			continue
		}
		for _, a := range m.Attachments {
			if a.Blob == id {
				return true
			}
		}
	}
	return false
}

// laneBlobBytes sums the sizes of blobs an agent owns: the per-agent quota metric
// (A9, fixes P1-3). A blob shared by N owners counts against each; content the
// agent put is content it is accountable for.
func (s *State) laneBlobBytes(agent string) int64 {
	var total int64
	for _, b := range s.Blobs {
		if b.Owners[agent] {
			total += b.Size
		}
	}
	return total
}

// storeBytes is the total registry size: the global-cap metric (A9).
func (s *State) storeBytes() int64 {
	var total int64
	for _, b := range s.Blobs {
		total += b.Size
	}
	return total
}

// applyPutBlob registers newly-staged content, or grants the caller ownership
// of already-present content, with caller-scoped dedup (A6.1, fixes P1-1). The
// engine has already made the bytes durable off-thread (A4.1) before this op,
// so registration is bytes-free and pure.
func (s *State) applyPutBlob(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	if op.Blob == "" {
		return nil, nil, ErrNoID
	}
	if !ValidBlobID(op.Blob) {
		return nil, nil, ErrBadID
	}
	if op.Size < 0 || op.Size > int64(s.Limits.MaxBlobSize) {
		return nil, nil, errTooLarge("blob", s.Limits.MaxBlobSize)
	}
	if op.Mime != "" && !ValidMime(op.Mime) {
		return nil, nil, ErrBadMime
	}
	b := s.Blobs[op.Blob]

	// Caller already owns it → true dedup, no state change, no oracle leak.
	if b != nil && b.Owners[l.ID] {
		return Result{"blob": b.ID, "size": b.Size, "mime": b.Mime, "deduped": true}, nil, nil
	}

	// Per-agent quota: adding this content must not exceed the agent's budget.
	addl := op.Size
	if b != nil {
		addl = b.Size // canonical size wins over caller-claimed
	}
	if s.laneBlobBytes(l.ID)+addl > int64(s.Limits.PerLaneBlobBytes) {
		return nil, nil, ErrQuota
	}

	if b == nil {
		// Brand-new content. Reclaim space deterministically first (A5); the
		// eviction events ride this op's serial, so replay reproduces them.
		var evs []Event
		if s.storeBytes()+op.Size > int64(s.Limits.BlobStoreBytes) {
			evs = append(evs, s.gcBlobs(now, op.Size)...) // reclaim, protecting the incoming size
		}
		if s.storeBytes()+op.Size > int64(s.Limits.BlobStoreBytes) {
			return nil, nil, ErrStoreFull // nothing reclaimable
		}
		b = &Blob{
			ID: op.Blob, Size: op.Size, Mime: op.Mime, CreatedAt: now,
			Owners: map[string]bool{l.ID: true}, Pins: map[string]bool{},
		}
		s.Blobs[op.Blob] = b
		evs = append(evs, Event{Type: "blob.registered", Agent: l.ID, Data: map[string]any{
			"id": op.Blob, "size": op.Size, "mime": op.Mime,
		}})
		serial := s.finish(&evs, now)
		b.CreatedSerial = serial
		return Result{"blob": op.Blob, "size": op.Size, "mime": op.Mime, "deduped": false}, evs, nil
	}

	// Content exists for someone else; grant this caller ownership (they
	// supplied the plaintext, so this discloses nothing. A6.1).
	b.Owners[l.ID] = true
	return Result{"blob": b.ID, "size": b.Size, "mime": b.Mime, "deduped": false},
		[]Event{{Type: "blob.owner_added", Agent: l.ID, Data: map[string]any{"id": b.ID}}}, nil
}

// gcBlobs is the deterministic, pure blob eviction pass (A5). It runs inside
// the sweep (for TTL) and inside put_blob (to reclaim under cap pressure before
// registering). Every decision is a pure function of (registry, live messages,
// now), so replay reproduces it exactly: no recorded victims needed, unlike
// agent liveness (which depends on impure PID probes). Bytes are deleted from
// disk afterward by the engine's reconcile pass, which diffs files vs registry.
//
// Policy (deterministic; created-order, not access-order: true LRU would
// require ledgering reads, which we refuse to do for a read path):
//  1. unreferenced blobs older than the hard TTL → evicted (cause "ttl");
//  2. under cap pressure, unreferenced blobs past the grace window, oldest
//     first, until under cap (cause "cap"): grace protects freshly-put,
//     not-yet-attached blobs so put→send works;
//  3. still over cap → last-resort: referenced blobs oldest first (cause
//     "cap"), so a store full of referenced blobs degrades honestly instead of
//     dead-locking (fixes P1-3).
func (s *State) gcBlobs(now time.Time, reserve int64) []Event {
	var evs []Event
	drop := func(id, cause string) {
		delete(s.Blobs, id)
		evs = append(evs, Event{Type: "blob.evicted", Data: map[string]any{"id": id, "cause": cause}})
	}
	// Sorted: this loop emits blob.evicted, and the sweep's event stream is the
	// audit history. Map order differs per process, so an unsorted traversal
	// reordered those events on replay.
	for _, id := range sortedKeys(s.Blobs) {
		b := s.Blobs[id]
		if s.blobRefs(id) == 0 && now.Sub(b.CreatedAt) > s.Limits.BlobTTL {
			drop(id, "ttl")
		}
	}
	limit := int64(s.Limits.BlobStoreBytes) - reserve
	if s.storeBytes() <= limit {
		return evs
	}
	type cand struct {
		id      string
		created time.Time
	}
	collect := func(unrefOnly bool) []cand {
		var cs []cand
		for id, b := range s.Blobs {
			if unrefOnly && (s.blobRefs(id) != 0 || now.Sub(b.CreatedAt) <= s.Limits.BlobGraceWindow) {
				continue
			}
			cs = append(cs, cand{id, b.CreatedAt})
		}
		slices.SortFunc(cs, func(a, b cand) int {
			if a.created.Equal(b.created) {
				return strings.Compare(a.id, b.id) // stable tiebreak for determinism
			}
			return a.created.Compare(b.created)
		})
		return cs
	}
	for _, c := range collect(true) {
		if s.storeBytes() <= limit {
			break
		}
		drop(c.id, "cap")
	}
	for _, c := range collect(false) { // last resort: referenced too
		if s.storeBytes() <= limit {
			break
		}
		if _, still := s.Blobs[c.id]; still {
			drop(c.id, "cap")
		}
	}
	return evs
}

// validateAttachments checks a send's attachment list against A6/A9 and returns
// the normalized handles to store on the message. Blob handles are id-validated
// and access-checked; filerefs are bounded but never opened (A2.1).
func (s *State) validateAttachments(l *Agent, atts []Attachment) ([]Attachment, error) {
	if len(atts) > s.Limits.MaxAttachments {
		return nil, errTooLarge("attachments", s.Limits.MaxAttachments)
	}
	out := make([]Attachment, 0, len(atts))
	for _, a := range atts {
		switch {
		case a.IsBlob():
			if !ValidBlobID(a.Blob) {
				return nil, ErrBadID
			}
			if !s.blobAccessible(a.Blob, l.ID) {
				return nil, ErrNoBlob // does not reveal existence (A6)
			}
			b := s.Blobs[a.Blob]
			out = append(out, Attachment{Blob: a.Blob, Size: b.Size, Mime: b.Mime})
		case a.Path != "":
			if len(a.Path) > s.Limits.MaxPathBytes {
				return nil, errTooLarge("fileref path", s.Limits.MaxPathBytes)
			}
			if len(a.Hash) > s.Limits.MaxFilerefHash {
				return nil, errTooLarge("fileref hash", s.Limits.MaxFilerefHash)
			}
			if a.Mime != "" && !ValidMime(a.Mime) {
				return nil, ErrBadMime
			}
			// Recorded verbatim as sender-asserted advisory metadata; the
			// daemon never stats or hashes path (A2.1, fixes P0-2).
			out = append(out, Attachment{Path: a.Path, Size: a.Size, Hash: a.Hash, Mime: a.Mime})
		default:
			return nil, errf(
				"E_BAD_ATTACHMENT", "each attachment needs a blob id or a fileref path",
				"attachment has neither blob nor path",
			)
		}
	}
	return out, nil
}
