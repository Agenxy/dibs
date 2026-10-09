// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import "github.com/agenxy/dibs/internal/core"

// Private IPC still enters as user input. Names are data only when bounded
// to ^[a-z0-9][a-z0-9-]{0,62}$; other registered names are omitted entirely.
func nativeSenderSlug(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i := range len(s) {
		alnum := (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= '0' && s[i] <= '9')
		if !alnum && (i == 0 || s[i] != '-') {
			return false
		}
	}
	return true
}

func nativeNoticeKind(kind string) bool {
	switch kind {
	case core.MsgNotify, core.MsgQuestion, core.MsgRequest, core.MsgHandoff, "notice", "coordination", "recheck":
		return true
	}
	return core.IsMailEvent("message." + kind)
}
