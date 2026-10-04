package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/mcp"
)

type wakeDigestSource struct {
	key, token, session, url, secret string
	client                           *http.Client
}

func (iw *inboxWatcher) markRefresh(key string) {
	iw.mu.Lock()
	iw.refreshSupported = true
	if st := iw.streams[key]; st != nil {
		st.refresh = true
	}
	iw.mu.Unlock()
	liveWake.mu.Lock()
	if st := liveWake.streams[key]; st != nil {
		st.Refresh = true
	}
	liveWake.mu.Unlock()
}

// Read all mailboxes sharing this session's ONE writer. A coalesced timer is
// not the first event's text: it covers the current state of every mailbox.
// Compatibility is explicit: only a daemon advertising refresh is queried.
func (iw *inboxWatcher) freshNotice(captured string) (string, error) {
	iw.mu.Lock()
	enabled := iw.refreshSupported
	var sources []wakeDigestSource
	for _, st := range iw.streams {
		sources = append(sources, st.source)
	}
	iw.mu.Unlock()
	if !enabled {
		return captured, nil
	}
	// Every stream belongs to this bridge's daemon. One additive capability
	// proves it supports refresh for all agents, including a pre-capability
	// stream restored beside a newer one. No captured mailbox gets dropped.
	sort.Slice(sources, func(i, j int) bool { return sources[i].key < sources[j].key })
	var texts []string
	for _, source := range sources {
		text, err := source.read()
		if err != nil {
			return "", err
		} // never deliver stale text on a failed fresh read
		if text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

func (s wakeDigestSource) read() (string, error) {
	text, _, err := s.readOffer(nil)
	return text, err
}

// offerNotice keeps receipts LOCAL TO THIS WRITE. Several mailboxes share one
// socket writer; a failed refresh of any releases the other attempts too.
func (iw *inboxWatcher) offerNotice(captured string) (string, func(bool), error) {
	iw.mu.Lock()
	offers := iw.offerSupported
	batch := iw.batchSupported
	var sources []wakeDigestSource
	for _, st := range iw.streams {
		sources = append(sources, st.source)
	}
	iw.mu.Unlock()
	if !offers {
		text, err := iw.freshNotice(captured)
		return text, nil, err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].key < sources[j].key })
	if batch && len(sources) > 0 {
		return batchSocketOffer(sources)
	}
	type receipt struct {
		source wakeDigestSource
		id     string
	}
	var receipts []receipt
	finish := func(written bool) {
		for _, r := range receipts {
			_, _, err := r.source.readOffer(map[string]any{
				mcp.SocketOfferMetaKey:   true,
				mcp.SocketOfferIDMetaKey: r.id, mcp.SocketWrittenMetaKey: written,
			})
			if err != nil {
				slog.Debug("could not report the socket write; hook fallback remains available", "err", err)
			}
		}
	}
	var texts []string
	for _, source := range sources {
		text, id, err := source.readOffer(map[string]any{mcp.SocketOfferMetaKey: true})
		if err != nil {
			return "", finish, err
		}
		if id != "" {
			receipts = append(receipts, receipt{source, id})
		}
		if text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n"), finish, nil
}

func batchSocketOffer(sources []wakeDigestSource) (string, func(bool), error) {
	var tokens []string
	for _, source := range sources {
		if source.session == sources[0].session {
			tokens = append(tokens, source.token)
		}
	}
	meta := map[string]any{mcp.SocketOfferMetaKey: true, mcp.SocketTokensMetaKey: tokens}
	text, id, err := sources[0].readOffer(meta)
	finish := func(written bool) {
		if id == "" {
			return
		}
		meta[mcp.SocketOfferIDMetaKey], meta[mcp.SocketWrittenMetaKey] = id, written
		if _, _, err := sources[0].readOffer(meta); err != nil {
			slog.Debug("could not settle this session's socket offer; hook fallback remains", "err", err)
		}
	}
	return text, finish, err
}

func (s wakeDigestSource) readOffer(extra map[string]any) (string, string, error) {
	meta := map[string]any{"com.dibs/token": s.token, mcp.SessionMetaKey: s.session}
	for k, v := range extra {
		meta[k] = v
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "dibs-wake-refresh", "method": "resources/read",
		"params": map[string]any{"uri": mcp.WakeDigestURI, "_meta": meta},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := s.request(ctx, body)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("wake digest read: HTTP %d", resp.StatusCode)
	}
	var reply struct {
		Result *struct {
			Meta     map[string]any `json:"_meta"`
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return "", "", err
	}
	if reply.Error != nil {
		// A credential the board revoked no longer owns a notice. Do not
		// let one obsolete stream surrender the shared writer for live peers.
		if reply.Error.Data.Code == "E_BAD_TOKEN" {
			return "", "", nil
		}
		return "", "", fmt.Errorf("wake digest read: RPC %d: %s", reply.Error.Code, reply.Error.Message)
	}
	if reply.Result == nil || len(reply.Result.Contents) != 1 {
		return "", "", fmt.Errorf("wake digest read: missing digest content")
	}
	id, _ := reply.Result.Meta[mcp.SocketOfferIDMetaKey].(string)
	return reply.Result.Contents[0].Text, id, nil
}

// Follow a board moved underneath a long-lived bridge, without caching a new
// machine fact. The guarded transport refreshes credentials on each request.
func (s wakeDigestSource) request(ctx context.Context, body []byte) (*http.Response, error) {
	client, where := s.client, s.url
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, where, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dibs-Local", s.secret)
		resp, err := client.Do(req)
		if attempt > 0 || !dialFailed(err) {
			return resp, err
		}
		where = boardNow(req.URL)
		if where != s.url {
			client = daemonClient(5 * time.Second)
		}
	}
}
