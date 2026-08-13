package mcp

import (
	"encoding/json"
	"net/http"
	"sync"
)

// Push for the protocol every shipping client actually speaks.
//
// Dibs already had server-push: subscriptions/listen (SEP-2575), verified end
// to end. But that is the 2026-07-28 method, and no shipping host negotiates
// 2026-07-28, so the push capability existed for a protocol nobody speaks and
// was absent from the one everybody does. Every real agent was left polling.
//
// 2025-11-25 does it in two parts rather than one:
//
//	POST resources/subscribe {uri}  : register interest, returns {}
//	GET  /mcp  Accept: text/event-stream: the space notifications arrive on
//
// The split is why this could not simply reuse serveSubscription: there, the
// POST that subscribes IS the stream. Here the subscription outlives any single
// request and has to be remembered against the session, and the stream is opened
// separately: possibly before the subscribe, possibly after, possibly
// reconnected after a drop.
//
// # Still not driving anything
//
// The client opens the space and asks. Dibs answers on a connection the
// client owns and can close at any moment, which is the same shape as
// await_events and the opposite of the shell-hook wrapper that was built and
// deleted. Nothing here reaches into a harness.
//
// # Scoping
//
// dibs://board is public: every agent may watch it. dibs://inbox is one
// agent's mail, so subscribing to it requires that agent's token, exactly as
// reading it does. The token is remembered with the subscription because the
// GET that opens the stream carries no body to put it in.

// legacySubs remembers what each session asked to watch.
//
// Keyed by Mcp-Session-Id, which the legacy handshake hands out at initialize,
// the only identifier that survives from the POST that subscribes to the GET
// that streams.
type legacySubs struct {
	mu sync.RWMutex
	// by session id → what that session watches.
	by map[string]*legacySub
}

type legacySub struct {
	board bool
	inbox bool
	// token scopes the inbox subscription to one agent. Held here because the
	// GET carries no body; it is the same token the client already proved it
	// holds when it subscribed.
	token string
}

func newLegacySubs() *legacySubs { return &legacySubs{by: map[string]*legacySub{}} }

// add records a subscription, returning false if the URI is not one Dibs
// publishes.
//
// Unknown URIs are refused rather than silently accepted: a client that
// subscribes to a typo and then waits forever has been told nothing is
// happening, when in fact nobody was ever listening on its behalf.
func (l *legacySubs) add(session, uri, token string) bool {
	if session == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sub, ok := l.by[session]
	if !ok {
		sub = &legacySub{}
		l.by[session] = sub
	}
	switch uri {
	case "dibs://board":
		sub.board = true
	case "dibs://inbox":
		sub.inbox = true
		sub.token = token
	default:
		return false
	}
	return true
}

// remove drops one subscription. Removing the last one leaves an empty record
// rather than deleting it, so a stream already open keeps a stable identity and
// simply stops matching.
func (l *legacySubs) remove(session, uri string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sub, ok := l.by[session]
	if !ok {
		return
	}
	switch uri {
	case "dibs://board":
		sub.board = false
	case "dibs://inbox":
		sub.inbox, sub.token = false, ""
	}
}

// get returns a copy of what a session watches.
func (l *legacySubs) get(session string) legacySub {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if sub, ok := l.by[session]; ok {
		return *sub
	}
	return legacySub{}
}

// drop forgets a session entirely, when its stream closes for good.
func (l *legacySubs) drop(session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.by, session)
}

// subscribeParams is the legacy shape: a bare URI, plus the usual _meta.
type subscribeParams struct {
	URI  string         `json:"uri"`
	Meta map[string]any `json:"_meta"`
}

// handleLegacySubscribe records interest for this session.
func (s *Server) handleLegacySubscribe(r *http.Request, params json.RawMessage) (any, *rpcError) {
	var p subscribeParams
	_ = json.Unmarshal(params, &p)
	session := r.Header.Get("Mcp-Session-Id")
	if session == "" {
		return nil, &rpcError{Code: -32602, Message: "resources/subscribe needs a session; " +
			"call initialize first and send the Mcp-Session-Id header it returns"}
	}
	token, _ := p.Meta[metaTokenKey].(string)
	if p.URI == "dibs://inbox" && token == "" {
		return nil, &rpcError{
			Code: -32602,
			Message: "dibs://inbox is one agent's mail; subscribing needs that agent's token in _meta['" +
				metaTokenKey + "']",
		}
	}
	if !s.legacy.add(session, p.URI, token) {
		return nil, &rpcError{
			Code:    -32602,
			Message: "unknown resource " + p.URI + "; Dibs publishes dibs://board and dibs://inbox",
		}
	}
	return map[string]any{}, nil
}

// handleLegacyUnsubscribe stops the flow without closing the stream, which may
// still be carrying another subscription.
//
// Cannot fail, and the signature says so. Unsubscribing from something you were
// not subscribed to is not an error: it is the state you asked for.
func (s *Server) handleLegacyUnsubscribe(r *http.Request, params json.RawMessage) any {
	var p subscribeParams
	_ = json.Unmarshal(params, &p)
	if session := r.Header.Get("Mcp-Session-Id"); session != "" {
		s.legacy.remove(session, p.URI)
	}
	return map[string]any{}
}

// serveLegacyStream is the GET side: the space notifications arrive on.
//
// Opened by the client, closed by the client. If it opens before anything is
// subscribed that is fine and normal: the stream simply carries keepalives
// until a subscribe arrives, which is what a client reconnecting after a drop
// does.
func (s *Server) serveLegacyStream(w http.ResponseWriter, r *http.Request) {
	session := r.Header.Get("Mcp-Session-Id")
	if session == "" {
		http.Error(w, "GET /mcp is the notification space and needs a session; "+
			"call initialize first and send the Mcp-Session-Id header it returns",
			http.StatusBadRequest)
		return
	}
	sub := s.legacy.get(session)

	// Resolve the agent the inbox subscription belongs to, and the serial to
	// start from, so nothing that happened between subscribing and connecting is
	// missed.
	agentID, since, err := s.eng.SubscribeInfo(r.Context(), sub.token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, cancel := s.eng.Subscribe(since)
	defer cancel()
	// subID nil: a legacy notifications/resources/updated carries only the uri.
	// There is no subscription id in 2025-11-25: that is a 2026 concept, and
	// inventing one here would put a field in the payload no client expects.
	s.pump(r, sseStream{w: w, fl: flusher}, ch, nil, s.legacyWants(r, session, agentID, sub.token))
}

// legacyWants reads this session's subscription state fresh on every event.
//
// The agent is resolved from whichever token the session last subscribed with,
// and re-resolved when that token changes. A stream opened before any subscribe
// has no token at all, so there is nothing to resolve until one arrives: that
// is the ordering the transport explicitly allows and the captured-at-open
// version could never serve.
//
// A token that no longer authenticates resolves to no agent, which delivers
// nothing rather than delivering someone else's mail. Failing closed is the
// only safe direction here.
func (s *Server) legacyWants(r *http.Request, session, agentID, token string) wantsFunc {
	return func() (string, bool, bool) {
		sub := s.legacy.get(session)
		if sub.token != token {
			token, agentID = sub.token, ""
			if sub.token != "" {
				if id, _, err := s.eng.SubscribeInfo(r.Context(), sub.token); err == nil {
					agentID = id
				}
			}
		}
		return agentID, sub.inbox, sub.board
	}
}
