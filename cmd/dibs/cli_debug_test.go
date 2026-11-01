// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cliDebugMarker = "dibs CLI debug fixture"

func TestCLIDebugMainHelper(_ *testing.T) {
	if os.Getenv("DIBS_TEST_CLI_LOG_HELPER") != "1" {
		return
	}
	detectedSurface = func(context.Context) (string, error) {
		slog.Debug(cliDebugMarker)
		return "", nil
	}
	os.Args = []string{"dibs", "mcp-stdio"}
	main()
	os.Exit(0) // the test runner's PASS line is not part of the stdio protocol
}

// Invoke the real CLI main, with the same stdio transport as a harness. A
// helper that merely sets a logger would pass with the production wire absent.
func TestCLIDebugUsesStderrAndKeepsStdioAsJSONRPC(t *testing.T) {
	for _, debug := range []string{"", "1"} {
		t.Run("debug="+debug, func(t *testing.T) {
			dir := cliIPCProfile(t)
			for name, body := range map[string]string{"local.secret": strings.Repeat("a", 64), "dibs.toml": "[wake]\nsockets = false\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			srv := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Method string }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Method != "tools/list" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIDebugMainHelper$")
			cmd.Dir = dir
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "USERPROFILE=" + dir,
				"TMPDIR=" + os.TempDir(), "TEMP=" + os.TempDir(), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
				"DIBS_TEST_CLI_LOG_HELPER=1", "DIBS_DIR=" + dir, "DIBS_ADDR=" + srv.URL,
				"DIBS_HOST_ID=startup", "DIBS_NOTIFY=off", "DIBS_LOG_DEBUG=" + debug,
			}
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			errors, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			firstFrame, framesDone := make(chan []byte, 1), make(chan []byte, 1)
			go func() {
				reader := bufio.NewReader(out)
				first, _ := reader.ReadBytes('\n')
				firstFrame <- first
				rest, _ := io.ReadAll(reader)
				framesDone <- append(first, rest...)
			}()
			debugSeen, errorsDone := make(chan struct{}, 1), make(chan string, 1)
			go func() {
				var text strings.Builder
				scanner := bufio.NewScanner(errors)
				for scanner.Scan() {
					line := scanner.Text()
					text.WriteString(line + "\n")
					if strings.Contains(line, cliDebugMarker) {
						select {
						case debugSeen <- struct{}{}:
						default:
						}
					}
				}
				errorsDone <- text.String()
			}()
			if _, err := io.WriteString(in, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case first := <-firstFrame:
				if !json.Valid(bytes.TrimSpace(first)) {
					t.Fatalf("stdout was not a JSON-RPC frame: %q", first)
				}
			case <-ctx.Done():
				t.Fatal("CLI stdio startup never returned a frame")
			}
			if debug != "" {
				select {
				case <-debugSeen:
				case <-ctx.Done():
					t.Fatal("DIBS_LOG_DEBUG did not enable Debug at the actual CLI entry")
				}
			}
			_ = in.Close()
			var frames []byte
			var diagnostic string
			select {
			case frames = <-framesDone:
			case <-ctx.Done():
				t.Fatal("CLI did not finish stdout after EOF")
			}
			select {
			case diagnostic = <-errorsDone:
			case <-ctx.Done():
				t.Fatal("CLI did not finish stderr after EOF")
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("CLI exit: %v; stderr=%s", err, diagnostic)
			}
			if strings.Contains(diagnostic, cliDebugMarker) != (debug != "") {
				t.Fatalf("Debug switch disagrees with stderr: %q", diagnostic)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(frames), []byte{'\n'}) {
				var frame map[string]any
				if json.Unmarshal(line, &frame) != nil || frame["jsonrpc"] != "2.0" {
					t.Fatalf("non-protocol output on stdout: %q", line)
				}
			}
		})
	}
}
