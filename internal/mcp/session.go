package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
)

// Streamable HTTP is stateless per request, but MCP Apps capability is declared
// once at initialize. Without somewhere to keep it, a host that told us it can
// render gets no panel data on every subsequent call, which is exactly what the
// reference host hit: it drew the panel and we sent it nothing, because the
// only carrier for that signal was the stdio bridge's _meta injection.
//
// So issue an Mcp-Session-Id at initialize, as the transport spec provides for,
// and remember the capability against it. Clients echo the header on later
// requests. Bridge-injected _meta still works and takes precedence, so stdio
// hosts are unaffected.
type sessionStore struct {
	mu sync.RWMutex
	ui map[string]bool
	// client remembers who introduced themselves at initialize.
	//
	// The streamable-HTTP transport is stateless, so a tools/call carries no
	// clientInfo: only the handshake does. Without this, every harness that
	// connects over HTTP rather than through the stdio bridge registers agents
	// with NO identity: a live codex run showed up on the board as `harness:
	// null`, indistinguishable from a hand-rolled script. The bridge injects
	// identity for the harnesses that use it; this is the same courtesy for the
	// ones that do not.
	client map[string]clientInfoJSON
	// panelCalls remembers that a PANEL for this session reached the daemon.
	//
	// It is a capability discovered by success rather than declared: an app tool
	// call arriving here proves the host permits them, and a host that permits
	// them is not the one that drops _meta and shows structuredContent instead of
	// content. That pairing is what makes check_in's duplicate droppable: see
	// panelResult. Recorded per session because it is a property of the host on
	// the other end of this connection, not of the daemon.
	panelCalls map[string]bool
	fifo       []string
}

// maxSessions bounds the map: a long-running daemon must not grow one entry per
// reconnect forever. Oldest out first; a dropped session simply re-negotiates.
const maxSessions = 512

func newSessionStore() *sessionStore {
	return &sessionStore{
		ui: map[string]bool{}, client: map[string]clientInfoJSON{},
		panelCalls: map[string]bool{},
	}
}

func (s *sessionStore) create(wantsUI bool, ci *clientInfoJSON) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ui[id] = wantsUI
	if ci != nil && (ci.Name != "" || ci.Title != "") {
		s.client[id] = *ci
	}
	s.fifo = append(s.fifo, id)
	if len(s.fifo) > maxSessions {
		drop := s.fifo[0]
		s.fifo = s.fifo[1:]
		delete(s.ui, drop)
		delete(s.client, drop)
		delete(s.panelCalls, drop)
	}
	return id
}

// clientFor returns what this session said it was at initialize, or nil.
func (s *sessionStore) clientFor(r *http.Request) *clientInfoJSON {
	id := r.Header.Get("Mcp-Session-Id")
	if id == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ci, ok := s.client[id]
	if !ok {
		return nil
	}
	return &ci
}

func (s *sessionStore) wantsUI(r *http.Request) bool {
	id := r.Header.Get("Mcp-Session-Id")
	if id == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ui[id]
}

// notePanelCall records that a panel on this session reached the daemon.
func (s *sessionStore) notePanelCall(r *http.Request) {
	id := r.Header.Get("Mcp-Session-Id")
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, known := s.ui[id]; !known {
		// Never seen this session at initialize, so there is no entry to hang the
		// fact on and no eviction bookkeeping for it. Ignoring it is the safe
		// direction: the duplicate keeps being sent, which is merely expensive.
		return
	}
	s.panelCalls[id] = true
}

// panelFetches reports whether this session's panel has proved it can call
// tools. False until proved, because the expensive behaviour is the safe one.
func (s *sessionStore) panelFetches(r *http.Request) bool {
	id := r.Header.Get("Mcp-Session-Id")
	if id == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.panelCalls[id]
}

// isPanelCall reports whether a tools/call carries this panel's own marker.
func isPanelCall(params json.RawMessage) bool {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	v, ok := p.Meta["com.dibs/panel-call"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// declaresUI reports whether an initialize's capabilities include the MCP Apps
// extension. This is the client's own statement, made once, and the only
// authoritative source: no published matrix breaks capability down per client.
func declaresUI(params []byte) bool {
	var p struct {
		Capabilities struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	_, ok := p.Capabilities.Extensions["io.modelcontextprotocol/ui"]
	return ok
}
