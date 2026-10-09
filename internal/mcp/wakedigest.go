// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import "context"

// WakeDigestURI is hidden from discovery, like the host bridge's wake stream.
// The notification advertises this read to bridges capable of refreshing a
// deferred notice. Old dormant bridges ignore the additive key.
const (
	WakeDigestURI        = "dibs://wake-digest"
	DigestRefreshMetaKey = "com.dibs/digest_refresh"
	SocketOfferMetaKey   = "com.dibs/socket_offer"
	SocketRetryMetaKey   = "com.dibs/socket_retry_offer"
	SocketOfferIDMetaKey = "com.dibs/socket_offer_id"
	SocketWrittenMetaKey = "com.dibs/socket_written"
	SocketBatchMetaKey   = "com.dibs/socket_batch"
	SocketTokensMetaKey  = "com.dibs/socket_tokens" //nolint:gosec // metadata key name, not a credential
)

func (s *Server) readWakeDigest(ctx context.Context, meta map[string]any) (any, *rpcError) {
	token, _ := meta[metaTokenKey].(string)
	session, _ := meta[SessionMetaKey].(string)
	if token == "" || session == "" {
		return nil, &rpcError{
			Code: -32602, Message: "wake digest requires an agent token and session",
			Data: hint("pass _meta['com.dibs/token'] and _meta['com.dibs/session'] from the bridge's subscription"),
		}
	}
	var text, offer string
	var err error
	if offers, _ := meta[SocketOfferMetaKey].(bool); offers {
		id, _ := meta[SocketOfferIDMetaKey].(string)
		written, _ := meta[SocketWrittenMetaKey].(bool)
		tokens := []string{token}
		if raw, ok := meta[SocketTokensMetaKey].([]any); ok {
			for _, v := range raw {
				if t, ok := v.(string); ok && len(tokens) < 512 {
					tokens = append(tokens, t)
				}
			}
		}
		retry, _ := meta[SocketRetryMetaKey].(string)
		res, callErr := s.eng.SocketOffersRetryFor(ctx, tokens, session, id, written, retry)
		err = callErr
		text, _ = res["digest"].(string)
		offer, _ = res["offer"].(string)
	} else {
		text, err = s.eng.FreshWakeDigestFor(ctx, token, session)
	}
	if err != nil {
		return nil, rpcErrFrom(err)
	}
	out := map[string]any{"contents": []map[string]any{
		{"uri": WakeDigestURI, "mimeType": "text/plain", "text": text},
	}}
	outMeta := map[string]any{}
	if offer != "" {
		outMeta[SocketOfferIDMetaKey] = offer
	}
	if retry, _ := meta[SocketRetryMetaKey].(string); retry != "" {
		outMeta[SocketRetryMetaKey] = retry
	}
	if len(outMeta) > 0 {
		out["_meta"] = outMeta
	}
	return cacheable(out, 0, scopePrivate), nil
}
