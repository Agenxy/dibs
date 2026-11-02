// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import "os"

// UnlockAndClose ends a lock's lifetime before closing its descriptor. On Unix
// a fork or dup retains the open-file description, so Close alone need not
// release the lock. Unlock errors remain unreported, as with Unlock; the
// returned error is from Close.
func UnlockAndClose(f *os.File) error {
	Unlock(f)
	return f.Close()
}
