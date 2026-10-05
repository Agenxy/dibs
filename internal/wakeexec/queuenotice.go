package wakeexec

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

const queueNoticeSuffix = ". It may already be handled."

// queueNoticeAt changes only Dibs' own text on the canonical native queue
// route. Issued means this local admission attempt, not app acceptance or a
// claim that mail is still unread when a durable queue item finally arrives.
func queueNoticeAt(argv []string, at time.Time) []string {
	if !nativeQueueRoute(argv) {
		return argv
	}
	kind, ok := legacyWakeKind(argv[5])
	if !ok {
		return argv
	}
	if kind == "" {
		kind = "coordination"
	}
	out := append([]string(nil), argv...)
	out[5] = fmt.Sprintf("Dibs: a %s notice was issued at %s%s",
		kind, at.UTC().Format(time.RFC3339Nano), queueNoticeSuffix)
	return out
}

// Historical text remains a wire format: dormant writers may still emit it.
// The event vocabulary, not participant text, decides the variable kind.
func legacyWakeKind(text string) (string, bool) {
	kinds := []string{
		"", core.MsgNotify, core.MsgQuestion, core.MsgRequest, core.MsgHandoff,
		"notice", KindContinuation, KindRecheck,
	}
	for _, kind := range kinds {
		if text == Compose(kind) {
			return kind, true
		}
	}
	kind, ok := strings.CutPrefix(text, "Dibs: a new ")
	if !ok {
		return "", false
	}
	kind, ok = strings.CutSuffix(kind, " is waiting.")
	return kind, ok && text == Compose(kind) && core.IsMailEvent("message."+kind)
}

func timestampedWake(text string) bool {
	body, ok := strings.CutPrefix(text, "Dibs: a ")
	if !ok {
		return false
	}
	kind, stamp, ok := strings.Cut(body, " notice was issued at ")
	if !ok {
		return false
	}
	stamp, ok = strings.CutSuffix(stamp, queueNoticeSuffix)
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || stamp != at.UTC().Format(time.RFC3339Nano) {
		return false
	}
	if kind == "coordination" {
		return true
	}
	got, known := legacyWakeKind(Compose(kind))
	return known && got == kind && kind != ""
}
