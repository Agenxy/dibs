// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"encoding/json"
	"errors"
	"fmt"
)

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
	return frame{}, errors.New("native reply traffic exceeded bound")
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
	params := map[string]any{
		"conversationId": thread,
		"turnStart": map[string]any{
			"request": map[string]any{"threadId": thread, "input": input},
			"context": map[string]bool{"inheritThreadSettings": true},
		},
	}
	// The app owns start-or-steer. Never fetch the thread's growing history
	// merely to predict a decision the harness already makes atomically.
	// Exactly one submission; no retry follows a possibly submitted input.
	r, err := c.call("thread-follower-start-turn", 2, owner, params)
	if err != nil {
		return Receipt{}, err
	}
	return deliveryReceipt(r.Result)
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
