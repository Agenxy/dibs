// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package notify

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// AskDetail is reading-window data, never a command argument. The native
// helper persists a bounded copy in macOS notification userInfo. Deadline is
// the request's recorded bound; zero falls back to the native 24-hour cap.
type AskDetail struct {
	Sender, Body, Kind string
	Deadline           time.Time
}

// AskMessageDetail preserves the ordinary banner and action protocol, adding
// full text on stdin for an explicitly opened reading window. Linux and legacy
// callers retain their existing path. An empty node denotes an unkeyed caller.
func AskMessageDetail(
	node string, serial uint64, title, excerpt string, detail AskDetail, receipt DeliveryReceipt, buttons ...string,
) (string, error) {
	id := ""
	if node != "" {
		var err error
		id, err = MessageID(node, serial)
		if err != nil {
			return "", err
		}
	}
	return askWithDeliveryID(id, title, excerpt, &detail, receipt, buttons...)
}

const (
	askDetailLimit     = 8 * 1024
	maxAskLifetime     = 24 * time.Hour
	remainingAskDetail = "\n\nRest on the Dibs board."
)

func utf8Prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func encodeAskDetail(detail AskDetail) []byte {
	payload := struct {
		Sender string `json:"sender"`
		Body   string `json:"body"`
		Kind   string `json:"kind,omitempty"`
	}{
		utf8Prefix(strings.ToValidUTF8(detail.Sender, "�"), 1024),
		strings.ToValidUTF8(detail.Body, "�"),
		utf8Prefix(strings.ToValidUTF8(detail.Kind, "�"), 64),
	}
	encoded, _ := json.Marshal(payload) // only strings; cannot fail
	if len(encoded) <= askDetailLimit {
		return encoded
	}
	body := utf8Prefix(payload.Body, askDetailLimit)
	for {
		payload.Body = body + remainingAskDetail
		encoded, _ = json.Marshal(payload)
		if len(encoded) <= askDetailLimit {
			return encoded
		}
		body = utf8Prefix(body, max(0, len(body)-(len(encoded)-askDetailLimit)))
	}
}

func askUntil(deadline, now time.Time) time.Time {
	bound := now.Add(maxAskLifetime)
	if deadline.IsZero() || deadline.After(bound) {
		return bound
	}
	return deadline
}

func askContext(detail *AskDetail) (context.Context, context.CancelFunc) {
	if detail == nil {
		return context.WithTimeout(context.Background(), timeout+30*time.Second)
	}
	// The native helper owns the request deadline. There is no new daemon
	// timer or polling loop; cleanup cancels a live invocation directly.
	return context.WithCancel(context.Background())
}

// Derived per-process invocations, keyed by the same board-scoped ID as the
// native cleanup API. Losing this map never loses a request or an answer.
var nativeAsks = struct {
	sync.Mutex
	byID map[string]map[*context.CancelFunc]struct{}
}{byID: make(map[string]map[*context.CancelFunc]struct{})}

func trackNativeAsk(id string, cancel context.CancelFunc) func() {
	if id == "" {
		return func() {}
	}
	nativeAsks.Lock()
	if nativeAsks.byID[id] == nil {
		nativeAsks.byID[id] = make(map[*context.CancelFunc]struct{})
	}
	nativeAsks.byID[id][&cancel] = struct{}{}
	nativeAsks.Unlock()
	return func() {
		nativeAsks.Lock()
		defer nativeAsks.Unlock()
		delete(nativeAsks.byID[id], &cancel)
		if len(nativeAsks.byID[id]) == 0 {
			delete(nativeAsks.byID, id)
		}
	}
}

func cancelNativeAsks(ids []string) {
	var cancels []context.CancelFunc
	nativeAsks.Lock()
	for _, id := range ids {
		for cancel := range nativeAsks.byID[id] {
			cancels = append(cancels, *cancel)
		}
	}
	nativeAsks.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
