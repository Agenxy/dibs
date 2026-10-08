// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
// This identifier applies only to Agenxy-authored portions.
// Outside contributions retain their original licences; see NOTICE.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humankey"
)

// relayHarness is a board with the human relay's door open, behind the real
// gate, and a software key standing in for the Secure Enclave.
type relayHarness struct {
	t      *testing.T
	eng    *engine.Engine
	srv    *httptest.Server
	priv   *ecdsa.PrivateKey
	pub    string
	key    string
	secret string
}

func newRelayHarness(t *testing.T) *relayHarness {
	t.Helper()
	eng, _ := testEngine(t)
	dir := t.TempDir()
	hash, err := adminpw.Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.hash"), []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	const secret = "board-secret"
	gate := newAuthGate(secret, filepath.Join(dir, "admin.hash"), "127.0.0.1:4777")
	mux := http.NewServeMux()
	registerHumanAPI(mux, eng, gate, dir)
	srv := httptest.NewServer(gate.wrap(mux))
	t.Cleanup(srv.Close)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	return &relayHarness{
		t: t, eng: eng, srv: srv, priv: priv,
		pub: base64.StdEncoding.EncodeToString(der), secret: secret,
	}
}

func (h *relayHarness) post(path string, hdr map[string]string, body any) (int, map[string]any) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *relayHarness) sign(msg []byte) string {
	d := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, h.priv, d[:])
	if err != nil {
		h.t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func (h *relayHarness) enroll() {
	h.t.Helper()
	code, out := h.post("/api/human/enroll", map[string]string{"X-Dibs-Admin": "correct horse"},
		map[string]string{"public_key": h.pub, "label": "laptop"})
	if code != http.StatusOK {
		h.t.Fatalf("enrol: %d %v", code, out)
	}
	h.key = out["key"].(string)
}

// challenge returns a nonce and the node it was issued on.
func (h *relayHarness) challenge() (string, string) {
	h.t.Helper()
	code, out := h.post("/api/human/challenge", nil, map[string]string{"key": h.key})
	if code != http.StatusOK {
		h.t.Fatalf("challenge: %d %v", code, out)
	}
	return out["nonce"].(string), out["node"].(string)
}

func (h *relayHarness) session() string {
	h.t.Helper()
	nonce, node := h.challenge()
	code, out := h.post("/api/human/session", nil, map[string]string{
		"key": h.key, "nonce": nonce, "signature": h.sign(humankey.SessionMessage(node, nonce)),
	})
	if code != http.StatusOK {
		h.t.Fatalf("session: %d %v", code, out)
	}
	return out["session"].(string)
}

// attach opens the stream and returns the notices it carries, one per read.
func (h *relayHarness) attach(ctx context.Context, session string) <-chan engine.HumanNotice {
	h.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/api/human/stream", nil)
	req.Header.Set("Authorization", "Bearer "+session)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		h.t.Fatalf("stream: HTTP %d", resp.StatusCode)
	}
	out := make(chan engine.HumanNotice, 16)
	attached := make(chan struct{})
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if line == ": attached" {
				close(attached)
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var n engine.HumanNotice
				if json.Unmarshal([]byte(data), &n) == nil {
					out <- n
				}
			}
		}
	}()
	select {
	case <-attached:
	case <-time.After(5 * time.Second):
		h.t.Fatal("the stream never said it was attached")
	}
	return out
}

// agentSends registers an agent and has it send one message to the person.
func (h *relayHarness) agentSends(name string, op core.Op) uint64 {
	h.t.Helper()
	ctx := context.Background()
	human, _, err := h.eng.HumanAgent(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	reg, err := h.eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: name, Nonce: "n-" + name})
	if err != nil || reg["token"] == nil {
		h.t.Fatalf("register %s: %v %v", name, err, reg)
	}
	op.Kind, op.Token, op.To = core.OpSendMessage, reg["token"].(string), human
	res, err := h.eng.Do(ctx, &op)
	if err != nil || res["error"] != nil {
		h.t.Fatalf("send: %v %v", err, res)
	}
	serial, _ := res["msg_serial"].(uint64)
	if serial == 0 {
		h.t.Fatalf("send returned no serial: %v", res)
	}
	return serial
}

func next(t *testing.T, feed <-chan engine.HumanNotice) engine.HumanNotice {
	t.Helper()
	select {
	case n := <-feed:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no notice reached the relay")
		return engine.HumanNotice{}
	}
}

func stateOf(t *testing.T, eng *engine.Engine, serial uint64) string {
	t.Helper()
	n, open, err := eng.HumanNoticeFor(context.Background(), serial)
	if err != nil {
		t.Fatal(err)
	}
	_ = n
	if open {
		return "open"
	}
	return "closed"
}

// The whole path a person on another machine takes: enrol with the admin
// password, open a session with a signature, receive a question as it is
// sent, answer it, and see it answered on the board.
func TestAPersonOnAnotherMachineIsAskedAndAnswers(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed := h.attach(ctx, h.session())

	serial := h.agentSends("asker", core.Op{MsgType: core.MsgQuestion, Body: "ship it?", Choices: []string{"yes", "no"}})
	n := next(t, feed)
	if n.Serial != serial || n.Body != "ship it?" || len(n.Choices) != 2 {
		t.Fatalf("the relay got %+v, want the question %d", n, serial)
	}
	session := h.session()
	code, out := h.post("/api/human/answer", map[string]string{"Authorization": "Bearer " + session},
		map[string]any{"serial": serial, "disposition": "answer", "body": "yes"})
	if code != http.StatusOK {
		t.Fatalf("answer: %d %v", code, out)
	}
	if got := stateOf(t, h.eng, serial); got != "closed" {
		t.Errorf("the question is %s after the person answered", got)
	}
	// Answered once is answered: a second relay is told so.
	code, _ = h.post("/api/human/answer", map[string]string{"Authorization": "Bearer " + session},
		map[string]any{"serial": serial, "disposition": "answer", "body": "no"})
	if code != http.StatusConflict {
		t.Errorf("a second answer was answered %d, want 409", code)
	}
}

// Mail that arrived while no relay was attached is handed over on attach.
func TestARelayIsHandedWhatArrivedWhileItWasAway(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	serial := h.agentSends("early", core.Op{MsgType: core.MsgRequest, Body: "may I deploy?"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if n := next(t, h.attach(ctx, h.session())); n.Serial != serial {
		t.Fatalf("the relay was handed %d, want the pending request %d", n.Serial, serial)
	}
}

// A grant needs a finger on the answer itself, not just a session: a process
// that stole a session can answer a question and cannot promote itself.
func TestApprovingAGrantNeedsAFreshSignature(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	serial := h.agentSends("climber", core.Op{MsgType: core.MsgRequest, Body: "routine", Grant: core.RoleCoordinator})
	auth := map[string]string{"Authorization": "Bearer " + h.session()}

	code, _ := h.post("/api/human/answer", auth, map[string]any{"serial": serial, "disposition": "approve"})
	if code != http.StatusUnauthorized {
		t.Fatalf("a grant approved on the session alone was answered %d", code)
	}
	nonce, node := h.challenge()
	// A signature for a different disposition does not approve.
	code, _ = h.post("/api/human/answer", auth, map[string]any{
		"serial": serial, "disposition": "approve", "nonce": nonce,
		"signature": h.sign(humankey.AnswerMessage(node, serial, "deny", nonce)),
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("a signature for deny approved the grant: %d", code)
	}
	if stateOf(t, h.eng, serial) != "open" {
		t.Fatal("the grant was applied without the right signature")
	}
	nonce, node = h.challenge()
	code, out := h.post("/api/human/answer", auth, map[string]any{
		"serial": serial, "disposition": "approve", "nonce": nonce,
		"signature": h.sign(humankey.AnswerMessage(node, serial, "approve", nonce)),
	})
	if code != http.StatusOK {
		t.Fatalf("a signed approval was answered %d %v", code, out)
	}
	// Denying needs no finger: refusing grants nothing.
	serial2 := h.agentSends("climber2", core.Op{MsgType: core.MsgRequest, Body: "again", Grant: core.RoleCoordinator})
	if code, out := h.post("/api/human/answer", auth, map[string]any{"serial": serial2, "disposition": "deny"}); code != http.StatusOK {
		t.Errorf("a denial on the session was answered %d %v", code, out)
	}
}

// The board's secret, which every agent holds, opens none of this.
func TestTheBoardsSecretIsNotAPerson(t *testing.T) {
	h := newRelayHarness(t)
	asAgent := map[string]string{"X-Dibs-Local": h.secret, "Authorization": "Bearer " + h.secret}
	if code, _ := h.post("/api/human/enroll", asAgent, map[string]string{"public_key": h.pub}); code != http.StatusUnauthorized {
		t.Errorf("enrolment with the board's secret and no password was answered %d", code)
	}
	if code, _ := h.post("/api/human/enroll", map[string]string{"X-Dibs-Admin": "wrong"},
		map[string]string{"public_key": h.pub}); code != http.StatusUnauthorized {
		t.Errorf("enrolment with the wrong password was answered %d", code)
	}
	h.enroll()
	serial := h.agentSends("asker", core.Op{MsgType: core.MsgQuestion, Body: "?"})
	if code, _ := h.post("/api/human/answer", asAgent, map[string]any{"serial": serial, "disposition": "answer", "body": "x"}); code != http.StatusUnauthorized {
		t.Errorf("an answer with the board's secret was answered %d", code)
	}
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/human/stream", nil)
	req.Header.Set("Authorization", "Bearer "+h.secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the stream opened for the board's secret: %d", resp.StatusCode)
	}
	// A session signature from a key this board never enrolled opens nothing.
	stranger, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	nonce, node := h.challenge()
	d := sha256.Sum256(humankey.SessionMessage(node, nonce))
	sig, _ := ecdsa.SignASN1(rand.Reader, stranger, d[:])
	if code, _ := h.post("/api/human/session", nil, map[string]string{
		"key": h.key, "nonce": nonce, "signature": base64.StdEncoding.EncodeToString(sig),
	}); code != http.StatusUnauthorized {
		t.Errorf("a stranger's signature opened a session: %d", code)
	}
}
