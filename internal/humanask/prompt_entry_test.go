// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package humanask

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/testexec"
)

// The engine/relay entry, the installed-helper resolver and both anonymous
// broker pipes run for real. Only the human's UI gesture is doubled. The
// legacy --pick branch is safe too, so the old-code control cannot open UI.
func promptEntryHelper() {
	if len(os.Args) > 1 && os.Args[1] == "--pick" {
		_, _ = os.Stdout.WriteString("fourth choice\n")
		return
	}
	if len(os.Args) != 2 || os.Args[1] != "--broker" {
		os.Exit(3)
	}
	out := json.NewEncoder(os.Stdout)
	old := os.Getenv("DIBS_PROMPT_OLD_HELPER") == "1"
	ready := map[string]any{"v": 1, "event": "ready"}
	if !old {
		ready["reply"] = 1
	}
	_ = out.Encode(ready)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		var p struct {
			ID, ReplyKind    string
			Choices, Buttons []string
			Detail           struct{ Sender, Body string }
		}
		if json.Unmarshal(scanner.Bytes(), &p) != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv("DIBS_PROMPT_ENTRY_CAPTURE"), scanner.Bytes(), 0o600); err != nil {
			os.Exit(3)
		}
		_ = out.Encode(map[string]any{"v": 1, "id": p.ID, "event": "posted", "receipt": map[string]string{"state": "posted"}})
		if p.ReplyKind != "" {
			action := "answer"
			choice := "fourth choice"
			if p.ReplyKind == "request" {
				action = "comment"
				choice = ""
			}
			_ = out.Encode(map[string]any{"v": 1, "id": p.ID, "event": "reply", "action": action, "choice": choice, "text": "typed note"})
		} else {
			if len(p.Buttons) == 0 {
				continue
			}
			choice := p.Buttons[0]
			for _, button := range p.Buttons {
				if button == "Approve" || button == "Pick one…" {
					choice = button
				}
			}
			_ = out.Encode(map[string]any{"v": 1, "id": p.ID, "event": "action", "action": choice})
		}
	}
}

func TestPromptEntryPreservesWholeMessageChoicesAndTypedReply(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS entry")
	}
	if os.Getenv("DIBS_PROMPT_ENTRY_DRIVER") == "1" {
		choices := []string{"first choice", "second choice", "third choice", "fourth choice"}
		for index, mode := range []string{"full-message", "choice-and-note", "request-comment"} {
			t.Run(mode, func(t *testing.T) {
				body := "short message"
				kind := core.MsgQuestion
				opts := choices
				if mode == "full-message" {
					body = strings.Repeat("paragraph with `code` and a line break\n\n", 350) + "END OF MESSAGE"
				}
				if mode == "request-comment" {
					kind = core.MsgRequest
					opts = nil
				}
				posted := false
				got, err := Ask(Message{
					Type: kind, From: "sender", Who: "full context", Body: body, Choices: opts,
					Node: "prompt-entry", Serial: uint64(7 + index), Receipt: func(s string) { posted = s == "posted" },
				})
				if err != nil {
					t.Fatal(err)
				}
				if !posted {
					t.Fatal("real helper posting receipt missing")
				}
				raw, err := os.ReadFile(os.Getenv("DIBS_PROMPT_ENTRY_CAPTURE"))
				if err != nil {
					t.Fatal(err)
				}
				var sent struct {
					Choices []string
					Detail  struct{ Body string }
				}
				if err := json.Unmarshal(raw, &sent); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "full-message":
					if sent.Detail.Body != body {
						t.Fatalf("full message lost: got %d bytes, want %d", len(sent.Detail.Body), len(body))
					}
				case "choice-and-note":
					if strings.Join(sent.Choices, "|") != strings.Join(choices, "|") {
						t.Errorf("prompt choices lost: %v", sent.Choices)
					}
					if got.Disposition != "answer" || got.Body != "fourth choice\n\ntyped note" {
						t.Fatalf("question reply lost: %+v", got)
					}
				case "request-comment":
					if got.Disposition != "comment" || got.Body != "typed note" {
						t.Fatalf("typed-only request became a verdict: %+v", got)
					}
				}
			})
		}
		return
	}
	dir := t.TempDir()
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	driver, helper := filepath.Join(dir, "driver.test"), filepath.Join(dir, "Dibs.app", "Contents", "MacOS", "dibs-notify")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{driver, helper} {
		if err := os.WriteFile(path, image, 0o700); err != nil {
			t.Fatal(err)
		}
		testexec.PrepareExecutable(t, exec.Command(path, "--dibs-test-prepare"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestPromptEntryPreservesWholeMessageChoicesAndTypedReply$")
	cmd.Env = append(os.Environ(), "DIBS_PROMPT_ENTRY_DRIVER=1", "DIBS_PROMPT_ENTRY_CAPTURE="+filepath.Join(dir, "capture.json"), "DIBS_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real prompt entry: %v\n%s", err, out)
	}
}

func TestOldNativeHelperStillPostsWithoutClaimingTypedReply(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS installed helper")
	}
	if os.Getenv("DIBS_PROMPT_ENTRY_DRIVER") == "1" {
		for _, kind := range []string{core.MsgRequest, "two choices", "four choices", "typed only"} {
			m := Message{Type: core.MsgQuestion, From: "sender", Body: "full request", Node: "old-prompt", Serial: 7}
			wantBody, wantDisposition := "", "answer"
			switch kind {
			case core.MsgRequest:
				m.Type = core.MsgRequest
				wantDisposition = "approve"
			case "two choices":
				m.Choices = []string{"yes", "no"}
				wantBody = "yes"
			case "four choices":
				m.Choices = []string{"first", "second", "third", "fourth choice"}
				wantBody = "fourth choice"
			default:
				wantDisposition = ""
			}
			posted := false
			var available *bool
			hint := ""
			m.DeliveryReceipt = func(r notify.ReceiptData) { posted = r.State == "posted"; available = r.ReplyField; hint = r.ReplyHint }
			got, err := Ask(m)
			if err != nil || got.Disposition != wantDisposition || (wantBody != "" && got.Body != wantBody) || !posted {
				t.Fatalf("old helper %s: %+v posted=%v %v", kind, got, posted, err)
			}
			if available == nil || *available || hint != notify.LegacyReplyHint {
				t.Fatalf("old helper falsely claims reply field: %v %q", available, hint)
			}
			raw, err := os.ReadFile(os.Getenv("DIBS_PROMPT_ENTRY_CAPTURE"))
			if err != nil {
				t.Fatal(err)
			}
			var sent struct {
				ReplyKind, Subtitle string
				Buttons             []string
			}
			if err := json.Unmarshal(raw, &sent); err != nil {
				t.Fatal(err)
			}
			if sent.ReplyKind != "" {
				t.Fatal("new reply protocol sent to old helper")
			}
			if kind == "typed only" && (len(sent.Buttons) != 0 || !strings.Contains(sent.Subtitle, "Dibs board")) {
				t.Fatalf("typed-only question has no honest board answer route: %+v", sent)
			}
		}
		return
	}
	dir := t.TempDir()
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	driver, helper := filepath.Join(dir, "driver.test"), filepath.Join(dir, "Dibs.app", "Contents", "MacOS", "dibs-notify")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{driver, helper} {
		if err := os.WriteFile(path, image, 0o700); err != nil {
			t.Fatal(err)
		}
		testexec.PrepareExecutable(t, exec.Command(path, "--dibs-test-prepare"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestOldNativeHelperStillPostsWithoutClaimingTypedReply$")
	cmd.Env = append(os.Environ(), "DIBS_PROMPT_ENTRY_DRIVER=1", "DIBS_PROMPT_OLD_HELPER=1", "DIBS_PROMPT_ENTRY_CAPTURE="+filepath.Join(dir, "capture.json"), "DIBS_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real old-helper entry: %v\n%s", err, out)
	}
}
