// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/agenxy/dibs/internal/localtransport"
)

func stampLocalConnection(r *http.Request, params json.RawMessage, host string) *http.Request {
	local := localtransport.IsLocal(r.Context()) || isLoopback(r.RemoteAddr)
	r = r.WithContext(withTransportHost(r.Context(), local, host))
	pid, start := bridgeProcessMetadata(params)
	localtransport.ObserveBridge(r.Context(), pid, start)
	return r
}
