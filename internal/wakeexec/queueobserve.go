// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
)

const queueProbeTimeout = time.Second

// queueTarget finds the canonical thread on a queue route. Extra options use
// only the fallback: a remote/profile/config override can name another app.
func queueTarget(argv []string) (string, bool) {
	if len(argv) < 4 || filepath.Base(argv[0]) != "codex" || argv[1] != "queue" {
		return "", false
	}
	for i := 2; i+1 < len(argv); i++ {
		if argv[i] == "--thread" && argv[i+1] != "" {
			return argv[i+1], true
		}
	}
	return "", false
}

func nativeQueueRoute(argv []string) bool {
	return len(argv) == 6 && argv[2] == "--thread" && argv[4] == "--message"
}

// UsesQueueReceipt reports whether RunCommands observes or retains this route's
// pending item. An upstream inferred hold must not override an observed empty queue.
func UsesQueueReceipt(argv []string) bool { _, ok := queueTarget(argv); return ok }

// NativeQueueTemplate is the exact operator-owned queue shape the app-restart
// sweep may reuse. A hard-coded thread or message is not a route to this
// agent, even if it happens to be a valid command for another purpose.
func NativeQueueTemplate(argv []string) bool {
	_, ok := queueTarget(argv)
	return ok && nativeQueueRoute(argv) && argv[3] == "{thread}" && argv[5] == "{message}"
}

// observeQueue uses the harness's own experimental API, never its database.
// initialize and list load no thread (measured on the installed binary).
// An unsupported or changed response is UNKNOWN, never an empty queue.
func observeQueue(binary, thread string) (pending, known bool) {
	p := observeQueueDetail(binary, thread)
	return p.pending, p.known
}

func observeQueueDetail(binary, thread string) queueProbe {
	ctx, cancel := context.WithTimeout(context.Background(), queueProbeTimeout)
	defer cancel()
	argv := []string{binary, "app-server", "--listen", "stdio://"}
	if !boardconfig.ReadOnlyQueueProbe(argv) {
		return queueProbe{}
	}
	// #nosec G204 G702 -- binary is the operator's queue executable; all probe
	// arguments are fixed literals, separately refused by ReadOnlyQueueProbe.
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.WaitDelay = 100 * time.Millisecond
	in, err := cmd.StdinPipe()
	if err != nil {
		return queueProbe{}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return queueProbe{}
	}
	if err = cmd.Start(); err != nil {
		return queueProbe{}
	}
	stop := context.AfterFunc(ctx, func() { _ = out.Close() })
	defer stop()
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	rpc := queueRPC{enc: json.NewEncoder(in), dec: json.NewDecoder(io.LimitReader(out, 4<<20))}
	if err = rpc.initialize(); err != nil {
		return queueProbe{}
	}
	pending, known := rpc.list(thread)
	return queueProbe{pending: pending, known: known, issuedAt: rpc.issuedAt}
}

type queueRPC struct {
	enc      *json.Encoder
	dec      *json.Decoder
	issuedAt time.Time
}

func (r queueRPC) call(id int, method string, params any) (json.RawMessage, error) {
	if err := r.enc.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for range 32 {
		var out struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := r.dec.Decode(&out); err != nil {
			return nil, err
		}
		if out.ID == id {
			if len(out.Error) > 0 || len(out.Result) == 0 {
				return nil, errors.New("queue RPC failed")
			}
			return out.Result, nil
		}
	}
	return nil, errors.New("queue RPC notification bound exceeded")
}

func (r queueRPC) initialize() error {
	params := map[string]any{
		"clientInfo": map[string]string{"name": "dibs-queue-observer", "version": "1"},
		// The harness gates this experimental method. Shape changes are UNKNOWN.
		"capabilities": map[string]bool{"experimentalApi": true},
	}
	if _, err := r.call(1, "initialize", params); err != nil {
		return err
	}
	return r.enc.Encode(map[string]any{"method": "initialized", "params": map[string]any{}})
}

func (r *queueRPC) list(thread string) (pending, known bool) {
	var cursor *string
	for page := 0; page < 16; page++ {
		raw, er := r.call(page+2, "thread/queue/list", map[string]any{"threadId": thread, "limit": 100, "cursor": cursor})
		if er != nil {
			return false, false
		}
		p, k, next, issued := queuePageDetail(raw)
		if !k {
			return false, false
		}
		if p {
			r.issuedAt = issued
			return true, true
		}
		if next == nil {
			return false, true
		}
		cursor = next
	}
	return false, false
}

func queuePage(raw json.RawMessage) (pending, known bool, next *string) {
	p, k, n, _ := queuePageDetail(raw)
	return p, k, n
}

func queuePageDetail(raw json.RawMessage) (pending, known bool, next *string, issued time.Time) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false, false, nil, time.Time{}
	}
	data, ok := fields["data"]
	if !ok || string(data) == "null" {
		return false, false, nil, time.Time{}
	}
	nc, ok := fields["nextCursor"]
	if !ok || json.Unmarshal(nc, &next) != nil {
		return false, false, nil, time.Time{}
	}
	var rows []queueItem
	if json.Unmarshal(data, &rows) != nil {
		return false, false, nil, time.Time{}
	}
	p := observeQueueItems(rows)
	return p.pending, p.known, next, p.issuedAt
}

type queueItem struct {
	ID    string `json:"id"`
	Input []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"input"`
}

func observeQueueItems(rows []queueItem) queueProbe {
	var pending bool
	var issued time.Time
	legacyAge := false
	for _, r := range rows {
		if r.ID == "" || r.Input == nil {
			return queueProbe{}
		}
		if len(r.Input) == 1 && r.Input[0].Type == "text" && isDibsWake(r.Input[0].Text) {
			pending = true
			at, _ := noticeIssuedAt(r.Input[0].Text)
			legacyAge = legacyAge || at.IsZero()
			if !at.IsZero() && (issued.IsZero() || at.Before(issued)) {
				issued = at
			}
		}
	}
	if legacyAge {
		issued = time.Time{} // do not date a legacy backlog from its newer sibling
	}
	return queueProbe{pending: pending, known: true, issuedAt: issued}
}

func isDibsWake(text string) bool {
	_, legacy := legacyWakeKind(text)
	return legacy || timestampedWake(text)
}
