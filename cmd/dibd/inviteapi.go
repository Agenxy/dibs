// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"

	"github.com/agenxy/dibs/internal/invites"
)

// Human issuance is one path, not the agent-facing issuance policy. Its
// proof remains behind the private god-view gate; MCP invite needs no prompt.
func registerInvitationAPI(mux *http.ServeMux, service *invites.Service) {
	mux.HandleFunc("POST /api/admin/invites", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Action   string `json:"action"`
			Name     string `json:"name"`
			IssuedBy string `json:"issued_by"`
			TTLS     int64  `json:"ttl_s"`
			Export   bool   `json:"export"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p); err != nil {
			publicError(w, 400, "E_ARGUMENT", "invalid invitation request", "use action: mint, list or revoke")
			return
		}
		result, err := service.Human(r.Context(), p.Action, p.Name, p.IssuedBy, p.TTLS, p.Export)
		if err != nil {
			publicError(w, 400, "E_INVITE_CONFIG", err.Error(),
				"review dibs invite list; use a new unprivileged name, or revoke before reissuing")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	})
}
