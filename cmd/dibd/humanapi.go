package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humankey"
	"github.com/agenxy/dibs/internal/notify"
)

// The human relay's door: the person's own Mac, on a board that runs
// somewhere else. docs/NETWORK.md §8 is the argument; this is the wire.
//
//	POST /api/human/enroll     X-Dibs-Admin, {public_key, label}        → {key}
//	POST /api/human/challenge  {key}                                    → {nonce, node}
//	POST /api/human/session    {key, nonce, signature}                  → {session, expires}
//	GET  /api/human/stream     Bearer session                           → SSE of notices
//	POST /api/human/answer     Bearer session, {serial, disposition, body, nonce?, signature?}
//
// NONE of these take the board's secret, and that is the design rather than
// an omission: every agent on the board holds the secret, so it proves
// nothing about a person. Enrolment takes the admin password, under the same
// throttle as the web board's login; everything after it takes a signature
// from a key that signs only after Touch ID, or a session one opened.
//
// The gate lets /api/human/ through to these handlers (humanPath), which do
// their own authentication: the only routes besides /livez that it does.

// humanPrefix is the path every relay route lives under.
const humanPrefix = "/api/human/"

// humanPath reports whether a request is for the relay's routes, cleaned
// first like every other path the gate decides on.
func humanPath(p string) bool {
	return strings.HasPrefix(path.Clean("/"+p)+"/", humanPrefix)
}

type humanAPI struct {
	eng    *engine.Engine
	gate   *authGate
	keys   *humankey.Store
	ledger *humankey.Ledger
	now    func() time.Time
}

func registerHumanAPI(mux *http.ServeMux, eng *engine.Engine, gate *authGate, dir string) {
	h := &humanAPI{
		eng: eng, gate: gate, keys: humankey.NewStore(dir),
		ledger: humankey.NewLedger(), now: time.Now,
	}
	mux.HandleFunc("POST /api/human/enroll", h.enroll)
	mux.HandleFunc("POST /api/human/challenge", h.challenge)
	mux.HandleFunc("POST /api/human/session", h.session)
	mux.HandleFunc("GET /api/human/stream", h.stream)
	mux.HandleFunc("POST /api/human/answer", h.answer)
	mux.HandleFunc("POST /api/human/delivery", h.delivery)
}

// A receipt proves only what the relay reports about its own OS. It grants
// nothing, answers no mail and shares the existing authenticated relay session.
func (h *humanAPI) delivery(w http.ResponseWriter, r *http.Request) {
	key, ok := h.sessionKey(r)
	if !ok {
		humanRefuse(w, http.StatusUnauthorized, "no relay session", "open a session with dibs human-relay")
		return
	}
	var req struct {
		Serial            uint64          `json:"serial"`
		State             string          `json:"state"`
		Error             string          `json:"error"`
		Settings          json.RawMessage `json:"settings"`
		InterruptionLevel json.RawMessage `json:"interruption_level"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	var level string
	_ = json.Unmarshal(req.InterruptionLevel, &level)
	data := notify.ReceiptData{State: req.State, Settings: notify.DecodeSettings(req.Settings), InterruptionLevel: level}
	if err := h.eng.ReportHumanReceipt(r.Context(), req.Serial, key, data, req.Error); err != nil {
		humanRefuse(w, http.StatusBadRequest, err.Error(), "report posted, dismissed or failed for retained human mail")
		return
	}
	humanJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func humanJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func humanRefuse(w http.ResponseWriter, status int, why, hint string) {
	humanJSON(w, status, map[string]string{"error": why, "hint": hint})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		humanRefuse(w, http.StatusBadRequest, "the body did not parse: "+err.Error(),
			"send JSON, as `dibs human-relay` does")
		return false
	}
	return true
}

func (h *humanAPI) enroll(w http.ResponseWriter, r *http.Request) {
	if status, why, wait := h.gate.checkAdmin(r.Header.Get("X-Dibs-Admin")); status != 0 {
		if wait > 0 {
			w.Header().Set("Retry-After", strconvItoa(int(wait.Seconds())+1))
		}
		humanRefuse(w, status, why, "enrolling a relay takes this board's admin password")
		return
	}
	var req struct {
		PublicKey string `json:"public_key"`
		Label     string `json:"label"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	k, err := h.keys.Add(req.PublicKey, strings.TrimSpace(req.Label), h.now())
	if err != nil {
		humanRefuse(w, http.StatusBadRequest, err.Error(),
			"the key must come from `dibs human-relay enroll`, which makes it in this Mac's Secure Enclave")
		return
	}
	slog.Info("a human relay was enrolled", "key", k.ID, "label", k.Label)
	humanJSON(w, http.StatusOK, map[string]any{"key": k.ID, "node": h.eng.NodeID()})
}

func (h *humanAPI) challenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	// Only for an enrolled key, so a stranger cannot fill the nonce table.
	if _, err := h.keys.Find(req.Key); err != nil {
		humanRefuse(w, http.StatusUnauthorized, "this board has no enrolled key "+req.Key,
			"run `dibs human-relay enroll` against this board")
		return
	}
	n, err := h.ledger.Challenge(req.Key, h.now())
	if err != nil {
		humanRefuse(w, http.StatusServiceUnavailable, err.Error(), "try again shortly")
		return
	}
	humanJSON(w, http.StatusOK, map[string]any{"nonce": n, "node": h.eng.NodeID()})
}

// signed checks one signature from an enrolled key over msg, spending the
// nonce it covers. The nonce is spent whether or not the signature verifies,
// so a wrong guess cannot be retried against it.
func (h *humanAPI) signed(keyID, nonce, sig string, msg []byte) error {
	k, err := h.keys.Find(keyID)
	if err != nil {
		return err
	}
	if !h.ledger.Redeem(keyID, nonce, h.now()) {
		return errors.New("that challenge was not issued to this key, was already used, or expired")
	}
	return humankey.Verify(k, msg, sig)
}

func (h *humanAPI) session(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key       string `json:"key"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := h.signed(req.Key, req.Nonce, req.Signature,
		humankey.SessionMessage(h.eng.NodeID(), req.Nonce)); err != nil {
		humanRefuse(w, http.StatusUnauthorized, err.Error(), "ask for a fresh challenge and sign it")
		return
	}
	t, err := h.ledger.Open(req.Key, h.now())
	if err != nil {
		humanRefuse(w, http.StatusServiceUnavailable, err.Error(), "try again shortly")
		return
	}
	humanJSON(w, http.StatusOK, map[string]any{
		"session": t, "expires": h.now().Add(humankey.SessionTTL).UTC(),
	})
}

// sessionKey is the enrolled key behind a request's session, if it still
// stands: valid, and its key not revoked since.
func (h *humanAPI) sessionKey(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	key, ok := h.ledger.Session(token, h.now())
	if !ok {
		return "", false
	}
	if _, err := h.keys.Find(key); err != nil {
		h.ledger.Revoke(key)
		return "", false
	}
	return key, true
}

// heartbeat keeps a quiet stream from being closed by whatever sits between
// the relay and the board, and is when a revoked key is noticed.
var heartbeat = 25 * time.Second

func (h *humanAPI) stream(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.sessionKey(r); !ok {
		humanRefuse(w, http.StatusUnauthorized, "no relay session",
			"open one: POST /api/human/challenge, sign it, POST /api/human/session")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		humanRefuse(w, http.StatusInternalServerError, "this connection cannot stream", "")
		return
	}
	// Attach BEFORE reading what is pending, so nothing that arrives between
	// the two is lost. A notice can then arrive twice, which a relay drops.
	feed, detach := h.eng.AttachHumanRelay()
	defer detach()
	pending, err := h.initialHumanNotices(r.Context())
	if err != nil {
		humanRefuse(w, http.StatusServiceUnavailable, err.Error(), "try again shortly")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	send := func(n engine.HumanNotice) bool {
		b, _ := json.Marshal(n)
		_, err := fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		return err == nil
	}
	for _, n := range pending {
		if !send(n) {
			return
		}
	}
	_, _ = fmt.Fprint(w, ": attached\n\n")
	flusher.Flush()
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case n, open := <-feed:
			if !open || !send(n) {
				return
			}
		case <-tick.C:
			if _, ok := h.sessionKey(r); !ok {
				return // revoked or expired: the relay re-authenticates
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *humanAPI) initialHumanNotices(ctx context.Context) ([]engine.HumanNotice, error) {
	pending, err := h.eng.PendingForHuman(ctx)
	if err != nil {
		return nil, err
	}
	cleanup, err := h.eng.HumanCleanupForRelay(ctx)
	if cleanup != nil {
		pending = append(pending, engine.HumanNotice{Cleanup: cleanup})
	}
	return pending, err
}

func (h *humanAPI) answer(w http.ResponseWriter, r *http.Request) {
	key, ok := h.sessionKey(r)
	if !ok {
		humanRefuse(w, http.StatusUnauthorized, "no relay session",
			"open one: POST /api/human/challenge, sign it, POST /api/human/session")
		return
	}
	var req struct {
		Serial      uint64 `json:"serial"`
		Disposition string `json:"disposition"`
		Body        string `json:"body"`
		Nonce       string `json:"nonce"`
		Signature   string `json:"signature"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	n, open, err := h.eng.HumanNoticeFor(r.Context(), req.Serial)
	if err != nil {
		humanRefuse(w, http.StatusNotFound, err.Error(), "a relay answers only mail addressed to the human")
		return
	}
	if !open {
		humanRefuse(w, http.StatusConflict, "already answered",
			"somebody answered it first, on the board or on another relay")
		return
	}
	// A grant needs a finger on THIS answer, not on the session: a process
	// that stole a session can answer a question and cannot make itself
	// coordinator.
	if n.Privileged() && req.Disposition == "approve" {
		if req.Nonce == "" || req.Signature == "" {
			humanRefuse(w, http.StatusUnauthorized, "approving this grants something and needs a fresh signature",
				"ask for a challenge and sign dibs-human-answer/v1 for this serial and disposition")
			return
		}
		if err := h.signed(key, req.Nonce, req.Signature,
			humankey.AnswerMessage(h.eng.NodeID(), req.Serial, req.Disposition, req.Nonce)); err != nil {
			humanRefuse(w, http.StatusUnauthorized, err.Error(), "ask for a fresh challenge and sign it")
			return
		}
	}
	if err := h.eng.AnswerAsHuman(r.Context(), req.Serial, req.Disposition, req.Body); err != nil {
		humanRefuse(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	slog.Info("the human answered from a relay", "msg", req.Serial, "disposition", req.Disposition, "key", key)
	humanJSON(w, http.StatusOK, map[string]any{"ok": true})
}
