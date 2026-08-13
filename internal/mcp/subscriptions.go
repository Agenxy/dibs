package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// rpcErrFrom maps a core error to a JSON-RPC error, preserving the code/hint.
func rpcErrFrom(err error) *rpcError {
	var ce *core.Error
	if errors.As(err, &ce) {
		return &rpcError{Code: -32000, Message: ce.Msg, Data: map[string]any{"code": ce.Code, "hint": ce.Hint}}
	}
	return &rpcError{Code: -32603, Message: err.Error()}
}

// metaTokenKey names the _meta field that carries an agent token in a
// subscriptions/listen (or resources/read) request, so an inbox subscription can
// be scoped to the caller's agent. The daemon connection is already gated by the
// local secret; this token identifies WHICH agent's mailbox to watch. It is a
// field NAME, not a secret value.
const metaTokenKey = "com.dibs/token" //nolint:gosec // G101: metadata key name, not a credential

// subscriptionParams is the SEP-2575 subscriptions/listen params shape.
type subscriptionParams struct {
	Meta          map[string]any `json:"_meta"`
	Notifications struct {
		ToolsListChanged      bool     `json:"toolsListChanged"`
		PromptsListChanged    bool     `json:"promptsListChanged"`
		ResourcesListChanged  bool     `json:"resourcesListChanged"`
		ResourceSubscriptions []string `json:"resourceSubscriptions"`
	} `json:"notifications"`
}

// sseStream writes JSON-RPC messages as SSE `data:` events on a flushed writer.
type sseStream struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func (s sseStream) send(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return false
	}
	s.fl.Flush()
	return true
}

func (s sseStream) comment() bool {
	if _, err := fmt.Fprint(s.w, ": keepalive\n\n"); err != nil {
		return false
	}
	s.fl.Flush()
	return true
}

// serveSubscription implements SEP-2575 `subscriptions/listen` over HTTP: the
// POST response is a long-lived SSE stream whose first message is
// notifications/subscriptions/acknowledged, followed by
// notifications/resources/updated whenever a subscribed resource changes. This
// is the spec-native push path (the future-proof successor to await_events);
// whether a host surfaces the notification to the model is the host's call, but
// the server half is standards-correct and harmless when unused.
//
// Dibs honors two resource URIs:
//   - dibs://board : any board change (agents/slots/claims). No token needed.
//   - dibs://inbox : mail to the caller's agent. Requires an agent token in
//     _meta[com.dibs/token] so it can be scoped and access-checked.
func (s *Server) serveSubscription(w http.ResponseWriter, r *http.Request, req *rpcRequest) {
	var p subscriptionParams
	_ = json.Unmarshal(req.Params, &p)

	wantBoard := containsStr(p.Notifications.ResourceSubscriptions, "dibs://board")
	wantInbox := containsStr(p.Notifications.ResourceSubscriptions, "dibs://inbox")

	// Resolve the agent (for inbox scoping) and the current serial in one call.
	token := ""
	if wantInbox {
		token, _ = p.Meta[metaTokenKey].(string)
		if token == "" {
			writeRPC(w, http.StatusBadRequest, req.ID, nil, &rpcError{
				Code: -32602, Message: "dibs://inbox subscription requires an agent token in _meta['" + metaTokenKey + "']",
			})
			return
		}
	}
	agentID, since, err := s.eng.SubscribeInfo(r.Context(), token)
	if err != nil {
		writeRPC(w, http.StatusOK, req.ID, nil, rpcErrFrom(err))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPC(w, http.StatusInternalServerError, req.ID, nil,
			&rpcError{Code: -32603, Message: "streaming not supported by this server"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	stream := sseStream{w: w, fl: flusher}

	// SEP-2575: acknowledge first, echoing only the notification types honored.
	honored := map[string]any{}
	if subs := honoredURIs(wantBoard, wantInbox); len(subs) > 0 {
		honored["resourceSubscriptions"] = subs
	}
	stream.send(notification("notifications/subscriptions/acknowledged", map[string]any{"notifications": honored}, req.ID))

	ch, cancel := s.eng.Subscribe(since)
	defer cancel()
	// Fixed for the lifetime of the stream: 2026-07-28 carries the whole
	// subscription in the listen call, so there is nothing to re-read.
	s.pump(r, stream, ch, req.ID, func() (string, bool, bool) {
		return agentID, wantInbox, wantBoard
	})
}

// pump forwards resource-change notifications until the client disconnects.
// wantsFunc reports what a stream should deliver RIGHT NOW: the agent whose
// inbox it follows, and whether it follows the inbox and the board at all.
//
// A function rather than three values captured when the stream opened, because
// the legacy transport lets a client subscribe and unsubscribe while its stream
// is already open. Captured values meant a subscribe AFTER the GET was never
// noticed and an unsubscribe never took effect: the stream kept delivering what
// the client had asked to stop hearing, and stayed silent about what it had
// just asked for.
type wantsFunc func() (agentID string, inbox, board bool)

func (s *Server) pump(r *http.Request, stream sseStream, ch <-chan core.Event,
	subID json.RawMessage, wants wantsFunc,
) {
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done(): // client closed the stream = cancellation (SEP-2575)
			return
		case <-keepalive.C:
			if !stream.comment() {
				return
			}
		case ev, open := <-ch:
			if !open {
				return
			}
			agentID, wantInbox, wantBoard := wants()
			if uri := matchedURI(ev, agentID, wantInbox, wantBoard); uri != "" {
				if !stream.send(resourceUpdated(uri, subID)) {
					return
				}
			}
		}
	}
}

// matchedURI returns the subscribed resource URI an event changed, or "".
func matchedURI(ev core.Event, agentID string, wantInbox, wantBoard bool) string {
	if wantInbox && ev.To == agentID && strings.HasPrefix(ev.Type, "message.") {
		return "dibs://inbox"
	}
	if wantBoard && isBoardEvent(ev) {
		return "dibs://board"
	}
	return ""
}

func honoredURIs(wantBoard, wantInbox bool) []string {
	subs := make([]string, 0, 2)
	if wantBoard {
		subs = append(subs, "dibs://board")
	}
	if wantInbox {
		subs = append(subs, "dibs://inbox")
	}
	return subs
}

func isBoardEvent(ev core.Event) bool {
	for _, pfx := range []string{"agent.", "slot.", "claim.", "board."} {
		if strings.HasPrefix(ev.Type, pfx) {
			return true
		}
	}
	return false
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// notification builds a JSON-RPC notification, tagging it with the subscription
// id in _meta so STDIO clients can demultiplex (SEP-2575).
func notification(method string, params map[string]any, subID json.RawMessage) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	if len(subID) > 0 {
		meta["io.modelcontextprotocol/subscriptionId"] = subID
	}
	params["_meta"] = meta
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

func resourceUpdated(uri string, subID json.RawMessage) map[string]any {
	return notification("notifications/resources/updated", map[string]any{"uri": uri}, subID)
}
