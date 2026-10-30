// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func peerIsUser(fd int) error {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return err
	}
	if uint64(cred.Uid) != uint64(os.Geteuid()) { // #nosec G115 -- Unix euid is nonnegative.
		return fmt.Errorf("connected IPC peer belongs to another OS user; no credentials were sent")
	}
	return nil
}
