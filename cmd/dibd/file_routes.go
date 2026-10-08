// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/transfer"
)

func registerTransferStatus(mux *http.ServeMux, publicOrigin string) {
	mux.HandleFunc("GET /api/transfer-status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"public_origin": publicOrigin})
	})
}

// Installed on the PRIVATE mux only: not a public CA-discovery or admin route.
func registerGuestStatus(mux *http.ServeMux, endpoint *invites.PublicEndpoint) {
	mux.HandleFunc("GET /api/guest-status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(endpoint.Snapshot())
	})
}

// This is the ONLY private auth-gate exception: ticket handlers authenticate
// their own one-file capability and never delegate to board or admin routes.
func fileRoutes(files *transfer.Manager, private http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, transfer.Prefix) {
			files.Handler("", false).ServeHTTP(w, r)
			return
		}
		private.ServeHTTP(w, r)
	})
}
