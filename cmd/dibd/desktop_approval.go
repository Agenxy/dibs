// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// This administrative route inherits the existing human password/page-key
// authority gate. An ordinary bridge secret or agent token cannot reach it.
func registerDesktopApprovalAPI(mux *http.ServeMux, eng *engine.Engine) {
	mux.HandleFunc("POST /api/admin/desktop-approvals", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeAdminResult(w, nil, &core.Error{
				Code: "E_BAD_ARG", Msg: "expected an optional boolean enabled field",
				Hint: "use Settings on dibs web or dibs desktop-approvals on|off",
			})
			return
		}
		if body.Enabled == nil {
			result, err := eng.DesktopApprovalSettings(r.Context())
			writeAdminResult(w, result, err)
			return
		}
		_, token, err := eng.HumanAgent(r.Context())
		if err != nil {
			writeAdminResult(w, nil, err)
			return
		}
		result, err := eng.Configure(r.Context(), token, core.DesktopApprovalSettingKey, strconv.FormatBool(*body.Enabled))
		writeAdminResult(w, result, err)
	})
}
