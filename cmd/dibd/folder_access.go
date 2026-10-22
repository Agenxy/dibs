// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"

	"github.com/agenxy/dibs/internal/folderaccess"
)

var newFolderAccess = func() *folderaccess.Monitor {
	if runtime.GOOS != "darwin" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return folderaccess.New(home, nil)
}

func registerFolderAccessAPI(mux *http.ServeMux, m *folderaccess.Monitor) {
	if m == nil {
		return
	}
	// The normal coordination gate authenticates this route. It accepts no
	// paths: acquisition can only request the three predefined local roots.
	mux.HandleFunc("POST /api/folder-access/request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"requested": m.Request(), "folder_access": m.Snapshot(), "hint": "A directory read was requested; only macOS and the person can grant access. " + folderaccess.Settings})
	})
}
