// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"

	"github.com/agenxy/dibs/internal/engine"
)

func registerNativeWakeFence(mux *http.ServeMux, eng *engine.Engine, authed func(*http.Request) bool) {
	mux.HandleFunc("POST /api/wake-owed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !authed(r) {
			refuse(w, http.StatusUnauthorized, "wake fence not available")
			return
		}
		var req struct {
			ID   uint64 `json:"id"`
			Host string `json:"host"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || req.ID == 0 || req.Host == "" {
			refuse(w, http.StatusBadRequest, "a wake fence names the request id and its host")
			return
		}
		owed, err := eng.HostWakeOwed(r.Context(), req.ID, req.Host)
		if err != nil {
			refuse(w, http.StatusUnauthorized, "wake fence not available")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"owed": owed})
	})
}
