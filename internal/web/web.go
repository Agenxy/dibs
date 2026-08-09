// Package web serves the human window: a server-rendered board over SSE.
// Package web serves the operator's god view of the board: every lane, every
// claim, all mail, and the ledger tail.
//
// Go owns all STATE — the ledger and the engine are the only source of truth,
// and this server holds none of its own. It no longer owns the templates: the
// board is rendered by the shared components in internal/assets, the same ones
// the MCP Apps panel uses, because the panel physically cannot be
// server-rendered (it lives in a sandboxed iframe whose CSP forbids any network
// call) and two renderers for one board is how the surfaces drifted apart.
//
// The stream therefore carries data rather than markup, which is also why the
// 53 KB of vendored htmx this used to need is gone.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/lanes/internal/assets"
	"github.com/agenxy/lanes/internal/build"
	"github.com/agenxy/lanes/internal/core"
	"github.com/agenxy/lanes/internal/engine"
)

//go:embed templates
var content embed.FS

// Server renders the board UI and admin JSON APIs.
type Server struct {
	eng  *engine.Engine
	tmpl *template.Template
	log  *eventLog
}

// New builds the web server over eng.
func New(eng *engine.Engine) (*Server, error) {
	funcs := template.FuncMap{
		// The shared design system and component library, inlined. Both this
		// board and the MCP Apps panel use these, so the two surfaces cannot
		// drift into looking like different products. Inlined rather than
		// served because the panel's CSP forbids external origins and the two
		// must stay byte-identical in what they load.
		// #nosec G203 -- not attacker-controlled: these are vendored assets embedded
		// at compile time from this repository, or a string already passed through
		// template.HTMLEscapeString on the line itself.
		"styles": func() template.CSS { return template.CSS(assets.Styles()) },
		// #nosec G203 -- not attacker-controlled: these are vendored assets embedded
		// at compile time from this repository, or a string already passed through
		// template.HTMLEscapeString on the line itself.
		"boardJS": func() template.JS { return template.JS(assets.BoardJS()) },
		"ago":     humanAgo,
		"msgGlyph": func(t string) string {
			switch t {
			case "question":
				return "?"
			case "request":
				return "⚑"
			case "handoff":
				return "⇥"
			}
			return "•"
		},
		"stateTag": stateTag,
		"evClass": func(t string) string {
			if i := strings.IndexByte(t, '.'); i > 0 {
				return t[:i]
			}
			return t
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{eng: eng, tmpl: tmpl, log: newEventLog(512)}, nil
}

// Register mounts all web routes on mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.index)
	// A board people leave open all day beside their editor. Without this the
	// tab shows the browser's blank-page glyph, which is what an unfinished tool
	// looks like.
	mux.HandleFunc("GET /icon.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(assets.Icon))
	})
	mux.HandleFunc("GET /help", s.help)
	mux.HandleFunc("GET /events", s.sse)
	mux.HandleFunc("GET /api/board", s.apiBoard)
	mux.HandleFunc("GET /api/messages", s.apiMessages)
	// Why the Lanes tab is empty. Without this a board with matching switched
	// off looks identical to one where nobody is colliding.
	mux.HandleFunc("GET /api/matching", func(w http.ResponseWriter, r *http.Request) {
		writeActJSON(w, http.StatusOK, s.eng.MatchStatus())
	})
	// The human's write surface. Same session gate as the board itself.
	s.registerActions(mux)
}

func (s *Server) help(w http.ResponseWriter, _ *http.Request) {
	if err := s.tmpl.ExecuteTemplate(w, "help.html", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// boardJSON is the payload both the bootstrap and the stream carry: the same
// shape the MCP Apps panel receives, so one set of components renders both.
func (s *Server) boardJSON(r *http.Request) ([]byte, error) {
	b, _, err := s.boardJSONAt(r)
	return b, err
}

// boardJSONAt also reports the serial the snapshot represents, so an SSE frame
// can advertise an id a client can actually resume from.
func (s *Server) boardJSONAt(r *http.Request) ([]byte, uint64, error) {
	board, err := s.eng.Board(r.Context())
	if err != nil {
		return nil, 0, err
	}
	serial, _ := board["serial"].(uint64)
	msgs, err := s.eng.AllMessages(r.Context())
	if err != nil {
		return nil, 0, err
	}
	out, err := json.Marshal(map[string]any{
		"board":    map[string]any(board),
		"messages": msgs["messages"],
		"events":   s.log.recent(50),
	})
	return out, serial, err
}

// indexData is the first paint: the same JSON the stream carries, embedded so
// the board is on screen before EventSource delivers anything. Split into three
// fields because template.JS marks each as already-safe JSON.
func (s *Server) indexData(r *http.Request) (map[string]any, error) {
	board, err := s.eng.Board(r.Context())
	if err != nil {
		return nil, err
	}
	msgs, err := s.eng.AllMessages(r.Context())
	if err != nil {
		return nil, err
	}
	js := func(v any) (template.JS, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		// #nosec G203 -- `b` is a vendored asset embedded at compile time from this
		// repository, not request data.
		return template.JS(b), nil
	}
	boardJSON, err := js(map[string]any(board))
	if err != nil {
		return nil, err
	}
	msgJSON, err := js(msgs["messages"])
	if err != nil {
		return nil, err
	}
	evJSON, err := js(s.log.recent(50))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"BoardJSON":    boardJSON,
		"MessagesJSON": msgJSON,
		"EventsJSON":   evJSON,
		"Version":      build.Version,
	}, nil
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	data, err := s.indexData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// StillAuthorized is how a long-lived handler asks whether the request that
// opened it is STILL allowed to be here.
//
// The auth gate runs once, when a request enters it. An SSE stream is one
// request that then lives for hours, so a god-view connection opened a second
// before its session expired kept delivering decrypted mail indefinitely.
// Verified with a shortened TTL: a fresh request with the same cookie got 401
// while the already-open stream carried a message sent after the deadline.
//
// The gate puts a revalidation func here; a handler with none (a test, or a
// deployment with no gate) is treated as authorized, because this must never be
// the thing that breaks streaming.
type ctxKeyStillAuthorized struct{}

// WithRevalidator attaches the check. Called by the auth gate.
func WithRevalidator(ctx context.Context, ok func() bool) context.Context {
	return context.WithValue(ctx, ctxKeyStillAuthorized{}, ok)
}

func stillAuthorized(ctx context.Context) bool {
	ok, _ := ctx.Value(ctxKeyStillAuthorized{}).(func() bool)
	return ok == nil || ok()
}

// sse streams the board as JSON. Last-Event-ID (the serial) makes browser
// reconnects gapless for free.
func (s *Server) sse(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	since := uint64(0)
	if lei := r.Header.Get("Last-Event-ID"); lei != "" {
		since, _ = strconv.ParseUint(lei, 10, 64)
	}
	ch, cancel := s.eng.Subscribe(since)
	defer cancel()

	// The stream carries DATA, not markup. It used to push a rendered fragment
	// for htmx to swap in, which meant this surface had its own renderer and
	// drifted from the panel's. Both now receive the same shape and render it
	// with the same shared components.
	// A snapshot frame must advertise the serial it REPRESENTS, not the one the
	// client connected at.
	//
	// The initial frame and the 30-second refresh both sent `since`, which is
	// fixed at connect time — so on an idle board a client's Last-Event-ID never
	// advanced, and on a busy one it was reset backwards every 30 seconds. On
	// reconnect the client then asked for everything from that stale point, and
	// the replay is a non-blocking send into a 256-slot channel: the excess is
	// dropped silently. The board itself was never wrong (every frame is a full
	// snapshot), but the id was useless as a resume point, which is the one job
	// it has.
	sendSnapshot := func() bool {
		payload, at, err := s.boardJSONAt(r)
		if err != nil {
			return false
		}
		// #nosec G705 -- an SSE frame, not HTML; see the note on send below.
		_, _ = fmt.Fprintf(w, "id: %d\nevent: board\ndata: %s\n\n", at, payload)
		fl.Flush()
		return true
	}
	send := func(serial uint64) bool {
		payload, err := s.boardJSON(r)
		if err != nil {
			return false
		}
		// SSE frames are newline-delimited, so the JSON must not contain a raw
		// newline. encoding/json never emits one inside a value.
		// #nosec G705 -- this is an SSE frame (text/event-stream), not HTML, and the
		// payload is encoding/json output the browser JSON.parses. Rendering escapes
		// separately in board.js; nothing here reaches innerHTML.
		_, _ = fmt.Fprintf(w, "id: %d\nevent: board\ndata: %s\n\n", serial, payload)
		fl.Flush()
		return true
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	// Relative timestamps stay honest without any client JS: push a fresh
	// frame every 30s even when nothing happened.
	refresh := time.NewTicker(30 * time.Second)
	defer refresh.Stop()
	// Coalesce bursts: render at most every 200ms.
	var pending *core.Event
	render := time.NewTicker(200 * time.Millisecond)
	defer render.Stop()

	if !sendSnapshot() {
		return
	}
	for {
		// A deadline checked only at the door is not a deadline. Re-asked on
		// every wake, so an expired session stops receiving decrypted mail
		// within one tick rather than whenever the browser happens to reconnect.
		if !stillAuthorized(r.Context()) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			s.log.add(ev)
			pending = &ev
		case <-render.C:
			if pending != nil {
				if !send(pending.Serial) {
					return
				}
				pending = nil
			}
		case <-refresh.C:
			if !sendSnapshot() {
				return
			}
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) apiBoard(w http.ResponseWriter, r *http.Request) {
	res, err := s.eng.Board(r.Context())
	writeJSON(w, res, err)
}

func (s *Server) apiMessages(w http.ResponseWriter, r *http.Request) {
	res, err := s.eng.AllMessages(r.Context())
	writeJSON(w, res, err)
}
