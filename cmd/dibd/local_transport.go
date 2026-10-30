// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
)

func registerClientTransports(mux *http.ServeMux, clients *localtransport.Clients) {
	mux.HandleFunc("GET /api/client-transports", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"clients": clients.Snapshot(),
			"scope":   "currently connected HTTP clients; absent dormant clients are not qualified",
		})
	})
}

func startLocalIPC(ctx context.Context, dir string, handler http.Handler,
	clients *localtransport.Clients, stop context.CancelFunc,
) (<-chan error, func(), error) {
	failed := make(chan error, 1)
	if !localtransport.Supported() {
		return failed, func() {}, nil
	}
	ln, err := localtransport.Listen(dir)
	if err != nil {
		return nil, nil, err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ConnContext: clients.ConnContext, ConnState: clients.ConnState}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
			stop()
		}
	}()
	go func() {
		<-ctx.Done()
		// The shutdown must drain after the serving context is cancelled.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	return failed, func() { _ = srv.Close(); _ = ln.Close() }, nil
}
