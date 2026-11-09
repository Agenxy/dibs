// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/humankey"
	"github.com/agenxy/dibs/internal/localtransport"
)

// deviceHarness is a board with both doors open on one mux, as dibd has
// them: the owner's Unix socket, where the Mac app grants a device, and the
// relay door over TCP, where the phone then authenticates. Only the code
// identity check is replaced, because the test binary is not the installed
// app; the kernel audit token still has to arrive for a request to get there.
type deviceHarness struct {
	t      *testing.T
	relay  *relayHarness
	native *nativeHumanAPI
	local  *http.Client
	node   string
	dir    string
}

func newDeviceHarness(t *testing.T) *deviceHarness {
	t.Helper()
	useFastRequestPasswordHash(t)
	eng, _ := testEngine(t)
	// A Unix socket path must fit in 104 bytes, which t.TempDir does not.
	sock, err := os.MkdirTemp("", "dibs-dev-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sock) })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "admin.hash"), []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := newAuthGate("board-secret", filepath.Join(dir, "admin.hash"), "127.0.0.1:4777")
	mux := http.NewServeMux()
	human := registerHumanAPI(mux, eng, gate, dir)
	native := registerNativeHumanAPI(mux, eng, human)
	native.verify = func(context.Context, []byte) error { return nil }

	srv := httptest.NewServer(gate.wrap(mux))
	t.Cleanup(srv.Close)
	ln, err := localtransport.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	local := &http.Server{ReadHeaderTimeout: 5 * time.Second, ConnContext: localtransport.ConnContext, Handler: gate.wrap(mux)}
	go func() { _ = local.Serve(ln) }()
	t.Cleanup(func() { _ = local.Close() })
	return &deviceHarness{
		t:      t,
		relay:  &relayHarness{t: t, eng: eng, srv: srv},
		native: native,
		local:  &http.Client{Transport: localtransport.RoundTripper(sock, localtransport.Local, nil), Timeout: 5 * time.Second},
		node:   eng.NodeID(),
		dir:    dir,
	}
}

// app is a request from the Mac app over the owner's socket.
func (d *deviceHarness) app(path string, body any) (int, map[string]any) {
	d.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "https://127.0.0.1:1"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.local.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// phone is a new Secure Enclave key, played by a software one.
func (d *deviceHarness) phone() {
	d.t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		d.t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	d.relay.priv, d.relay.pub, d.relay.key = priv, base64.StdEncoding.EncodeToString(der), ""
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (d *deviceHarness) grant(label, transcript string) (int, map[string]any) {
	d.t.Helper()
	return d.app("/api/human/native/devices", map[string]string{
		"node": d.node, "label": label, "public_key": d.relay.pub, "transcript_digest": transcript,
	})
}

func (d *deviceHarness) stored() []humankey.Key {
	d.t.Helper()
	keys, err := humankey.NewStore(d.dir).List()
	if err != nil {
		d.t.Fatal(err)
	}
	return keys
}

// The phone pairs nearby and never sees a password: the person's click in
// the Mac app grants its key, and the phone then opens its OWN session on
// the existing relay door, signing with the key that was granted. A grant is
// idempotent on the public key, and a retry after a lost answer reads back
// the transcript the key was first granted under, which is how the phone
// tells "added by this attempt" from "added by an earlier one".
func TestThePersonAddsAPhoneFromTheMacAndThePhoneSignsItsOwnSession(t *testing.T) {
	d := newDeviceHarness(t)
	d.phone()
	first := digest("attempt one")
	code, out := d.grant("Studio iPhone", first)
	if code != http.StatusOK {
		t.Fatalf("grant: %d %v", code, out)
	}
	der, _ := base64.StdEncoding.DecodeString(d.relay.pub)
	device, _ := out["device"].(map[string]any)
	if device["key_id"] != humankey.IDOf(der) || device["kind"] != "device" || device["label"] != "Studio iPhone" {
		t.Fatalf("the grant did not answer with the phone's key: %v", out)
	}
	d.relay.key = device["key_id"].(string)
	if s := d.relay.session(); s == "" {
		t.Fatal("the granted phone could not open its own session")
	}

	code, out = d.grant("Studio iPhone", digest("attempt two"))
	if code != http.StatusOK {
		t.Fatalf("retry: %d %v", code, out)
	}
	if again := out["device"].(map[string]any); again["key_id"] != d.relay.key || again["transcript_digest"] != first {
		t.Fatalf("a retry of the same key was not answered with the first grant: %v", out)
	}
	if keys := d.stored(); len(keys) != 1 || keys[0].Kind != humankey.KindDevice || keys[0].Transcript != first {
		t.Fatalf("one phone granted twice is %d stored keys: %+v", len(keys), keys)
	}
	code, out = d.app("/api/human/native/devices/list", map[string]string{"node": d.node})
	if list, _ := out["devices"].([]any); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("devices/list: %d %v", code, out)
	}
}

// Removing a phone from the Mac ends what the phone holds: its session is
// refused at once and it cannot open another. A second removal, which is
// what a retry after a lost answer is, says so and changes nothing.
func TestARemovedPhoneLosesItsSessionAndCannotOpenAnother(t *testing.T) {
	d := newDeviceHarness(t)
	d.phone()
	code, out := d.grant("Studio iPhone", digest("pairing"))
	if code != http.StatusOK {
		t.Fatalf("grant: %d %v", code, out)
	}
	d.relay.key = out["device"].(map[string]any)["key_id"].(string)
	session := d.relay.session()

	code, out = d.app("/api/human/native/devices/revoke", map[string]string{"node": d.node, "key_id": d.relay.key})
	if code != http.StatusOK || out["revoked"] != true {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if code, out := d.relay.post("/api/human/answer", map[string]string{"Authorization": "Bearer " + session},
		map[string]any{"serial": 1, "disposition": "answer", "body": "x"}); code != http.StatusUnauthorized {
		t.Fatalf("a removed phone's session still answered: %d %v", code, out)
	}
	if code, _ := d.relay.post("/api/human/challenge", nil, map[string]string{"key": d.relay.key}); code != http.StatusUnauthorized {
		t.Fatalf("a removed phone was issued a challenge: %d", code)
	}
	code, out = d.app("/api/human/native/devices/revoke", map[string]string{"node": d.node, "key_id": d.relay.key})
	if code != http.StatusOK || out["revoked"] != false {
		t.Fatalf("a repeated removal: %d %v", code, out)
	}
}

// Only the installed app, on this board, with the label and digest the
// pairing bound, adds a key. Every refusal leaves the key store as it was,
// because a refused grant that half-applied would be a phone nobody added.
func TestOnlyTheInstalledAppAddsAPhoneAndARefusalAddsNothing(t *testing.T) {
	d := newDeviceHarness(t)
	d.phone()
	ok := digest("pairing")
	body := func(node, label, transcript string) map[string]string {
		return map[string]string{"node": node, "label": label, "public_key": d.relay.pub, "transcript_digest": transcript}
	}
	// The relay door is open to anybody who can reach the board, and holding
	// the board's secret proves nothing about a person, so neither may grant.
	code, out := d.relay.post("/api/human/native/devices", map[string]string{"X-Dibs-Secret": "board-secret"},
		body(d.node, "Studio iPhone", ok))
	if code != http.StatusUnauthorized {
		t.Fatalf("a grant over TCP: %d %v", code, out)
	}

	d.native.verify = func(context.Context, []byte) error { return errors.New("not the installed notifier") }
	if code, out := d.grant("Studio iPhone", ok); code != http.StatusForbidden {
		t.Fatalf("a grant from another process on this Mac: %d %v", code, out)
	}
	d.native.verify = func(context.Context, []byte) error { return nil }

	for name, c := range map[string]struct {
		req  map[string]string
		want int
	}{
		"another board":     {body("some-other-node", "Studio iPhone", ok), http.StatusConflict},
		"empty label":       {body(d.node, "", ok), http.StatusBadRequest},
		"control character": {body(d.node, "Studio\niPhone", ok), http.StatusBadRequest},
		"label too long":    {body(d.node, strings.Repeat("a", 129), ok), http.StatusBadRequest},
		"digest not hex":    {body(d.node, "Studio iPhone", strings.Repeat("Z", 64)), http.StatusBadRequest},
		"digest uppercase":  {body(d.node, "Studio iPhone", strings.ToUpper(ok)), http.StatusBadRequest},
		"digest short":      {body(d.node, "Studio iPhone", ok[:62]), http.StatusBadRequest},
		"not a key":         {map[string]string{"node": d.node, "label": "x", "public_key": "AAAA", "transcript_digest": ok}, http.StatusBadRequest},
	} {
		if code, out := d.app("/api/human/native/devices", c.req); code != c.want {
			t.Errorf("%s: %d %v, want %d", name, code, out, c.want)
		}
	}
	if keys := d.stored(); len(keys) != 0 {
		t.Fatalf("refused grants stored %d keys: %+v", len(keys), keys)
	}
	if code, out := d.grant(strings.Repeat("a", 128), ok); code != http.StatusOK {
		t.Fatalf("a label of exactly 128 bytes is the wire's bound, not past it: %d %v", code, out)
	}
}
