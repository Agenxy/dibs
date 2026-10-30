// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import "github.com/agenxy/dibs/internal/localtransport"

func (c Config) validateClientTransport() error {
	if c.ClientTransport == "" {
		return nil
	}
	_, err := localtransport.Parse(c.ClientTransport)
	return err
}
