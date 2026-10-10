// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"encoding/json"
	"errors"
)

// Targeted native following requests a snapshot of the already owned thread.
// It loads no cold thread, navigates no window and reads no app database.
// Disconnecting removes the temporary follower; explicitly unfollow too.
func (c *client) snapshot(thread, owner string) (bool, string, error) {
	send := func(following bool) error {
		return c.write(frame{
			Type: "broadcast", SourceClientID: c.id, TargetClientIDs: []string{owner},
			Method: "thread-stream-following-changed", Version: 1,
			Params: map[string]any{"conversationId": thread, "hostId": "local", "following": following},
		})
	}
	if err := send(true); err != nil {
		return false, "", err
	}
	defer func() { _ = send(false) }()
	for range 128 {
		f, err := c.next()
		if err != nil {
			return false, "", err
		}
		running, prior, matched, err := snapshotState(f, thread, owner)
		if err != nil || matched {
			return running, prior, err
		}
	}
	return false, "", errors.New("native snapshot traffic exceeded bound")
}

func snapshotState(f frame, thread, owner string) (bool, string, bool, error) {
	if f.Type != "broadcast" || f.Method != "thread-stream-state-changed" || f.SourceClientID != owner {
		return false, "", false, nil
	}
	if f.Version != 11 {
		return false, "", false, errors.New("native snapshot version changed")
	}
	b, err := json.Marshal(f.Params)
	if err != nil {
		return false, "", false, err
	}
	var p struct {
		ConversationID string `json:"conversationId"`
		HostID         string `json:"hostId"`
		Change         struct {
			Type  string `json:"type"`
			State struct {
				ID     string `json:"id"`
				Status struct {
					Type string `json:"type"`
				} `json:"threadRuntimeStatus"`
				Turns []struct {
					TurnID string `json:"turnId"`
				} `json:"turns"`
			} `json:"conversationState"`
		} `json:"change"`
	}
	if json.Unmarshal(b, &p) != nil {
		return false, "", false, errors.New("native snapshot shape changed")
	}
	if p.ConversationID != thread || p.HostID != "local" {
		return false, "", false, nil
	}
	if p.Change.Type != "snapshot" {
		return false, "", false, nil
	}
	s := p.Change.State
	if s.ID != thread || (s.Status.Type != "idle" && s.Status.Type != "active") {
		return false, "", false, errors.New("native snapshot lacks known loaded thread state")
	}
	prior := ""
	if len(s.Turns) > 0 {
		prior = s.Turns[len(s.Turns)-1].TurnID
	}
	return s.Status.Type == "active", prior, true, nil
}
