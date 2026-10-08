// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

const pendingWakeTTL = 2 * time.Hour

type queueReceipt struct {
	QueuedAt   time.Time `json:"queued_at"`
	PromptAt   time.Time `json:"prompt_at"`
	Generation string    `json:"generation,omitempty"`
}

func receiptPath(thread string) string {
	key := sha256.Sum256([]byte(thread))
	return filepath.Join(paths.DataDir(), "queued-wakes", hex.EncodeToString(key[:])+".json")
}

func readReceipt(thread string) queueReceipt {
	return readReceiptFile(receiptPath(thread))
}

func readReceiptFile(path string) queueReceipt {
	// #nosec G304 -- private callers derive the path from DataDir and a SHA-256
	// thread key; no participant-supplied path or traversal reaches this read.
	b, err := os.ReadFile(path)
	if err != nil {
		return queueReceipt{}
	}
	var r queueReceipt
	_ = json.Unmarshal(b, &r)
	return r
}

func writeReceipt(thread string, r queueReceipt) error {
	return writeReceiptFile(receiptPath(thread), r)
}

func writeReceiptFile(path string, r queueReceipt) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// NoteQueuePrompt records only the current session's start/prompt hook. MCP
// and tool traffic can occur while the app still holds our queued wake.
// On unavailable observation a human prompt may re-arm one duplicate; refusing
// to re-arm could lose a wake. A bounded duplicate is the chosen error direction.
func NoteQueuePrompt(thread string, at time.Time) {
	if thread == "" {
		return
	}
	// A separate atomic file avoids making a hook wait for a slow queue command,
	// and preserves a prompt that arrives before that command has exited.
	if err := writeReceiptFile(receiptPath(thread)+".prompt", queueReceipt{PromptAt: at.UTC()}); err != nil {
		slog.Warn("could not retain the queued wake's prompt receipt", "err", err,
			"hint", "restore write access to the board data directory")
	}
}

func fallbackPending(thread string, now time.Time) bool {
	r := readReceipt(thread)
	p := readReceiptFile(receiptPath(thread) + ".prompt")
	reconnect := readReceiptFile(receiptPath(thread) + ".reconnect")
	return !r.QueuedAt.IsZero() && now.Sub(r.QueuedAt) < pendingWakeTTL &&
		!p.PromptAt.After(r.QueuedAt) && r.Generation == reconnect.Generation
}

func reconnectGeneration(thread string) string {
	return readReceiptFile(receiptPath(thread) + ".reconnect").Generation
}

// NoteQueueReconnect invalidates inference, not a real pending app item. A
// separate atomic record also handles a command that writes its old receipt
// after the reconnect was observed, without making the writer wait on it.
func NoteQueueReconnect(thread, generation string) error {
	if thread == "" {
		return nil
	}
	if err := writeReceiptFile(receiptPath(thread)+".reconnect", queueReceipt{Generation: generation}); err != nil {
		slog.Warn("could not invalidate the inferred pending wake after reconnect", "err", err,
			"hint", "restore write access to the board data directory")
		return err
	}
	return nil
}
