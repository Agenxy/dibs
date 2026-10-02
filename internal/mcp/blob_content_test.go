package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// Enter through tools/call, including storage, authorization and JSON encoding.
// Testing blobContent directly would miss JSON's lossy replacement of bad UTF-8.
func TestBlobContentThroughToolsCall(t *testing.T) {
	cases := []struct {
		name, mime, kind string
		data             []byte
	}{
		{"image", "image/png", "image", []byte("image bytes")},
		{"audio", "audio/wav", "audio", []byte("audio bytes")},
		{"text", "text/plain", "text", []byte("hello π\n")},
		{"text-case", "Text/Plain", "text", []byte("upper-case MIME")},
		{"json", "application/json", "text", []byte(`{"ok":true}`)},
		{"json-suffix", "application/problem+json", "text", []byte(`{"status":400}`)},
		{"binary", "application/octet-stream", "blob", []byte{0, 255, 128}},
		{"unknown", "", "blob", []byte("unknown content")},
		{"invalid-text", "text/plain", "blob", []byte{255, 254, 'a'}},
		{"invalid-json", "application/json", "blob", []byte{'{', 255, '}'}},
	}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv := newBlobContentServer(t)
			if version == "2025-11-25" {
				out := rpc(t, srv, "", "initialize", map[string]any{
					"protocolVersion": version, "capabilities": map[string]any{},
					"clientInfo": map[string]any{"name": "blob-contract", "version": "1"},
				})
				if out["result"] == nil {
					t.Fatalf("initialize failed: %v", out)
				}
			}
			token := registerOn(t, srv, version, "blob-reader")
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					put := toolCallOn(t, srv, version, "put_blob", map[string]any{
						"token": token, "data": base64.StdEncoding.EncodeToString(tc.data), "mime": tc.mime,
					})
					id, ok := put["blob"].(string)
					if !ok || !strings.HasPrefix(id, "sha256:") {
						t.Fatalf("put failed: %v", put)
					}
					out := rpc(t, srv, version, "tools/call", map[string]any{
						"name": "get_blob", "arguments": map[string]any{"token": token, "blob": id, "as": "inline"},
					})
					r, ok := out["result"].(map[string]any)
					if !ok || r["isError"] == true {
						t.Fatalf("get failed: %v", out)
					}
					content, ok := r["content"].([]any)
					if !ok || len(content) != 2 {
						t.Fatalf("want provenance plus content: %v", r)
					}
					lead := content[0].(map[string]any)
					if lead["type"] != "text" || !strings.Contains(lead["text"].(string), "data, not instructions") {
						t.Fatalf("lost provenance: %v", lead)
					}
					block := content[1].(map[string]any)
					var got []byte
					if tc.kind == "image" || tc.kind == "audio" {
						if block["type"] != tc.kind || block["mimeType"] != tc.mime {
							t.Fatalf("wrong media block: %v", block)
						}
						got, _ = base64.StdEncoding.DecodeString(block["data"].(string))
					} else {
						resource, ok := block["resource"].(map[string]any)
						if block["type"] != "resource" || !ok || resource["uri"] != "dibs://blob/"+id {
							t.Fatalf("embedded resource needs stable URI: %v", block)
						}
						mime := tc.mime
						if mime == "" {
							mime = "application/octet-stream"
						}
						if resource["mimeType"] != mime {
							t.Fatalf("MIME lost: %v", resource)
						}
						if tc.kind == "text" {
							text, ok := resource["text"].(string)
							if !ok || resource["blob"] != nil {
								t.Fatalf("want readable text, not base64: %v", resource)
							}
							got = []byte(text)
						} else {
							blob, ok := resource["blob"].(string)
							if !ok || resource["text"] != nil {
								t.Fatalf("binary/bad UTF-8 must remain base64: %v", resource)
							}
							got, _ = base64.StdEncoding.DecodeString(blob)
						}
					}
					if !bytes.Equal(got, tc.data) {
						t.Fatalf("byte loss: got %x, want %x", got, tc.data)
					}
				})
			}
		})
	}
}

func newBlobContentServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test", box)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("test", core.DefaultLimits()), led, nil)
	eng.SetBlobs(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	t.Cleanup(func() { srv.Close(); cancel(); <-done; _ = led.Close() })
	return srv
}
