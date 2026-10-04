package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRegistryRetryRequiresEquivalenceThroughActualPublishDoor(t *testing.T) {
	for _, mode := range []string{"already", "new", "ambiguous", "different", "unavailable", "false-success"} {
		t.Run(mode, func(t *testing.T) {
			const manifest = `{"name":"io.github.Agenxy/dibs","version":"0.0.11","packages":[{"fileSha256":"pinned"}]}`
			path := filepath.Join(t.TempDir(), "server.json")
			if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			var present atomic.Bool
			present.Store(mode == "already" || mode == "different")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != "/v0.1/servers/io.github.Agenxy%2Fdibs/versions/0.0.11" {
					t.Errorf("wrong lookup: %s %s", r.Method, r.URL.EscapedPath())
					w.WriteHeader(400)
					return
				}
				if mode == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if !present.Load() {
					w.WriteHeader(404)
					return
				}
				body := manifest
				if mode == "different" {
					body = `{"name":"io.github.Agenxy/dibs","version":"0.0.11"}`
				}
				_, _ = w.Write([]byte(`{"server":` + body + `}`))
			}))
			defer server.Close()
			writes := 0
			err := publish(context.Background(), server.Client(), server.URL, path, func() error {
				writes++
				present.Store(mode != "false-success")
				if mode == "ambiguous" {
					return errors.New("connection reset after apply")
				}
				return nil
			})
			good := mode == "already" || mode == "new" || mode == "ambiguous"
			if (err == nil) != good {
				t.Fatalf("mode%s: %v", mode, err)
			}
			if (mode == "already" || mode == "different" || mode == "unavailable") && writes != 0 {
				t.Fatal("wrote after existing/unverified registry result")
			}
		})
	}
}
