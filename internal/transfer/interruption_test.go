// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type canceledUploadBody struct{ sent bool }

func (b *canceledUploadBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, context.Canceled
	}
	b.sent = true
	return copy(p, "12"), context.Canceled
}

func (*canceledUploadBody) Close() error { return nil }

// Enter the actual handler and cancel the BODY after accepted bytes, not the
// authorization context before any bytes can legitimately be received.
func TestCanceledRequestBodyRetainsAcceptedUploadBytes(t *testing.T) {
	m, token, _ := transferFixture(t)
	result, err := authorizeEight(t, m, token)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := result["upload"].(Descriptor)
	handler := m.Handler("", false)
	r := httptest.NewRequest(http.MethodPatch, descriptor.URL, &canceledUploadBody{})
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/partial-upload")
	r.Header.Set("Upload-Offset", "0")
	r.Header.Set("Upload-Complete", "?1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Upload-Offset") != "2" {
		t.Fatalf("body cancellation discarded accepted bytes: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodHead, descriptor.URL, nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("Upload-Offset") != "2" {
		t.Fatalf("retained offset unavailable: %d %v", w.Code, w.Header())
	}
	r = httptest.NewRequest(http.MethodPatch, descriptor.URL, strings.NewReader("345678"))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/partial-upload")
	r.Header.Set("Upload-Offset", "2")
	r.Header.Set("Upload-Complete", "?1")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var finished struct {
		Blob string
		Size int64
	}
	if err := json.Unmarshal(w.Body.Bytes(), &finished); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("12345678")))
	if w.Code != http.StatusOK || finished.Size != 8 || finished.Blob != want {
		t.Fatalf("canceled prefix could not finish: %d %s", w.Code, w.Body.String())
	}
}
