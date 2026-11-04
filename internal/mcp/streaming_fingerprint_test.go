// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/assets"
)

func TestFingerprintToolsPreservesLegacySerialization(t *testing.T) {
	for name, value := range map[string]any{
		"nil": nil,
		"catalog": map[string]any{
			"html": "<&>\u2028", "invalidUTF8": string([]byte{'a', 0xff}),
			"nilSlice": []string(nil), "empty": map[string]int{}, "bytes": []byte{1, 2, 3},
		},
		"raw":         json.RawMessage(`{"x": 1, "x":2}`),
		"unsupported": make(chan int), "nan": math.NaN(),
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(value)
			sum := sha256.Sum256(raw)
			if got, want := FingerprintTools(value), hex.EncodeToString(sum[:8]); got != want {
				t.Fatalf("fingerprint %s differs from legacy bytes %s", got, want)
			}
		})
	}
}

func TestToolsFingerprintMatchesMaterializedPanel(t *testing.T) {
	// Enter through the path restoreCarried uses, before materializing styles.
	got := ToolsFingerprint()
	source := minimalPanelHTML
	if !panelMinimal {
		// Preserve the old three substitutions as an independent oracle; using
		// boardAppTemplate here would inherit a bug in the streaming renderer.
		source = strings.Replace(boardAppHTML, "/*__STYLES__*/", assets.Styles(), 1)
		source = strings.Replace(source, "<!--__PRODUCT_MARK__-->", assets.Icon, 1)
		source = strings.Replace(source, "/*__BOARDJS__*/", assets.BoardJS(), 1)
	}
	sum := sha256.Sum256([]byte(source))
	wantURI := uiBoardBase + "/" + hex.EncodeToString(sum[:])[:12]
	srv, _ := newServer(t)
	reply := rpc(t, srv, "2026-07-28", "tools/list", map[string]any{})
	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("setup: no catalog result: %v", reply)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatal("setup: advertised catalog is empty")
	}
	panels := 0
	for _, entry := range tools {
		tool := entry.(map[string]any)
		meta, _ := tool["_meta"].(map[string]any)
		ui, _ := meta["ui"].(map[string]any)
		if ui["resourceUri"] != nil {
			ui["resourceUri"] = wantURI
			panels++
		}
	}
	if panels == 0 {
		t.Fatal("setup: no panel URI in the real advertised catalog")
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	legacy := sha256.Sum256(raw)
	if want := hex.EncodeToString(legacy[:8]); got != want {
		t.Fatalf("ToolsFingerprint=%s, materialized-panel catalog=%s", got, want)
	}
}
