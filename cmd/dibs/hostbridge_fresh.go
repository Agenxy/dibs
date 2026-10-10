// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/wakeexec"
)

func (b *wakeBridge) nativeNotice(wr engine.WakeRequest, f wakeexec.Fields) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]any{"id": wr.ID, "host": b.host})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.origin+"/api/wake-owed", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Dibs-Local", b.secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("hub did not confirm original native wake is still owed")
	}
	var result struct {
		Owed *bool `json:"owed"`
	}
	if json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4096)).Decode(&result) != nil || result.Owed == nil {
		return "", errors.New("hub wake-owed response changed")
	}
	if !*result.Owed {
		return "", nil
	}
	return wakeexec.ComposeNative(f), nil
}
