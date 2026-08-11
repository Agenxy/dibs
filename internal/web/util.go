package web

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/agenxy/dibs/internal/core"
)

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// eventLog keeps a small ring of recent events for the UI (SSE-fed, so it
// lives here rather than in the engine; a page reload re-primes from it).
type eventLog struct {
	mu   sync.Mutex
	ring []core.Event
	cap  int
}

func newEventLog(capacity int) *eventLog { return &eventLog{cap: capacity} }

func (l *eventLog) add(ev core.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ring) > 0 {
		last := l.ring[len(l.ring)-1]
		if last.Serial == ev.Serial && last.Sub == ev.Sub {
			return // dedupe across multiple SSE clients
		}
	}
	l.ring = append(l.ring, ev)
	if len(l.ring) > l.cap {
		l.ring = l.ring[len(l.ring)-l.cap:]
	}
}

func (l *eventLog) recent(n int) []core.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ring) < n {
		n = len(l.ring)
	}
	out := make([]core.Event, n)
	for i := 0; i < n; i++ {
		out[i] = l.ring[len(l.ring)-1-i] // newest first
	}
	return out
}
