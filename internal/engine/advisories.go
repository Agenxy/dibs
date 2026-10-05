package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Advisory is standing configuration advice, rather than a fault worth
// reporting again after every restart. Key identifies the local subject;
// Revision identifies the configuration it was measured under, not the corpus
// or daemon build. Neither contains credentials. Delivery is ordinary mail.
type Advisory struct {
	Key, Revision string
	What, Remedy  string
}

type advisoryState struct {
	mu       sync.Mutex
	file     string
	seen     map[string]string
	pending  map[string]Advisory
	batches  int
	flushing bool
	readyAt  time.Time // coalesce lazy discoveries through one quiet second
}

// SetAdvisoryFile loads the derived memory from the daemon's selected data
// directory, once before Run. Losing it can repeat advice, never lose mail or
// coordination state. Empty keeps the memory for this run only.
func (e *Engine) SetAdvisoryFile(path string) {
	e.advisories.file = path
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- daemon-owned data directory
	if err != nil {
		return
	}
	var saved map[string]string
	if err := json.Unmarshal(raw, &saved); err != nil {
		slog.Warn("could not read advisory memory; standing advice may repeat", "err", err)
		return
	}
	e.advisories.seen = saved
}

// BeginAdvisoryBatch holds delivery until all related discovery work finishes.
// Indexing stays asynchronous; only advice waits. The returned finish function
// is safe to call once or more, and no lock is held across engine queries.
func (e *Engine) BeginAdvisoryBatch() func() {
	e.advisories.mu.Lock()
	e.advisories.batches++
	e.advisories.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.advisories.mu.Lock()
			e.advisories.batches--
			e.advisories.mu.Unlock()
		})
	}
}

// QueueAdvisory coalesces advice from startup and later lazy indexes. An
// unchanged subject is silent; changed configuration remains pending until
// ordinary mail commits. The next sweep batches everything currently ready.
func (e *Engine) QueueAdvisory(a Advisory) {
	if a.Key == "" || a.Revision == "" || a.What == "" || a.Remedy == "" {
		slog.Warn("incomplete standing advisory suppressed")
		return
	}
	e.advisories.mu.Lock()
	defer e.advisories.mu.Unlock()
	if e.advisories.seen[a.Key] == a.Revision {
		return
	}
	if e.advisories.pending == nil {
		e.advisories.pending = map[string]Advisory{}
	}
	e.advisories.pending[a.Key] = a
	e.advisories.readyAt = time.Now().Add(time.Second)
}

// On the writer loop: take a snapshot and start the off-loop sender.
// It never queries the writer or does I/O, including through this mutex.
func (e *Engine) flushAdvisories() {
	e.advisories.mu.Lock()
	if e.advisories.flushing || e.advisories.batches != 0 || len(e.advisories.pending) == 0 ||
		time.Now().Before(e.advisories.readyAt) {
		e.advisories.mu.Unlock()
		return
	}
	batch := make([]Advisory, 0, len(e.advisories.pending))
	for _, a := range e.advisories.pending {
		batch = append(batch, a)
	}
	e.advisories.flushing = true
	e.advisories.mu.Unlock()
	go e.deliverAdvisories(batch)
}

func (e *Engine) deliverAdvisories(batch []Advisory) {
	defer func() {
		e.advisories.mu.Lock()
		e.advisories.flushing = false
		e.advisories.mu.Unlock()
	}()
	to := e.coordinatorOrHuman()
	if to == "" {
		return // keep pending: nobody has received it yet
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	from, token, err := e.dibsAgent(ctx)
	if err != nil || from == to {
		return
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: to,
		MsgType: core.MsgNotify, Body: advisoryBody(batch)}); err != nil {
		slog.Warn("could not deliver standing advice; retrying", "err", err)
		return
	}
	// Only a successful ledgered send spends these revisions. Advice arriving
	// during delivery stays pending, including a newer revision of this subject.
	e.advisories.mu.Lock()
	if e.advisories.seen == nil {
		e.advisories.seen = map[string]string{}
	}
	for _, a := range batch {
		e.advisories.seen[a.Key] = a.Revision
		if pending := e.advisories.pending[a.Key]; pending.Revision == a.Revision {
			delete(e.advisories.pending, a.Key)
		}
	}
	raw, err := json.Marshal(e.advisories.seen)
	e.advisories.mu.Unlock()
	// The sender is serialized by flushing. No disk I/O holds the mutex the
	// writer takes on its tick; a slow filesystem cannot freeze the board.
	if err == nil {
		e.saveAdvisories(raw)
	}
}

func (e *Engine) saveAdvisories(raw []byte) {
	if e.advisories.file == "" {
		return
	}
	tmp := e.advisories.file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil { // #nosec G304 -- daemon-owned data directory
		if err := os.Rename(tmp, e.advisories.file); err == nil {
			return
		}
	}
	slog.Warn("could not save advisory memory; standing advice may repeat after restart")
}

func advisoryBody(batch []Advisory) string {
	sort.Slice(batch, func(i, j int) bool { return batch[i].What < batch[j].What })
	var b strings.Builder
	b.WriteString("Dibs configuration advice:\n")
	remedies := map[string]bool{}
	for _, a := range batch {
		b.WriteString("\n- " + a.What)
	}
	for _, a := range batch {
		if !remedies[a.Remedy] {
			b.WriteString("\n\n" + a.Remedy)
			remedies[a.Remedy] = true
		}
	}
	b.WriteString("\n\nThis is standing configuration advice. Unchanged repositories and daemon restarts " +
		"do not resend it; changing the scorer or its thresholds makes it relevant again.")
	return b.String()
}
