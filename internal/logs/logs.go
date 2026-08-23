// Package logs keeps a bounded, in-memory tail of recent log records so the
// daemon can answer "what just happened?" without writing an unbounded file.
//
// Two rules shape it, both from PHILOSOPHY.md:
//   - Nothing is immortal. The ring has a fixed capacity; the oldest record is
//     dropped, never a file that grows until a disk fills.
//   - The log must not leak what the ledger protects. Agent tokens, secrets, and
//     message bodies are encrypted at rest; a debug log that printed them would
//     quietly undo that. Sensitive attributes are redacted at capture time, not
//     at display time: there is no copy holding the real value.
package logs

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Record is one captured log line, already redacted.
type Record struct {
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// Ring is a fixed-capacity circular buffer of records, safe for concurrent use.
type Ring struct {
	mu   sync.RWMutex
	buf  []Record
	next int
	full bool
}

// NewRing returns a ring holding at most capacity records.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Ring{buf: make([]Record, capacity)}
}

func (r *Ring) add(rec Record) {
	r.mu.Lock()
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Tail returns up to n most recent records, oldest first. n<=0 returns all held.
func (r *Ring) Tail(n int) []Record {
	r.mu.RLock()
	defer r.mu.RUnlock()
	size := r.next
	if r.full {
		size = len(r.buf)
	}
	if n <= 0 || n > size {
		n = size
	}
	out := make([]Record, 0, n)
	start := r.next - n
	if start < 0 {
		start += len(r.buf)
	}
	for i := 0; i < n; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

// redactKeys are attribute names whose values must never reach the log. Matched
// case-insensitively as substrings, so "token", "lane_token", and "NewToken"
// are all covered.
//
// "output" and "transcript" are here because a wake command IS an agent turn:
// whatever it prints can contain decrypted mail, a tool's credentials, or a
// model's summary of a private message. Bounding the quantity to a few kilobytes
// bounds nothing about what is in them.
var redactKeys = []string{
	"token", "secret", "password", "nonce", "body", "response", "key", "cookie",
	"authorization", "output", "transcript",
}

func sensitive(key string) bool {
	k := strings.ToLower(key)
	for _, r := range redactKeys {
		if strings.Contains(k, r) {
			return true
		}
	}
	return false
}

// Handler is a slog.Handler that captures into a Ring and forwards to a base
// handler, so the terminal output and the in-memory tail never diverge.
type Handler struct {
	base  slog.Handler
	ring  *Ring
	attrs []slog.Attr
	group string
}

// NewHandler wraps base, capturing every record into ring.
func NewHandler(base slog.Handler, ring *Ring) *Handler {
	return &Handler{base: base, ring: ring}
}

// Enabled reports whether the base handler wants this level.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.base.Enabled(ctx, l) }

// Handle captures a redacted copy into the ring, then forwards to the base.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	rec := Record{Time: r.Time, Level: r.Level.String(), Msg: r.Message, Attrs: map[string]any{}}
	for _, a := range h.attrs {
		putAttr(rec.Attrs, a)
	}
	r.Attrs(func(a slog.Attr) bool { putAttr(rec.Attrs, a); return true })
	if len(rec.Attrs) == 0 {
		rec.Attrs = nil
	}
	h.ring.add(rec)
	// AND THE BASE HANDLER GETS THE REDACTED ONE TOO.
	//
	// This forwarded the ORIGINAL record, so every value the list above calls
	// sensitive was hidden from `/api/logs` and printed in full on stderr,
	// which is where an operator's service manager collects it, where a crash
	// reporter picks it up, and where anything reading the daemon's output can
	// see it. The redaction list read as a guarantee about logging and was a
	// guarantee about one of its two destinations.
	return h.base.Handle(ctx, redacted(r))
}

// redacted returns a copy of r whose sensitive attributes are replaced.
//
// A copy, because slog.Record shares its backing array with the caller: editing
// in place would mutate a record another handler may already hold.
func redacted(r slog.Record) slog.Record {
	var hit bool
	r.Attrs(func(a slog.Attr) bool {
		if sensitive(a.Key) {
			hit = true
			return false
		}
		return true
	})
	if !hit {
		return r
	}
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if sensitive(a.Key) {
			out.AddAttrs(slog.String(a.Key, "[redacted]"))
		} else {
			out.AddAttrs(a)
		}
		return true
	})
	return out
}

func putAttr(m map[string]any, a slog.Attr) {
	if sensitive(a.Key) {
		m[a.Key] = "[redacted]"
		return
	}
	m[a.Key] = a.Value.Resolve().Any()
}

// WithAttrs returns a handler carrying additional attributes.
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.base = h.base.WithAttrs(as)
	n.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	return &n
}

// WithGroup returns a handler nested under a group name.
func (h *Handler) WithGroup(name string) slog.Handler {
	n := *h
	n.base = h.base.WithGroup(name)
	n.group = name
	return &n
}
