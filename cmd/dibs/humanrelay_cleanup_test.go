package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/humanask"
)

func relayCleanupHelper() int {
	if len(os.Args) != 2 || os.Args[1] != "--remove-messages=dibs.msg.board-A.7" {
		return 3
	}
	_, _ = os.Stdout.WriteString(`{"cleanup":"requested"}`)
	return 0
}

func TestActualRelayStreamRemovesThroughItsNativeHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS relay route")
	}
	if os.Getenv("DIBS_TEST_RELAY_CLEANUP_DRIVER") == "1" {
		testRelayCleanupStream(t)
		return
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	driver, helper := filepath.Join(dir, "driver.test"), filepath.Join(dir, "Dibs.app/Contents/MacOS/dibs-notify")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{driver, helper} {
		if err := os.WriteFile(path, bytes, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestActualRelayStreamRemovesThroughItsNativeHelper$") // #nosec G204 -- private test fixture
	cmd.Env = append(os.Environ(), "DIBS_TEST_RELAY_CLEANUP_DRIVER=1", "DIBS_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("relay stream/native wiring: %v\n%s", err, out)
	}
}

func testRelayCleanupStream(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipts := make(chan map[string]any, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/human/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"serial\":0,\"notification_cleanup\":{\"node\":\"board-A\",\"serials\":[7]}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /api/human/delivery", func(w http.ResponseWriter, r *http.Request) {
		var result map[string]any
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			t.Error(err)
		}
		receipts <- result
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := newRelay(srv.URL, relayState{Node: "board-A"}, &fakeSigner{}, func(humanask.Message) (humanask.Answer, error) {
		t.Error("cleanup was shown as a human question")
		return humanask.Answer{}, nil
	})
	r.session = "fixture-session"
	r.busy[7] = true // cleanup still arrives while this message is being shown
	done := make(chan error, 1)
	go func() { done <- r.attach(ctx) }()
	select {
	case receipt := <-receipts:
		if receipt["serial"] != float64(7) || receipt["state"] != "cleanup_requested" {
			t.Fatalf("native removal receipt: %v", receipt)
		}
	case <-ctx.Done():
		t.Fatal("real relay stream never requested native cleanup")
	}
	cancel()
	<-done
}
