// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/plugins"
	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/sibling"
)

// Observations, not approvals. The existing client monitor calls this at
// startup and checks the resolved runtime's file identity on later passes.
// The expensive read-only hooks/list query runs only when that identity changes.
type codexTrustWatch struct {
	previous     string
	previousInfo os.FileInfo
	warnings     []clientDriftWarning
	resolve      func() string
	inspect      func(context.Context) (plugins.CodexHookReport, error)
	enabled      func() bool
}

var codexTrustObserver = newCodexTrustWatch

func newCodexTrustWatch() *codexTrustWatch {
	return &codexTrustWatch{resolve: sibling.Codex, inspect: readCodexHookReport, enabled: func() bool {
		home, err := plugins.CodexHome()
		if err != nil {
			return false
		}
		installed, _ := plugins.CodexInstalled(home)
		return installed || plugins.CodexHookSourceShipped(filepath.Join(home, "hooks.json"))
	}}
}

func (w *codexTrustWatch) observe(ctx context.Context) []clientDriftWarning {
	if w.enabled != nil && !w.enabled() {
		w.previous = ""
		w.warnings = nil
		return nil
	}
	bin := w.resolve()
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() {
		w.previous = ""
		w.warnings = nil
		return nil
	}
	if w.previous == bin && sameCodexRuntime(info, w.previousInfo) {
		return w.warnings
	}
	w.previous = bin
	w.previousInfo = info
	w.warnings = nil
	identity := fmt.Sprintf("%s:%d:%d", bin, info.Size(), info.ModTime().UnixNano())
	digest, err := codexRuntimeDigest(bin)
	if err != nil {
		return w.unverified(identity)
	}
	identity += ":" + digest
	report, err := w.inspect(ctx)
	if ctx.Err() != nil {
		return nil
	}
	key := fmt.Sprintf("codex-hook-trust-%x", sha256.Sum256([]byte(identity)))
	current, statErr := os.Stat(bin)
	if err != nil || report.Binary != bin || statErr != nil || !sameCodexRuntime(info, current) {
		return w.unverified(identity)
	}
	w.warnings = codexHookWarnings(report, key)
	return w.warnings
}

func sameCodexRuntime(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() &&
		a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

func codexRuntimeDigest(bin string) (string, error) {
	f, err := os.Open(bin) // #nosec G304 G703 -- locally resolved executable; binary digest only.
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, err = io.Copy(digest, f)
	_ = f.Close()
	return fmt.Sprintf("%x", digest.Sum(nil)), err
}

func (w *codexTrustWatch) unverified(identity string) []clientDriftWarning {
	w.warnings = []clientDriftWarning{{
		fmt.Sprintf("codex-hook-trust-%x", sha256.Sum256([]byte(identity))),
		"Codex hook approval could not be verified after a runtime change",
		"Run `dibs codex-hooks` to inspect hook approval; Dibs changed no trust or enablement.",
	}}
	return w.warnings
}

func codexHookWarnings(report plugins.CodexHookReport, key string) []clientDriftWarning {
	var warnings []clientDriftWarning
	unchanged, changed := 0, 0
	for _, h := range report.Hooks {
		if h.Trust != "modified" {
			continue
		}
		if h.ShippedContent {
			unchanged++
		} else {
			changed++
		}
	}
	if unchanged > 0 {
		warnings = append(warnings, clientDriftWarning{
			key,
			fmt.Sprintf("Codex reports %d unchanged Dibs hooks as modified; lifecycle delivery is not approved", unchanged),
			"Review the hooks with `dibs codex-hooks` and Codex `/hooks`; verify `dibs doctor` afterwards. " +
				"Dibs changed no hook trust or enablement.",
		})
	}
	if changed > 0 {
		warnings = append(warnings, clientDriftWarning{
			key + "-content",
			fmt.Sprintf("Codex reports %d modified Dibs hooks whose source is different or unverified", changed),
			"Run `dibs codex-hooks` and review the hook source in Codex `/hooks` before approving it. " +
				"Dibs did not trust content that differs from its shipped hooks.",
		})
	}
	return warnings
}

// Query our paired CLI rather than duplicating Codex's app-server protocol.
// --json is mutually exclusive with --trust. No thread is started or resumed,
// and no config/batchWrite or hook enablement operation is issued here.
func readCodexHookReport(ctx context.Context) (plugins.CodexHookReport, error) {
	var report plugins.CodexHookReport
	self, err := os.Executable()
	if err != nil {
		return report, err
	}
	cli := filepath.Join(filepath.Dir(self), "dibs")
	if _, err := os.Stat(cli); err != nil {
		cli = sibling.Find("dibs")
	}
	ctx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	// #nosec G204 G702 -- paired Dibs CLI, fixed read-only arguments.
	cmd := procexec.Command(ctx, cli, "codex-hooks", "--json")
	cmd.Env = append(os.Environ(), "DIBS_ADMIN=1")
	cmd.WaitDelay = 100 * time.Millisecond
	var output codexHookBuffer
	cmd.Stdout = &output
	err = cmd.Run()
	if err != nil {
		return report, err
	}
	err = json.Unmarshal(output, &report)
	return report, err
}

type codexHookBuffer []byte

func (b *codexHookBuffer) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-len(*b) {
		return 0, errors.New("hook report exceeds its metadata bound")
	}
	*b = append(*b, p...)
	return len(p), nil
}
