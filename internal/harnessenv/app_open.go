package harnessenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// A failed ownership probe must cost at most one background open per ten
// minutes, not one forever and not one per message. A new app incarnation or
// an observed loaded -> unloaded transition re-arms sooner, but the per-thread
// rate limit still applies. This is derived local delivery state, not the ledger.
const (
	appOpenExpiry = 10 * time.Minute
	appOpenRate   = 20 * time.Second
)

type appOpenMemo struct {
	OpenedAt time.Time `json:"opened_at"`
	Epoch    string    `json:"epoch"`
	Loaded   bool      `json:"loaded"`
}

func isChatGPTOpen(argv []string) bool {
	return len(argv) > 1 && argv[0] == "/usr/bin/open" &&
		strings.HasPrefix(argv[len(argv)-1], "codex://threads/")
}

func (s Shower) ownership(thread string) ThreadOwnership {
	if s.Holds != nil {
		return ThreadOwnership{Known: true, Loaded: s.Holds(thread)}
	}
	if s.Ownership != nil {
		return s.Ownership(thread)
	}
	return ThreadOwnership{}
}

// showChatGPT is shared by the local daemon and the remote host bridge. The
// OS lock and memo survive overlapping producers and process replacement;
// losing this view can allow one extra open, never lose coordination mail.
// The attempt is saved BEFORE opening, including failure, so a hung command
// or failing ownership probe cannot recreate #304/7428955's repeated switching.
func (s Shower) showChatGPT(_ []string, thread string) (bool, error) {
	argv := chatGPTWakeArgv(thread)
	if argv == nil {
		return false, nil
	}
	key := sha256.Sum256([]byte(thread))
	path := filepath.Join(paths.DataDir(), "app-opens", hex.EncodeToString(key[:])+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	// #nosec G304 -- fixed private data directory plus hashed thread, no supplied path.
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	if err := paths.LockExclusive(f, false); err != nil {
		if paths.LockHeldElsewhere(err) {
			return false, nil // the other producer owns this attempt
		}
		return false, err
	}
	defer paths.Unlock(f)
	memo, err := readAppOpen(path)
	if err != nil {
		return false, err
	}
	state := s.ownership(thread)
	if state.Loaded {
		memo.Loaded = true
		if state.Epoch != "" {
			memo.Epoch = state.Epoch
		}
		return false, saveAppOpen(path, memo)
	}
	now := time.Now().UTC()
	if memo.suppresses(state, now) {
		return false, nil
	}
	memo.OpenedAt, memo.Loaded = now, false
	if state.Epoch != "" {
		memo.Epoch = state.Epoch
	}
	if err := saveAppOpen(path, memo); err != nil {
		return false, err // never open without recording the bound
	}
	if err := s.Open(argv); err != nil {
		return false, err
	}
	return true, nil
}

func (m appOpenMemo) suppresses(state ThreadOwnership, now time.Time) bool {
	if m.OpenedAt.IsZero() {
		return false
	}
	elapsed := now.Sub(m.OpenedAt)
	if elapsed < appOpenRate {
		return true
	}
	if elapsed >= appOpenExpiry {
		return false
	}
	newEpoch := state.Epoch != "" && m.Epoch != "" && state.Epoch != m.Epoch
	return !newEpoch && !(state.Known && m.Loaded)
}

func readAppOpen(path string) (appOpenMemo, error) {
	var memo appOpenMemo
	// #nosec G304 -- same derived path, bounded read.
	previous, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return memo, nil
	}
	if err != nil {
		return memo, err
	}
	defer func() { _ = previous.Close() }()
	raw, err := io.ReadAll(io.LimitReader(previous, 4096))
	if err != nil {
		return memo, err // I/O failure is not evidence of corrupt content
	}
	if err := json.Unmarshal(raw, &memo); err == nil {
		return memo, nil
	}
	// A corrupt derived view must not strand every future wake. Conservatively
	// retain a fresh attempt now and repair it; this wake cannot open, and the
	// normal expiry permits a later one. Still refuse if the repair cannot save.
	memo = appOpenMemo{OpenedAt: time.Now().UTC()}
	if err := saveAppOpen(path, memo); err != nil {
		return memo, err
	}
	slog.Warn("rebuilt corrupt app-open memo; this wake will not reopen the thread", "path", path)
	return memo, nil
}

func saveAppOpen(path string, memo appOpenMemo) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".open-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = json.NewEncoder(f).Encode(memo); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
