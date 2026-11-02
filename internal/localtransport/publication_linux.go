// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import "golang.org/x/sys/unix"

func renameSocketExclusive(from, into string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, into, unix.RENAME_NOREPLACE)
}
