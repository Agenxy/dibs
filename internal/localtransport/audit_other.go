// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin

package localtransport

import "net"

func peerAuditToken(net.Conn) []byte { return nil }
