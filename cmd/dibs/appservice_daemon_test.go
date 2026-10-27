// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"net/http"
	"os"
	"time"
)

// External daemon double, reached only in a private copied fixture process.
// Its lifetime and PID registry are real; ServiceManagement is never called.
func serveAppServiceFixture() int {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/board", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node":"app-service-fixture","agents":[],"serial":7}`))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"version":"0.0.16"}}}`))
	})
	mux.HandleFunc("/api/folder-access/request", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requested":true}`))
	})
	srv := &http.Server{Addr: os.Getenv("DIBS_TEST_APP_SERVICE_ADDR"), Handler: mux, ReadHeaderTimeout: time.Second}
	if err := srv.ListenAndServe(); err != nil {
		return 25
	}
	return 0
}
