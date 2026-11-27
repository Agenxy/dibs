// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package antigravity

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchDesktop observes the existing app, never starts one. Descriptor changes
// are filesystem events; acceptance and mail are not retried on a timer.
func WatchDesktop(ctx context.Context, changed func(string)) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return watchHub(ctx, home, desktopEpoch, changed, nil)
}

func desktopEpoch() (string, bool) {
	hub, d, err := currentDesktopDescriptor()
	if err != nil {
		return "", false
	}
	// Credential replacement is also a new endpoint. Only its digest leaves
	// this adapter; the comparison never carries the credential itself.
	digest := sha256.Sum256([]byte(d.Token))
	return fmt.Sprintf("antigravity:%d:%d:%d:%x", hub.PID, hub.Start.UnixNano(), d.HTTPPort, digest), true
}

type hubDirectoryWatch struct {
	w               *fsnotify.Watcher
	target, watched string
	info            os.FileInfo
	reset           bool
}

func nearestHubDirectory(dir string) (string, os.FileInfo, error) {
	for {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return "", nil, errors.New("antigravity hub ancestor is not a directory")
			}
			return dir, info, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err // inaccessible is not missing: surface the watcher fault
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, errors.New("antigravity hub watch has no accessible ancestor")
		}
		dir = parent
	}
}

func newHubDirectoryWatch(home string) (*hubDirectoryWatch, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	h := &hubDirectoryWatch{w: w, target: filepath.Join(home, ".gemini", "antigravity", "daemon")}
	if err := h.attach(); err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}

func (h *hubDirectoryWatch) close() { _ = h.w.Close() }

func (h *hubDirectoryWatch) attach() error {
	dir, info, err := nearestHubDirectory(h.target)
	if err != nil {
		return err
	}
	if !h.reset && dir == h.watched && h.info != nil && os.SameFile(info, h.info) {
		return nil
	}
	if h.watched != "" {
		// kqueue can remove the renamed directory but retain child-file
		// watches. Rebuild that state too, not just the path, or subsequent
		// descriptor writes keep watching the old inode forever.
		fresh, err := fsnotify.NewWatcher()
		if err != nil {
			return err
		}
		h.close()
		h.w = fresh
	}
	if err := h.w.Add(dir); err != nil {
		return err
	}
	h.watched, h.info, h.reset = dir, info, false
	return nil
}

func (h *hubDirectoryWatch) changed(event fsnotify.Event) bool {
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
		return false
	}
	if h.watched != h.target && event.Name != h.watched && event.Name != h.target &&
		!strings.HasPrefix(h.target, event.Name+string(filepath.Separator)) {
		return false // unrelated files in a missing directory's ancestor are not hub events
	}
	if event.Name == h.watched && event.Op&(fsnotify.Rename|fsnotify.Remove) != 0 {
		h.reset = true
	}
	return true
}

func debounceHub(timer *time.Timer) *time.Timer {
	if timer == nil {
		return time.NewTimer(50 * time.Millisecond)
	}
	timer.Reset(50 * time.Millisecond)
	return timer
}

type hubObservation struct {
	previous string
	observe  func() (string, bool)
	changed  func(string)
}

func (o *hubObservation) refresh() {
	epoch, known := o.observe()
	if known && epoch != "" && epoch != o.previous {
		o.previous = epoch
		o.changed(epoch)
	}
}

func watchHub(
	ctx context.Context, home string, observe func() (string, bool), changed func(string), ready func(),
) error {
	h, err := newHubDirectoryWatch(home)
	if err != nil {
		return err
	}
	defer h.close()
	previous, _ := observe() // startup is a baseline, not a restart
	observation := hubObservation{previous: previous, observe: observe, changed: changed}
	if ready != nil {
		ready()
	}
	// Bounded event debounce lets an in-place write finish. Idle mail never
	// creates or re-arms this timer; only filesystem events can.
	var settle *time.Timer
	var settled <-chan time.Time
	defer func() {
		if settle != nil {
			settle.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-h.w.Events:
			if !ok {
				return errors.New("antigravity hub event source closed")
			}
			if !h.changed(event) {
				continue
			}
			settle = debounceHub(settle)
			settled = settle.C
		case err, ok := <-h.w.Errors:
			if !ok {
				return errors.New("antigravity hub error source closed")
			}
			return fmt.Errorf("antigravity hub observation failed: %w", err)
		case <-settled:
			settled = nil
			if err := h.attach(); err != nil {
				return err
			}
			observation.refresh()
		}
	}
}
