// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"context"
	"errors"
	"log/slog"

	"github.com/agenxy/dibs/internal/codexipc"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// NativeOutcome distinguishes confirmed app input from cold queue admission.
// An unknown native result must not arm the command route's automatic retry.
type NativeOutcome struct {
	OK, Settled, NoRetry bool
	Disposition          string
	Detail               string
}

// NativeEligible restricts replacement to the agent's own derived app and
// the canonical local queue shape. Config/profile overrides keep their route.
func NativeEligible(surface string, f Fields, argv []string) bool {
	thread, ok := queueTarget(argv)
	return surface == harnessenv.ChatGPTApp && ok && nativeQueueRoute(argv) &&
		core.LooksLikeThreadID(thread) && thread == f.Thread && argv[5] == f.Message
}

// ComposeNative carries names only over private app IPC, never over argv.
// Structured type/sender facts are composed on the recipient's machine;
// this channel carries neither an imperative nor anybody's message body.
func ComposeNative(f Fields) string {
	if f.MsgType == KindAppRestart {
		return Compose(f.MsgType)
	}
	if !nativeNoticeKind(f.MsgType) {
		return Compose("")
	}
	if nativeSenderSlug(f.From) {
		return "Dibs: new " + f.MsgType + " from " + f.From + "."
	}
	return "Dibs: new " + f.MsgType + "."
}

// TryNative runs BEFORE any queue probe, lock, retained receipt or app opener.
// handled=false means the unchanged cold route should run; only absent socket
// or the router's exact no-client-found permits that. Everything else fails
// loudly. Proven before-input failures and matched refusals can use the
// existing bounded retry; a possibly submitted input never can.
func TryNative(surface string, f Fields, argv []string, fresh func() (string, error)) (NativeOutcome, bool) {
	if !NativeEligible(surface, f, argv) {
		return NativeOutcome{}, false
	}
	if fresh == nil {
		fresh = func() (string, error) { return ComposeNative(f), nil }
	}
	r, err := codexipc.DeliverFresh(context.Background(), f.Thread, fresh)
	if errors.Is(err, codexipc.ErrNoOwner) {
		return NativeOutcome{Disposition: "unloaded"}, false
	}
	if err != nil {
		if codexipc.BeforeInput(err) || errors.Is(err, codexipc.ErrRefused) {
			slog.Warn("native app wake not sent; bounded retry remains available", "agent", f.Agent,
				"err", err, "hint", "inspect the native app connection, protocol or refusal; no queue fallback will run")
			return NativeOutcome{
				Disposition: "not_sent", Detail: "native app input not accepted; bounded retry, no queue fallback",
			}, true
		}
		slog.Error("native app wake not confirmed; no retry or queue fallback", "agent", f.Agent,
			"err", err, "hint", "inspect the app IPC protocol and retry only after resolving the unknown outcome")
		return NativeOutcome{
			NoRetry: true, Disposition: "unknown",
			Detail: "native app input not confirmed; no retry or queue fallback",
		}, true
	}
	settled := r.Disposition == "settled"
	if !settled {
		slog.Info("native app accepted Dibs notice", "agent", f.Agent, "disposition", r.Disposition)
	}
	return NativeOutcome{OK: true, Settled: settled, Disposition: r.Disposition}, true
}
