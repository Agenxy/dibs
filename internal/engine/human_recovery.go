// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
)

const (
	humanRecoveryInterval = time.Second
	humanPostingLimit     = 4096
	humanPostingFileLimit = 256 << 10
)

type humanRecoveryEpoch struct {
	Node    string `json:"node"`
	Through uint64 `json:"through"`
}

type humanPostingFile struct {
	humanRecoveryEpoch
	Posted map[uint64]string `json:"posted"`
}

type humanPostingStore struct {
	mu   sync.Mutex
	path string
	file humanPostingFile
}

// SetHumanPostingStore loads the local projection after replay and before Run.
// The upgrade checkpoint is independent of the discardable posting receipts.
func (e *Engine) SetHumanPostingStore(dir string) error {
	epoch, err := loadHumanRecoveryEpoch(dir, e.state.NodeID, e.state.Serial)
	if err != nil {
		return fmt.Errorf("human notification recovery checkpoint: %w", err)
	}
	store := &humanPostingStore{
		path: filepath.Join(dir, "human-posting-receipts.json"),
		file: humanPostingFile{humanRecoveryEpoch: epoch, Posted: map[uint64]string{}},
	}
	var saved humanPostingFile
	err = readHumanPostingFile(store.path, &saved)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("could not read saved human notification receipts; unconfirmed asks may be retried", "err", err)
	}
	if err == nil && saved.humanRecoveryEpoch == epoch && len(saved.Posted) <= humanPostingLimit {
		human := e.humanIdentityLocked()
		e.humanDelivery.bySerial = map[uint64]humanDelivery{}
		for serial, route := range saved.Posted {
			m := e.state.Messages[serial]
			if m == nil || m.To != human || !humanAskType(m.Type) || serial <= epoch.Through ||
				(route != "desktop" && route != "relay") {
				continue
			}
			store.file.Posted[serial] = route
			e.humanDelivery.bySerial[serial] = humanDelivery{Posted: true, Route: route, State: "posted"}
		}
	}
	e.humanDelivery.posting = store
	return nil
}

func loadHumanRecoveryEpoch(dir, node string, serial uint64) (humanRecoveryEpoch, error) {
	path := filepath.Join(dir, "human-posting-epoch.json")
	var epoch humanRecoveryEpoch
	err := readHumanPostingFile(path, &epoch)
	if errors.Is(err, os.ErrNotExist) || (err == nil && (epoch.Node != node || epoch.Through > serial)) {
		epoch = humanRecoveryEpoch{Node: node, Through: serial}
		err = writeHumanPostingFile(path, epoch)
	}
	return epoch, err
}

func readHumanPostingFile(path string, out any) error {
	f, err := os.Open(path) // #nosec G304 -- daemon-owned data directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, humanPostingFileLimit+1))
	if err != nil {
		return err
	}
	if len(buf) > humanPostingFileLimit {
		return fmt.Errorf("human posting data exceeds %d bytes", humanPostingFileLimit)
	}
	return json.Unmarshal(buf, out)
}

func writeHumanPostingFile(path string, value any) error {
	buf, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".human-posting-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // #nosec G703 -- the temporary file created above
	_, err = tmp.Write(buf)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) // #nosec G703 -- daemon-owned data directory
}

// Record only positive acceptance and the route. This runs off the writer.
func (e *Engine) saveHumanPosting(serial uint64, route string) {
	store := e.humanDelivery.posting
	if store == nil || serial <= store.file.Through {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, known := store.file.Posted[serial]; known {
		return
	}
	var droppedSerial uint64
	var droppedRoute string
	if len(store.file.Posted) >= humanPostingLimit {
		oldest := serial
		for key := range store.file.Posted {
			oldest = min(oldest, key)
		}
		if oldest == serial {
			return
		}
		droppedSerial, droppedRoute = oldest, store.file.Posted[oldest]
		delete(store.file.Posted, oldest)
	}
	store.file.Posted[serial] = route
	if err := writeHumanPostingFile(store.path, store.file); err != nil {
		delete(store.file.Posted, serial)
		if droppedSerial != 0 {
			store.file.Posted[droppedSerial] = droppedRoute
		}
		slog.Warn("could not save human notification receipt; the ask may be retried after a restart", "err", err)
	}
}

func humanAskType(kind string) bool {
	return kind == core.MsgQuestion || kind == core.MsgRequest
}

// On the writer. A board read that already established awareness needs no
// startup retry. Deadlines and current posting evidence are checked again after
// the off-loop native probe.
func (e *Engine) needsHumanRecovery(m *core.Message, now time.Time) bool {
	if m == nil || m.DeliveredAt != 0 || !humanAskType(m.Type) || m.To != e.humanIdentityLocked() {
		return false
	}
	pending, _ := nativeHumanPending(m, now)
	return pending && !e.deliveryForHuman(m).Posted
}

func (e *Engine) startHumanPostingRecovery(ctx context.Context, now time.Time) {
	store := e.humanDelivery.posting
	available, _ := e.localHumanNotifier()
	if store == nil || !available() {
		return
	}
	var serials []uint64
	for _, m := range e.state.MailTo(e.humanIdentityLocked()) {
		if m.Serial > store.file.Through && e.needsHumanRecovery(m, now) {
			serials = append(serials, m.Serial)
		}
	}
	if len(serials) > 0 {
		slices.Sort(serials)
		go e.recoverHumanPostings(ctx, store.file.Node, serials)
	}
}

func (e *Engine) recoverHumanPostings(ctx context.Context, node string, serials []uint64) {
	for i, serial := range serials {
		if i > 0 {
			timer := time.NewTimer(humanRecoveryInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if err := e.recoverHumanPosting(ctx, node, serial); err != nil {
			return
		}
	}
}

func (e *Engine) recoverHumanPosting(ctx context.Context, node string, serial uint64) error {
	present := e.humanPostingPresent(ctx, node, serial)
	recovered := false
	_, err := e.query(ctx, func() core.Result {
		m := e.state.Messages[serial]
		if !e.needsHumanRecovery(m, time.Now()) {
			return nil
		}
		if !present {
			e.dispatchHuman(core.Result{"msg_serial": serial})
			return nil
		}
		e.humanDelivery.mu.Lock()
		if e.humanDelivery.bySerial == nil {
			e.humanDelivery.bySerial = map[uint64]humanDelivery{}
		}
		e.humanDelivery.bySerial[serial] = humanDelivery{Posted: true, Route: "desktop", State: "posted"}
		e.humanDelivery.mu.Unlock()
		recovered = true
		return nil
	})
	if err == nil && recovered {
		e.saveHumanPosting(serial, "desktop")
	}
	return err
}

func (e *Engine) humanPostingPresent(ctx context.Context, node string, serial uint64) bool {
	e.humanDelivery.mu.Lock()
	n := e.humanDelivery.notifier
	e.humanDelivery.mu.Unlock()
	if n == nil {
		return notify.MessageDelivered(ctx, node, serial)
	}
	probe, ok := n.(interface {
		MessageDelivered(context.Context, string, uint64) bool
	})
	return ok && probe.MessageDelivered(ctx, node, serial)
}
