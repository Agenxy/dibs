// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Read approval through the same Codex and home used by this installer. Never
// transfer trust to changed bytes, and never roll back a verified install just
// because a separate read-only approval probe is unavailable.
func (p *codexPluginPlan) reportHookApproval(out io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app, err := startAppServerForHome(ctx, p.bin, p.home)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Codex hook approval is unverified: %v. Run `dibs codex-hooks` to inspect it.\n", err)
		return
	}
	defer app.close()
	hooks, err := app.dibsHooks()
	if err != nil || len(hooks) == 0 {
		_, _ = fmt.Fprintln(out, "Codex hook approval is unverified: the hook list is unavailable or empty. "+
			"Run `dibs codex-hooks` to inspect it.")
		return
	}
	pending, disabled := sortHooks(hooks)
	switch {
	case len(pending) > 0:
		_, _ = fmt.Fprintf(out, "Codex hooks need re-trust: %d of %d are untrusted or modified. "+
			"A person must review them with `dibs codex-hooks --trust` or Codex `/hooks`.\n", len(pending), len(hooks))
	case len(disabled) > 0:
		_, _ = fmt.Fprintf(out, "Codex reports %d of %d Dibs hooks switched off. Enable them in `/hooks`; "+
			"`dibs codex-hooks --trust` cannot change that switch.\n", len(disabled), len(hooks))
	default:
		_, _ = fmt.Fprintf(out, "Codex reports all %d Dibs hooks approved and on.\n", len(hooks))
	}
}
