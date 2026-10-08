// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/transfer"
	xport "github.com/agenxy/dibs/internal/transport"
)

type transferOriginKey struct{}

// SetTransfers uses the shared manager for both listeners. Public origin is
// operator-configured; the private listener advertises its authenticated request
// authority so wildcard binds remain reachable using their joined address.
func (s *Server) SetTransfers(manager *transfer.Manager, origin string) {
	s.transfers = manager
	s.transferOrigin = origin
}

func requestTransferOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	u, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return ""
	}
	if r.TLS == nil {
		if !xport.IsLoopback(r.RemoteAddr) || !xport.IsLoopback(u.Host) {
			return "" // plaintext descriptors are a same-machine loopback extension only
		}
	}
	return u.String()
}

func (s *Server) authorizeUpload(ctx context.Context, a *toolArgs) (core.Result, error) {
	if s.transfers == nil {
		return nil, transferUnavailable()
	}
	origin, _ := ctx.Value(transferOriginKey{}).(string)
	return s.transfers.Authorize(ctx, origin, a.Token, "", a.SHA256, a.Mime, a.Name, a.FileSize)
}

func (s *Server) authorizeDownload(ctx context.Context, a *toolArgs) (core.Result, error) {
	if a.Blob == "" {
		return nil, core.ErrNoID
	}
	if strings.HasPrefix(a.Blob, "dibs:pending:") {
		return nil, &core.Error{
			Code: "E_UPLOAD_PENDING", Msg: "file is not committed yet",
			Hint: "complete the upload and use the canonical blob id returned by that transfer",
		}
	}
	if s.transfers == nil {
		return nil, transferUnavailable()
	}
	origin, _ := ctx.Value(transferOriginKey{}).(string)
	return s.transfers.Authorize(ctx, origin, a.Token, a.Blob, "", "", "", nil)
}

func transferUnavailable() error {
	return &core.Error{
		Code: "E_TRANSFER_UNAVAILABLE", Msg: "this server has no HTTP byte-plane listener",
		Hint: "connect to the daemon's HTTP(S) MCP endpoint, or use put_blob/get_blob for inline bytes",
	}
}
