package harnessenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
const appOpenExpiry = 10 * time.Minute
const appOpenRate = 20 * time.Second

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
	argv := ChatGPTOpenArgv(thread)
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
	var memo appOpenMemo
	// #nosec G304 -- same derived path, bounded decode; a corrupt view fails closed.
	previous, err := os.Open(path)
	if err == nil {
		err = json.NewDecoder(io.LimitReader(previous, 4096)).Decode(&memo)
		_ = previous.Close()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
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
	elapsed := now.Sub(memo.OpenedAt)
	newEpoch := state.Epoch != "" && memo.Epoch != "" && state.Epoch != memo.Epoch
	unloaded := state.Known && memo.Loaded
	if !memo.OpenedAt.IsZero() && (elapsed < appOpenRate ||
		(elapsed < appOpenExpiry && !newEpoch && !unloaded)) {
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
