// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// errNoActiveTurn is the owner's refusal to steer a thread that is not
// running a turn. An owner's handler error reaches Dibs in the router's shape
// (no method, no handling client) with the thrown message as its code, so the
// message is the only evidence; this is the app's own test for it, verbatim.
var errNoActiveTurn = errors.New("the thread has no running turn to steer")

var noActiveTurn = regexp.MustCompile(`^Cannot steer conversation \S+ because its active turn already ended$`)

// Only matched replies can establish acceptance. No response or unrelated
// traffic can imply an owner, an empty queue, or a started turn.
func (c *client) call(method string, version int, owner string, params any) (frame, error) {
	id := requestID()
	if err := c.write(frame{
		Type: "request", RequestID: id, SourceClientID: c.id,
		TargetClientID: owner, Version: version, Method: method, Params: params,
	}); err != nil {
		return frame{}, fmt.Errorf("native %s write: %w", method, err)
	}
	for range 128 {
		r, err := c.next()
		if err != nil {
			return frame{}, fmt.Errorf("native %s reply unknown: %w", method, err)
		}
		if r.Type != "response" || r.RequestID != id {
			continue
		}
		return matchedReply(method, owner, r)
	}
	return frame{}, errors.New("native reply traffic exceeded bound")
}

// matchedReply checks the reply to the request this client sent.
func matchedReply(method, owner string, r frame) (frame, error) {
	if r.ResultType == "error" && r.Method == "" && r.HandledByClientID == "" {
		return frame{}, routerError(method, r.Error)
	}
	if r.Method != method || (owner != "" && r.HandledByClientID != owner) {
		return frame{}, errors.New("native reply method or owner changed")
	}
	if r.ResultType == "error" {
		if method == "thread-owner-discovery" && r.Error == "no-client-found" {
			return frame{}, ErrNoOwner
		}
		// Never include app error bodies (which may contain private input)
		// in the daemon's public logs or send note.
		return frame{}, fmt.Errorf("native %s: %w", method, ErrRefused)
	}
	if r.ResultType != "success" || len(r.Result) == 0 || string(r.Result) == "null" {
		return frame{}, errors.New("native reply result shape changed")
	}
	return r, nil
}

// routerError classifies an error the app's IPC router wrote itself. Those
// replies carry no method and no handling client: the router answers for a
// request it could not route, so they cannot match the method check that a
// client's own reply must pass. Read from ChatGPT 26.1007.30912's router.
// Reading them as a changed protocol sent every wake to an unloaded thread
// down the cold route, which opened it in the person's window (agenxy-dev-2,
// 2026-10-11 03:43), and stopped the post-restart load from ever starting.
func routerError(method, code string) error {
	if method == "thread-follower-steer-turn" && noActiveTurn.MatchString(code) {
		return fmt.Errorf("native %s: %w", method, errNoActiveTurn)
	}
	switch code {
	case "no-client-found":
		if method == "thread-owner-discovery" {
			return ErrNoOwner
		}
		// The router found no client to forward to: no client received it.
		return fmt.Errorf("native %s: %w", method, ErrRefused)
	case "request-version-mismatch":
		return fmt.Errorf("native %s: %w", method, ErrRefused)
	}
	// request-timeout, client-disconnected, server-closed: the request may
	// have reached a client. Unknown, never a refusal that permits a retry.
	return fmt.Errorf("native %s: the app's router reported %q; outcome unknown", method, code)
}

func (c *client) next() (frame, error) {
	f, err := c.read()
	if err != nil {
		return f, err
	}
	if f.Type == "client-discovery-request" {
		// Dibs never handles follower requests and never becomes a router.
		if f.RequestID == "" {
			return frame{}, errors.New("native discovery shape changed")
		}
		err = c.write(frame{
			Type: "client-discovery-response", RequestID: f.RequestID,
			Response: map[string]bool{"canHandle": false},
		})
	}
	return f, err
}

// owner is the client holding thread, from the router's own discovery.
func (c *client) owner(thread string) (string, error) {
	r, err := c.call("thread-owner-discovery", 1, "", map[string]string{"hostId": "local", "conversationId": thread})
	if err != nil {
		return "", err
	}
	var capabilities struct {
		SupportsUntrustedAppInput *bool `json:"supportsUntrustedAppInput"`
	}
	if r.HandledByClientID == "" || json.Unmarshal(r.Result, &capabilities) != nil ||
		capabilities.SupportsUntrustedAppInput == nil {
		return "", errors.New("native owner response changed")
	}
	if !*capabilities.SupportsUntrustedAppInput {
		return "", errors.New("native owner does not accept untrusted app input")
	}
	return r.HandledByClientID, nil
}

func (c *client) deliver(thread string, notice func() (string, error)) (Receipt, error) {
	owner, err := c.owner(thread)
	if err != nil {
		return Receipt{}, err
	}
	text, err := notice()
	if err != nil {
		return Receipt{}, err
	}
	if text == "" {
		return Receipt{Disposition: "settled"}, nil
	}
	input := []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
	// Steer first. A start request into a thread that is already running a
	// turn is admitted by the app-server, which answers with the running
	// turn's id, but the app's window has by then pushed an optimistic turn
	// with no id that nothing ever fills in. The next message the person types
	// is routed to steer that empty turn, waits 30 s for its id and fails with
	// "Cannot steer conversation ... without an active turn id" (ChatGPT
	// 26.1111.21308, the person's report of 2026-11-20). Steering is the app's
	// own path for input into a running turn and pushes no turn of its own.
	r, err := c.call("thread-follower-steer-turn", 1, owner, map[string]any{
		"conversationId":      thread,
		"clientUserMessageId": requestID(),
		"input":               input,
		"restoreMessage": map[string]any{
			"id": requestID(), "context": map[string]any{"prompt": text},
		},
	})
	if err == nil {
		return steerReceipt(r.Result)
	}
	if !errors.Is(err, errNoActiveTurn) {
		return Receipt{}, err
	}
	// The owner refused the steer because no turn is running, which it decides
	// before sending anything to the agent: the app's own composer starts a
	// turn after exactly this refusal (canStartAfterSteerError). Exactly one
	// start follows; no retry follows a possibly submitted input.
	r, err = c.call("thread-follower-start-turn", 2, owner, map[string]any{
		"conversationId": thread,
		"turnStart": map[string]any{
			"request": map[string]any{"threadId": thread, "input": input},
			"context": map[string]bool{"inheritThreadSettings": true},
		},
	})
	if err != nil {
		return Receipt{}, err
	}
	return deliveryReceipt(r.Result)
}

// steerReceipt reads the owner's reply to a steer: the running turn's id.
func steerReceipt(raw json.RawMessage) (Receipt, error) {
	var value struct {
		Result struct {
			TurnID string `json:"turnId"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Result.TurnID == "" {
		return Receipt{}, errors.New("native steer receipt lacks turn id")
	}
	return Receipt{"steered", value.Result.TurnID}, nil
}

func deliveryReceipt(raw json.RawMessage) (Receipt, error) {
	var value struct {
		Result struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return Receipt{}, errors.New("native delivery response changed")
	}
	turn := value.Result.Turn
	if turn.ID == "" || turn.Status != "inProgress" {
		return Receipt{}, errors.New("native start receipt lacks active turn")
	}
	// The same receipt represents a newly started or an already active turn.
	// Without a separate observation, acceptance cannot distinguish them.
	return Receipt{"accepted", turn.ID}, nil
}
