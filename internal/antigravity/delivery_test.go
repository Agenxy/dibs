// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const testConversation = "01a10520-0000-7000-8000-000000000007"

// The same executable doubles as the native command fixture. The production
// caller supplies the explicit environment and owns Unix/Windows argv quoting.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "agentapi" {
		os.Exit(agentAPI())
	}
	os.Exit(m.Run())
}

func agentAPI() int {
	if os.Getenv("ANTIGRAVITY_LS_ADDRESS") != "127.0.0.1:38199" ||
		os.Getenv("ANTIGRAVITY_CSRF_TOKEN") != "fixture-token" {
		return 2
	}
	var response map[string]any
	switch {
	case len(os.Args) == 4 && os.Args[2] == "get-conversation-metadata" && os.Args[3] == testConversation:
		response = map[string]any{"conversationMetadata": map[string]any{"metadata": map[string]any{}}}
	case len(os.Args) == 6 && os.Args[2] == "send-message" && os.Args[3] == "--title=Dibs" && os.Args[4] == testConversation:
		content := os.Args[5]
		if content != "Dibs: a new notify is waiting." {
			content = "different message"
		}
		response = map[string]any{"sendMessage": map[string]any{"recipientId": testConversation, "content": content}}
	default:
		return 3
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"response": response}); err != nil {
		return 4
	}
	return 0
}

func TestAntigravityNativeCommandUsesExactTargetAndReceipt(t *testing.T) {
	d := descriptor{HTTPPort: 38199, Token: "fixture-token"}
	if err := verifyWorkspace(t.Context(), os.Args[0], d, testConversation, ""); err != nil {
		t.Fatal("metadata for a folderless conversation was rejected", err)
	}
	if err := verifyWorkspace(t.Context(), os.Args[0], d, testConversation, t.TempDir()); err == nil {
		t.Fatal("folderless conversation was accepted for a checkout")
	}
	if err := deliver(t.Context(), os.Args[0], d, testConversation, "Dibs: a new notify is waiting."); err != nil {
		t.Fatal("matching native acceptance was rejected", err)
	}
	if err := deliver(t.Context(), os.Args[0], d, testConversation, "ambiguous"); err == nil {
		t.Fatal("mismatched native acceptance was treated as delivery")
	}
}

func TestAntigravityWorkspaceUsesAbsoluteLocalPathBoundaries(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("the Antigravity desktop route is macOS-only")
	}
	root := t.TempDir()
	uris := []string{"file://" + root}
	if !workspaceMatches(filepath.Join(root, "subproject"), uris) ||
		workspaceMatches(root+"-sibling", uris) ||
		workspaceMatches(root, []string{"file://other-host" + root}) {
		t.Fatal("workspace verification crossed a host or project boundary")
	}
}

func TestSelectHubDescriptorRequiresOneLiveCandidate(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini", "antigravity", "daemon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(pid, port int) {
		t.Helper()
		data, err := json.Marshal(descriptor{PID: pid, HTTPPort: port, Token: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hub_"+strconv.Itoa(port)+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(42, 38198)
	write(43, 38199)
	onlyCurrent := func(d descriptor) bool { return d.PID == 43 }
	if d, ok := selectHubDescriptor(home, onlyCurrent); !ok || d.HTTPPort != 38199 {
		t.Fatal("stale descriptor obscured the live desktop hub")
	}
	if _, ok := selectHubDescriptor(home, func(descriptor) bool { return false }); ok {
		t.Fatal("selected a descriptor without a live desktop")
	}
	write(43, 38200)
	if _, ok := selectHubDescriptor(home, onlyCurrent); ok {
		t.Fatal("selected an ambiguous live descriptor")
	}
}
