// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/web"
)

type boardMediaFixture struct {
	t       *testing.T
	eng     *engine.Engine
	ctx     context.Context
	handler http.Handler
	cookie  *http.Cookie
	key     string
	agent   string
	human   string
}

// Real storage, writer, board mux and auth gate. A session is obtained through
// the password bootstrap and redemption HTTP routes, never a planted cookie.
func newBoardMediaFixture(t *testing.T) *boardMediaFixture {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "board-media", box)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	limits := core.DefaultLimits()
	limits.MaxBlobSize, limits.BlobStoreBytes = 64, 128
	eng := engine.New(core.NewState("board-media", limits), led, nil)
	eng.SetBlobs(store)
	ctx, cancel := context.WithCancel(t.Context())
	go eng.Run(ctx)
	t.Cleanup(func() { cancel(); <-eng.Done(); _ = led.Close() })
	hash, err := adminpw.Hash("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(dir, "admin.hash")
	if err := os.WriteFile(passwordFile, []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := newAuthGate("fixture-secret", passwordFile, "127.0.0.1:4777")
	board, err := web.New(eng)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	board.Register(mux)
	f := &boardMediaFixture{t: t, eng: eng, ctx: ctx, handler: gate.wrap(mux)}
	boot := f.call("POST", "/bootstrap", nil, "", map[string]string{
		"X-Dibs-Local": "fixture-secret", "X-Dibs-Admin": "fixture-password",
	})
	if boot.Code != 200 {
		t.Fatalf("setup bootstrap: %d %s", boot.Code, boot.Body)
	}
	bt, _ := f.json(boot)["bt"].(string)
	if bt == "" {
		t.Fatal("setup: bootstrap returned no token")
	}
	redeem := f.call("GET", "/?bt="+url.QueryEscape(bt), nil, "", nil)
	response := redeem.Result()
	defer func() { _ = response.Body.Close() }()
	if redeem.Code != http.StatusSeeOther || len(response.Cookies()) != 1 {
		t.Fatalf("setup redemption: %d %s", redeem.Code, redeem.Body)
	}
	f.cookie = response.Cookies()[0]
	location, err := url.Parse(redeem.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(location.Fragment, "k=") {
		t.Fatalf("setup page key: %v %v", location, err)
	}
	f.key = strings.TrimPrefix(location.Fragment, "k=")
	r := f.op(&core.Op{Kind: core.OpRegister, Name: "asker", Nonce: "asker-nonce"})
	f.agent = r["token"].(string)
	f.human, _, err = eng.HumanAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *boardMediaFixture) call(method, path string, body []byte, authority string, headers map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:4777"+path, bytes.NewReader(body))
	if authority == "secret" {
		r.Header.Set("X-Dibs-Local", "fixture-secret")
	}
	if authority == "page" || authority == "cookie" {
		r.AddCookie(f.cookie)
		r.Header.Set("Origin", "http://127.0.0.1:4777")
	}
	if authority == "page" {
		r.Header.Set(pageKeyHeader, f.key)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f *boardMediaFixture) json(w *httptest.ResponseRecorder) map[string]any {
	f.t.Helper()
	var value map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		f.t.Fatalf("response is not JSON: %d %s: %v", w.Code, w.Body, err)
	}
	return value
}

func (f *boardMediaFixture) op(op *core.Op) core.Result {
	f.t.Helper()
	r, err := f.eng.Do(f.ctx, op)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *boardMediaFixture) mail(data []byte, mime string) (uint64, string) {
	f.t.Helper()
	blob, err := f.eng.PutBlob(f.ctx, f.agent, data, "", mime)
	if err != nil {
		f.t.Fatal(err)
	}
	id := blob["blob"].(string)
	message := f.op(&core.Op{
		Kind: core.OpSendMessage, Token: f.agent, To: f.human,
		MsgType: core.MsgQuestion, Body: "see this file", Attachments: []core.Attachment{{Blob: id}},
	})
	return message["msg_serial"].(uint64), id
}

func TestBoardMediaRoutesRequireTheRealPageAuthority(t *testing.T) {
	f := newBoardMediaFixture(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/messages/1/attachments/0"},
		{"POST", "/api/upload"},
		{"GET", "/api//messages/1/attachments/0"},
		{"POST", "/x/../api/upload"},
	} {
		for _, authority := range []string{"secret", "cookie"} {
			w := f.call(route.method, route.path, []byte("file"), authority, nil)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Errorf("%s %s accepted %s without the page authority: %d", route.method, route.path, authority, w.Code)
			}
		}
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/messages/1/attachments/0"}, {"POST", "/api/upload"},
	} {
		w := f.call(route.method, route.path, []byte("file"), "page", map[string]string{"Origin": "http://elsewhere.invalid"})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s accepted a foreign origin: %d", route.path, w.Code)
		}
	}
}

func TestBoardMediaHasSafeTypesAndRealRanges(t *testing.T) {
	f := newBoardMediaFixture(t)
	for _, mime := range []string{"image/png", "video/mp4", "image/svg+xml", "text/html"} {
		t.Run(mime, func(t *testing.T) {
			data := []byte("content:" + mime)
			serial, _ := f.mail(data, mime)
			path := fmt.Sprintf("/api/messages/%d/attachments/0", serial)
			w := f.call("GET", path, nil, "page", map[string]string{"Range": "bytes=1-4"})
			if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), data[1:5]) {
				t.Fatalf("real attachment Range failed: %d %s", w.Code, w.Body)
			}
			kind, contentType, disposition := engine.MediaKind(mime), mime, "inline"
			if kind == "" {
				contentType, disposition = "application/octet-stream", "attachment"
			}
			if w.Header().Get("Content-Type") != contentType || !strings.HasPrefix(w.Header().Get("Content-Disposition"), disposition) {
				t.Errorf("unsafe registry type/disposition: %v", w.Header())
			}
			for key, want := range map[string]string{
				"X-Content-Type-Options": "nosniff", "Cache-Control": "no-store",
				"Content-Security-Policy": "default-src 'none'; sandbox", "X-Dibs-Media-Kind": kind,
			} {
				if w.Header().Get(key) != want {
					t.Errorf("%s = %q, want %q", key, w.Header().Get(key), want)
				}
			}
		})
	}
}

func TestBoardMissingAttachmentHasACorrectiveHint(t *testing.T) {
	f := newBoardMediaFixture(t)
	serial, _ := f.mail([]byte("file"), "image/png")
	for _, path := range []string{
		"/api/messages/999999/attachments/0",
		fmt.Sprintf("/api/messages/%d/attachments/1", serial),
		fmt.Sprintf("/api/messages/%d/attachments/-1", serial),
		"/api/messages/not-a-serial/attachments/0",
	} {
		w := f.call("GET", path, nil, "page", nil)
		if w.Code != http.StatusNotFound || f.json(w)["hint"] == "" {
			t.Errorf("missing attachment has no corrective 404: %s: %d %s", path, w.Code, w.Body)
		}
	}
}

func TestBoardUploadsAndRepliesUseOrdinaryAttachments(t *testing.T) {
	f := newBoardMediaFixture(t)
	data := []byte("the person's concept file")
	up := f.call("POST", "/api/upload", data, "page", map[string]string{"Content-Type": "image/png"})
	if up.Code != 200 {
		t.Fatalf("page upload failed: %d %s", up.Code, up.Body)
	}
	blob := f.json(up)["blob"].(string)
	for _, tc := range []struct {
		name, mime string
		data       []byte
		status     int
	}{
		{"size", "application/octet-stream", bytes.Repeat([]byte("x"), 65), 413},
		{"type", "not a mime", []byte("x"), 400},
	} {
		w := f.call("POST", "/api/upload", tc.data, "page", map[string]string{"Content-Type": tc.mime})
		if w.Code != tc.status || f.json(w)["hint"] == "" {
			t.Errorf("upload %s did not refuse with a hint: %d %s", tc.name, w.Code, w.Body)
		}
	}
	serial, _ := f.mail([]byte("incoming"), "text/plain")
	answer, _ := json.Marshal(map[string]any{"serial": serial, "body": "like this", "attachments": []string{blob}, "submission": "reply-files"})
	w := f.call("POST", "/api/act/respond", answer, "page", nil)
	if w.Code != 200 || f.json(w)["files_error"] != nil {
		t.Fatalf("answer with files: %d %s", w.Code, w.Body)
	}
	got, err := f.eng.GetBlob(f.ctx, f.agent, blob, "inline")
	if err != nil || !bytes.Equal(got["bytes"].([]byte), data) {
		t.Fatalf("asker did not receive the reply attachment bytes: %v %v", got, err)
	}
	newFile := f.call("POST", "/api/upload", []byte("a new message file"), "page", map[string]string{"Content-Type": "text/plain"})
	if newFile.Code != 200 {
		t.Fatalf("new-message upload: %d %s", newFile.Code, newFile.Body)
	}
	newBlob := f.json(newFile)["blob"].(string)
	send, _ := json.Marshal(map[string]any{"to": "asker", "body": "a new message", "attachments": []string{newBlob}, "submission": "new-message"})
	w = f.call("POST", "/api/act/send", send, "page", nil)
	if w.Code != 200 {
		t.Fatalf("send with files: %d %s", w.Code, w.Body)
	}
	got, err = f.eng.GetBlob(f.ctx, f.agent, newBlob, "inline")
	if err != nil || string(got["bytes"].([]byte)) != "a new message file" {
		t.Fatalf("new-message file did not reach its recipient: %v %v", got, err)
	}
	failed, _ := f.mail([]byte("another question"), "text/plain")
	answer, _ = json.Marshal(map[string]any{"serial": failed, "body": "recorded", "attachments": []string{"sha256:" + strings.Repeat("0", 64)}})
	w = f.call("POST", "/api/act/respond", answer, "page", nil)
	if w.Code != 200 || !strings.Contains(fmt.Sprint(f.json(w)["files_error"]), "recorded") {
		t.Fatalf("file failure concealed a recorded reply: %d %s", w.Code, w.Body)
	}
	all, err := f.eng.AllMessages(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range all["messages"].([]*core.Message) {
		if message.Serial == failed {
			if message.State != "answered" || message.Response != "recorded" {
				t.Fatalf("file failure did not preserve its recorded decision: %+v", message)
			}
			return
		}
	}
	t.Fatal("the answered message disappeared")
}

func TestBoardAttachmentExpiryIsNotABrokenImage(t *testing.T) {
	f := newBoardMediaFixture(t)
	serial, id := f.mail([]byte("old image"), "image/png")
	for _, content := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		if _, err := f.eng.HumanPutBlob(f.ctx, []byte(content), "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.eng.MessageAttachment(f.ctx, serial, 0, false); !errors.Is(err, core.ErrBlobEvicted) {
		t.Fatalf("setup: the fixture did not evict its older attachment: %v", err)
	}
	w := f.call("GET", fmt.Sprintf("/api/messages/%d/attachments/0", serial), nil, "page", nil)
	if w.Code != http.StatusGone || f.json(w)["hint"] == "" {
		t.Fatalf("expired file did not say gone with a hint: %d %s", w.Code, w.Body)
	}
	w = f.call("GET", "/api/messages", nil, "page", nil)
	var payload struct {
		Messages []struct {
			Serial      uint64 `json:"serial"`
			Attachments []struct {
				Blob    string `json:"blob"`
				Expired bool   `json:"expired"`
			} `json:"attachments"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, message := range payload.Messages {
		if message.Serial == serial && len(message.Attachments) == 1 && message.Attachments[0].Blob == id && message.Attachments[0].Expired {
			return
		}
	}
	t.Fatalf("mail presentation lost its expired marker: %s", w.Body)
}
