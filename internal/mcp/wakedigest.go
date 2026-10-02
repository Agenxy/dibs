package mcp

import "context"

// WakeDigestURI is hidden from discovery, like the host bridge's wake stream.
// The notification advertises this read to bridges capable of refreshing a
// deferred notice. Old dormant bridges ignore the additive key.
const (
	WakeDigestURI        = "dibs://wake-digest"
	DigestRefreshMetaKey = "com.dibs/digest_refresh"
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
	text, err := s.eng.FreshWakeDigestFor(ctx, token, session)
	if err != nil {
		return nil, rpcErrFrom(err)
	}
	return cacheable(map[string]any{"contents": []map[string]any{
		{"uri": WakeDigestURI, "mimeType": "text/plain", "text": text},
	}}, 0, scopePrivate), nil
}
