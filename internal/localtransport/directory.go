// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import "time"

// DirectoryStatus is a read-only filesystem preflight, available before an old
// daemon is replaced. It does not read credentials or contact any TCP listener.
type DirectoryStatus struct {
	Mode        string
	UID         uint64
	GID         uint64
	WillTighten bool
}

// DirectoryTightening is a derived startup diagnostic, never coordination state.
type DirectoryTightening struct {
	FromMode    string    `json:"from_mode"`
	At          time.Time `json:"at"`
	NoticeError string    `json:"-"`
}
